package apply

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/xbcio/xinit/internal/fsutil"
	"github.com/xbcio/xinit/internal/worktree"
)

// Refusal 是一条被拒绝的写入（跳过、保持旧 base、退出码 2——§9.3）。
type Refusal struct {
	Path   string // Dst（slash）
	Reason string // 含出口指引的人话
}

// CheckWrites 对计划内的写入路径做三类只读检查：§9.5 路径安全、
// §9.3 障碍物（含 R16 的符号链接）与 §9.3 折叠。
//
// deleteSet = 本轮的删除集合（精确 Dst）：「先删后写」会腾出位置，
// 因此折叠条目在删除集合里就算放行（§9.3 原文）。
// 返回的 Refusal 与 writes 同序；IO 错误直接返回 error（引擎中止）。
func CheckWrites(projectDir string, writes []string, deleteSet map[string]bool) ([]Refusal, error) {
	var out []Refusal
	for _, p := range writes {
		r, bad, err := checkOne(projectDir, p, deleteSet)
		if err != nil {
			return nil, err
		}
		if bad {
			out = append(out, r)
		}
	}
	return out, nil
}

func checkOne(projectDir, p string, deleteSet map[string]bool) (Refusal, bool, error) {
	// ① §9.5：解析符号链接后必须仍在项目根内（写到项目外是硬边界）
	//
	// err ≠ nil（如悬空符号链接令 EvalSymlinks 失败）：fail-closed 拒绝该路径，
	// 不升格成整轮中止——§9.3 的语义是「跳过该路径、退出 2、每轮重查」，与
	// T7 对 ENOTDIR 的处置同构（裁定 W；T4 契约：err != nil = 拒绝写入）。
	ok, err := worktree.WithinResolvedRoot(projectDir, p)
	if err != nil {
		return Refusal{Path: p, Reason: fmt.Sprintf(
			"路径无法安全解析（可能含悬空符号链接）：%v；请移走它，或把该路径写进 .xinit/prune.txt（§9.5）", err)}, true, nil
	}
	if !ok {
		return Refusal{Path: p, Reason: "写入路径解析后落在项目根之外（父路径有指向外部的符号链接）；" +
			"请移走它，或把该路径写进 .xinit/prune.txt（§9.5）"}, true, nil
	}

	// ② §9.3 障碍物：逐段 Lstat（不穿透符号链接，与 worktree 的精确列举一致）
	parts := strings.Split(p, "/")
	cur := projectDir
	for i, seg := range parts {
		full := filepath.Join(cur, seg)
		fi, err := os.Lstat(full)
		if err != nil {
			if os.IsNotExist(err) || errors.Is(err, syscall.ENOTDIR) {
				break // 这一段的上级已经不是目录；上面的分支已覆盖，保险起见直接跳出
			}
			return Refusal{}, false, fmt.Errorf("检查写入路径 %s: %w", p, err)
		}
		last := i == len(parts)-1
		switch {
		case fi.Mode().Type()&fs.ModeSymlink != 0:
			if last {
				return Refusal{Path: p, Reason: fmt.Sprintf(
					"%s 是符号链接——xinit 不跟随、不覆盖（R16）；请移走它，或把该路径写进 .xinit/prune.txt", p)}, true, nil
			}
			return Refusal{Path: p, Reason: fmt.Sprintf(
				"父路径 %s 是符号链接——xinit 不穿透（R16，与精确列举的读盘语义一致）；"+
					"请移走它，或把 %s 写进 .xinit/prune.txt", strings.Join(parts[:i+1], "/"), p)}, true, nil
		case !last && !fi.IsDir():
			return Refusal{Path: p, Reason: fmt.Sprintf(
				"父路径 %s 在磁盘上是常规文件（模板要把它变成目录）——文件与目录不能互相顶替；"+
					"移走它后重跑即可收敛；或把 %s 写进 .xinit/prune.txt 使其脱离管理（§9.3）",
				strings.Join(parts[:i+1], "/"), p)}, true, nil
		case last && fi.IsDir():
			return Refusal{Path: p, Reason: fmt.Sprintf(
				"%s 在磁盘上是目录（模板要写文件）；移走或改名该目录后重跑即可收敛；"+
					"或把 %s 写进 .xinit/prune.txt 使其脱离管理（§9.3）", p, p)}, true, nil
		}
		cur = full
	}

	// ③ §9.3 折叠（大小写 + NFC）
	return checkFold(projectDir, p, deleteSet)
}

// checkFold 实现 §9.3 的折叠检查：写 P 之前精确列举其父目录，
// 存在 Q ≠ P 且折叠等价：Q 在删除集合里 → 放行；否则拒绝。
//
// 这条检查只能在 apply/diff 期做——Plan 是纯函数，看不到未跟踪的本地文件；
// 「用户有个未跟踪的 license、模板新增 LICENSE」正是 §9.3 实测的丢数据路径。
func checkFold(projectDir, p string, deleteSet map[string]bool) (Refusal, bool, error) {
	dir, base := path.Split(p) // Dst 是 slash 分隔，内部用 path 包（§9.4）
	names, err := worktree.ListNames(filepath.Join(projectDir, filepath.FromSlash(dir)))
	if err != nil {
		return Refusal{}, false, err
	}
	for _, q := range names {
		if q == base || !fsutil.FoldEqual(q, base) {
			continue
		}
		qPath := dir + q
		if deleteSet[qPath] {
			continue
		}
		return Refusal{Path: p, Reason: fmt.Sprintf(
			"父目录里已有折叠等价的条目 %s（大小写/NFC 不同、在 APFS/Windows 上是同一个文件）——"+
				"原子写会打到它上面；把它改名或移走后重跑，或把 %s 写进 .xinit/prune.txt（§9.3）", qPath, qPath)}, true, nil
	}
	return Refusal{}, false, nil
}
