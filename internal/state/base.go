package state

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/xbcio/xinit/internal/render"
)

// BaseDir 返回 base 树目录。
//
// 这里存的是**渲染后的原始字节**，不做 EOL 归一化：git merge-file 要的
// 是可比的原始字节，不是归一化后的。哈希比较时另行归一化（§9.1）。
func BaseDir(projectDir string) string {
	return filepath.Join(projectDir, DirName, "base")
}

// SyncBase 把渲染结果同步到 .xinit/base/。
//
// 三条规则：
//   - 键用 **Dst**，与 state.files 的键一致；
//   - entries 里有、rendered 里没有 → 报错（调用方 bug，不写空文件）；
//   - base 里有、entries 里没有 → 删除（模板已移除的路径必须清掉，
//     否则 update 会拿到过期的 base）。
//
// 先整体校验、再统一落盘：Dst 可能引用了无 pattern 的变量（值可以是
// "../x"），坏的 Dst 不能留下半棵 base 树。checkWithinBase 的注释已经
// 点名"写入会穿出项目根"——写路径必须和读路径一样守。
//
// rendered 以 Dst 为键。
func SyncBase(projectDir string, entries []render.Entry, rendered map[string][]byte) error {
	base := BaseDir(projectDir)

	want := make(map[string]bool, len(entries))
	for _, e := range entries {
		if err := checkWithinBase(e.Dst); err != nil {
			return err
		}
		if _, ok := rendered[e.Dst]; !ok {
			return fmt.Errorf("内部错误：路径 %q 有 Entry 但没有渲染内容", e.Dst)
		}
		want[e.Dst] = true
	}

	if err := os.MkdirAll(base, 0o755); err != nil {
		return fmt.Errorf("创建 %s: %w", base, err)
	}
	// 先清残片再写：大小写不敏感文件系统上，仅大小写改名（readme.md →
	// README.md）的原子写会落在旧目录项上，后清会把刚写的新 base 一并删掉
	// （§17.8 验收 15）。base 可重建，先删后写的中断窗口无数据风险。
	if err := removeStale(base, want); err != nil {
		return err
	}

	for _, e := range entries {
		mode := fs.FileMode(0o644)
		if e.Exec {
			mode = 0o755
		}
		full := filepath.Join(base, filepath.FromSlash(e.Dst))
		if err := WriteFileAtomic(full, rendered[e.Dst], mode); err != nil {
			return fmt.Errorf("写 base/%s: %w", e.Dst, err)
		}
	}
	return nil
}

// removeStale 删掉 base/ 里不在 want 中的文件，并清理变空的目录。
func removeStale(base string, want map[string]bool) error {
	var dirs []string

	err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == base {
			return nil
		}
		rel, err := filepath.Rel(base, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)

		if d.IsDir() {
			dirs = append(dirs, p)
			return nil
		}
		if !want[rel] {
			return os.Remove(p)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("清理 base: %w", err)
	}

	// 自底向上删空目录（WalkDir 保证父目录在子目录之后出现，
	// 所以逆序处理即可）
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = os.Remove(dirs[i]) // 非空时会失败，忽略
	}
	return nil
}

// ReadBase 读取某个 Dst 的 base 内容。
func ReadBase(projectDir, dst string) ([]byte, error) {
	if err := checkWithinBase(dst); err != nil {
		return nil, err
	}
	full := filepath.Join(BaseDir(projectDir), filepath.FromSlash(dst))
	data, err := os.ReadFile(full)
	if err != nil {
		return nil, fmt.Errorf("读取 base/%s: %w", dst, err)
	}
	return data, nil
}

// checkWithinBase 拒绝绝对路径与 .. 段。
//
// 模板路径可以引用 vars，而变量未必有 pattern —— 值为 "../../x" 时
// 写入会穿出项目根。
func checkWithinBase(dst string) error {
	if dst == "" {
		return fmt.Errorf("base 路径不能为空")
	}
	if path.IsAbs(dst) {
		return fmt.Errorf("base 路径不能是绝对路径: %q", dst)
	}
	for _, seg := range strings.Split(dst, "/") {
		if seg == ".." {
			return fmt.Errorf("base 路径不能含 .. 段: %q", dst)
		}
	}
	if cleaned := path.Clean(dst); cleaned != dst {
		return fmt.Errorf("base 路径不规范: %q（Clean 后为 %q）", dst, cleaned)
	}
	return nil
}
