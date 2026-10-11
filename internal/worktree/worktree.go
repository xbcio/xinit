// Package worktree 按精确路径读派生项目的工作树（只读，不跟随符号链接）。
//
// 为什么不用 WalkDir/glob：更新的 ours 侧只对 base ∪ theirs 这些**已知路径**
// 做存在性查询（§8 头部「ours 只对这些路径做存在性查询」）——全树遍历既慢，
// 又会把成片的未跟踪文件卷进来做无用的判定。目录列举（ListNames）只用于
// §9.3 的折叠检查，也只在需要的父目录上做。
package worktree

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/xbcio/xinit/internal/fsutil"
	"github.com/xbcio/xinit/internal/plan"
	"github.com/xbcio/xinit/internal/render"
)

var errNotExist = errors.New("路径不存在")

// ListNames 返回 dir 下的条目名，按名字节序排序（os.ReadDir 保证——折叠
// 检查的输出顺序由此确定）；目录不存在 → nil, nil。
func ListNames(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("列举目录 %s: %w", dir, err)
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names, nil
}

// hasExactName 报告 dir 下是否有字节完全相同的条目名 name。
func hasExactName(dir, name string) (bool, error) {
	names, err := ListNames(dir)
	if err != nil {
		return false, err
	}
	i := sort.SearchStrings(names, name)
	return i < len(names) && names[i] == name, nil
}

// lstatExact 逐段 Lstat 相对路径 rel：任何一段缺失、中间段不是目录
// （含中间段是符号链接——不穿透）、或遇到 ENOTDIR，都归为 errNotExist。
// 传来的 rel 必须已是 §9.5 校验过的 Dst（slash 分隔、无 . / .. 段）。
func lstatExact(root, rel string) (fs.FileInfo, error) {
	parts := strings.Split(rel, "/")
	// 先整体校验再逐段 Lstat：否则前面某段不存在时会在到达非法段之前
	// 就返回 errNotExist，把本该报错的非法路径静默吞成「不存在」。
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return nil, fmt.Errorf("非法路径段 %q（%s）", p, rel)
		}
	}
	cur := root
	for i, p := range parts {
		full := filepath.Join(cur, p)
		fi, err := os.Lstat(full)
		if err != nil {
			if os.IsNotExist(err) || errors.Is(err, syscall.ENOTDIR) {
				return nil, errNotExist
			}
			return nil, fmt.Errorf("lstat %s: %w", full, err)
		}
		// §9.3 路径一：大小写不敏感文件系统上 Lstat 会命中折叠等价名——
		// 存在性必须以父目录精确列举为准（README.md ≠ readme.md）。
		if exact, err := hasExactName(cur, p); err != nil {
			return nil, err
		} else if !exact {
			return nil, errNotExist
		}
		if i == len(parts)-1 {
			return fi, nil
		}
		if !fi.IsDir() {
			return nil, errNotExist
		}
		cur = full
	}
	return nil, errNotExist
}

// Exists 报告 root 下 rel 是否作为条目存在。目录算存在（调用方各自决定
// 它是否可接受）；不穿透符号链接、不跟随中间段软链。
func Exists(root, rel string) (bool, error) {
	if _, err := lstatExact(root, rel); err != nil {
		if errors.Is(err, errNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Scan 按 paths 逐条读 root，产出快照与内容表：
//   - 常规文件 → FileMeta{KindRegular, Exec, Binary, MixedEOL, Hash} + 内容；
//   - 符号链接 → FileMeta{KindSymlink}（不跟随、无内容）；
//   - 不存在 → 不入快照（缺项即「ours✗」）；
//   - 目录及其它类型 → 不入快照：P 位置是目录的障碍物由 apply 期
//     EISDIR 拒绝兜住（§9.3），不在这里编造 Kind。
//
// Generated 位恒为 false：磁盘上看不出「是不是 generated」，该位只来自
// state.files（base 侧）与新模板清单（theirs 侧）。
//
// Exec 判据与 render.Paths 一致：mode&0111 != 0。
func Scan(root string, paths []string) (plan.Snap, map[string][]byte, error) {
	snap := make(plan.Snap, len(paths))
	contents := make(map[string][]byte, len(paths))
	for _, p := range paths {
		fi, err := lstatExact(root, p)
		if errors.Is(err, errNotExist) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		switch {
		case fi.Mode().Type()&fs.ModeSymlink != 0:
			snap[p] = plan.FileMeta{Kind: render.KindSymlink}
		case fi.Mode().IsRegular():
			data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p)))
			if err != nil {
				return nil, nil, fmt.Errorf("读取 %s: %w", p, err)
			}
			snap[p] = plan.FileMeta{
				Kind:     render.KindRegular,
				Exec:     fi.Mode()&0o111 != 0,
				Binary:   fsutil.IsBinary(data),
				MixedEOL: fsutil.DetectEOL(data) == fsutil.EOLMixed,
				Hash:     fsutil.ContentHash(data),
			}
			contents[p] = data
		}
	}
	return snap, contents, nil
}

// WithinResolvedRoot 报告 rel 是否落在 root 之内（§9.5 写入路径安全）：
// 取 rel 最深的**已存在前缀**，EvalSymlinks 实解后断言仍在 root 的实解路径
// 之内；不存在的尾部段没有符号链接可穿透，纯词法即可。
//
// 返回 false = 路径上有指向外部的符号链接，往里写就是写到项目外 → 调用方
// 拒绝该路径。
func WithinResolvedRoot(root, rel string) (bool, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false, fmt.Errorf("解析项目根 %s: %w", root, err)
	}
	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return false, fmt.Errorf("解析项目根 %s: %w", rootAbs, err)
	}
	// 与 lstatExact 同理由：先整体校验段，非法 .. 不因前缀缺失被短路。
	parts := strings.Split(rel, "/")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return false, fmt.Errorf("非法路径段 %q（%s）", p, rel)
		}
		if fsutil.CheckForbiddenChars(p) != nil {
			return false, fmt.Errorf("非法路径段 %q（%s）", p, rel)
		}
	}
	cur, deepest := rootAbs, rootAbs
	for _, p := range parts {
		next := filepath.Join(cur, p)
		if _, err := os.Lstat(next); err != nil {
			break // 从这一段起都不存在，没有可穿透的链接
		}
		cur, deepest = next, next
	}
	real, err := filepath.EvalSymlinks(deepest)
	if err != nil {
		return false, fmt.Errorf("解析路径 %s: %w", deepest, err)
	}
	return real == rootReal || strings.HasPrefix(real, rootReal+string(filepath.Separator)), nil
}
