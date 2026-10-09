package apply

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/xbcio/xinit/internal/state"
	"github.com/xbcio/xinit/internal/worktree"
)

// Changes 是一次 update 的全部落盘动作（§10.2 第 8 步）。
// 键都是 Dst（slash、相对项目根）；Conflicts 例外：键是 P，
// 落盘为 .xinit/conflicts/<P>（§10.4）。
type Changes struct {
	Writes  map[string][]byte // Dst → 完整最终内容（EOL 还原已完成）
	Deletes []string          // 先删后写里的删除（Dst）
	// ExecBits：Dst → 目标 exec。三种语义（§8.3 的落地，由调用方判定）：
	//   在 Writes 里给出   → 写内容时把 exec 对齐到该值；
	//   不在 Writes 里给出 → 纯 exec 变更（R7），只调 exec 位；
	//   未给出             → 连 exec 一起保持磁盘原样（settle 第二分支「不动」）。
	ExecBits  map[string]bool
	Conflicts map[string][]byte // P → 冲突的 theirs 内容（重建式）
}

// Result 报告实际发生的动作，供 §10.2 第 12 步的清单使用。
type Result struct {
	Written   []string // 写入（含 mode 调整）的工作树路径，排序
	Deleted   []string
	Chmodded  []string // 只调整了 exec 位的路径（不写内容）
	Conflicts []string // 本轮写出的 conflicts/ 条目（P）
	Resolved  []string // 本轮删除的 conflicts/ 条目（P）
	Refusals  []Refusal
}

// Execute 落盘。顺序固定（§10.2 第 8 步）：
// 删除的 §9.5 安全 → 写前检查（§9.5/障碍物/折叠）→ 先删后写 →
// 纯 exec 变更（R7）→ conflicts/ 重建（§10.4）。
// 折叠拒绝与障碍物路径跳过、不中止其余路径（§9.3）；
// IO 错误返回 error，已写内容保留（§10.2：没有回滚，备份可恢复）。
func Execute(projectDir string, c Changes) (Result, error) {
	var res Result
	umask := currentUmask()

	// 1. 删除路径先过 §9.5；只有真会删掉的删除才能让折叠检查放行——
	//    否则「放行」会把写入打到仍然存在的旧条目上（折叠文件系统上丢数据）。
	deletes := append([]string(nil), c.Deletes...)
	sort.Strings(deletes)
	liveDeletes := make([]string, 0, len(deletes))
	for _, rel := range deletes {
		ok, err := worktree.WithinResolvedRoot(projectDir, rel)
		if err != nil {
			return Result{}, err
		}
		if !ok {
			res.Refusals = append(res.Refusals, Refusal{Path: rel, Reason: "删除路径解析后落在项目根之外（父路径有指向外部的符号链接）；请移走它，或把该路径写进 .xinit/prune.txt（§9.5）"})
			continue
		}
		liveDeletes = append(liveDeletes, rel)
	}
	deleteSet := make(map[string]bool, len(liveDeletes))
	for _, rel := range liveDeletes {
		deleteSet[rel] = true
	}

	// 2. 写前检查（§9.3）
	writes := sortedKeys(c.Writes)
	refusals, err := CheckWrites(projectDir, writes, deleteSet)
	if err != nil {
		return Result{}, err
	}
	res.Refusals = append(res.Refusals, refusals...)
	refused := make(map[string]bool, len(refusals))
	for _, r := range refusals {
		refused[r.Path] = true
	}
	sort.Slice(res.Refusals, func(i, j int) bool { return res.Refusals[i].Path < res.Refusals[j].Path })

	// 3. 先删后写
	for _, rel := range liveDeletes {
		full := filepath.Join(projectDir, filepath.FromSlash(rel))
		if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
			return Result{}, fmt.Errorf("删除 %s: %w", rel, err)
		}
		res.Deleted = append(res.Deleted, rel)
	}

	// 4. 写内容；被拒路径跳过、保持旧 base（§8.5）
	for _, rel := range writes {
		if refused[rel] {
			continue
		}
		wantExec, hasExec := c.ExecBits[rel]
		perm, err := writeMode(projectDir, rel, wantExec, hasExec, umask)
		if err != nil {
			return Result{}, err
		}
		full := filepath.Join(projectDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return Result{}, fmt.Errorf("创建 %s 的父目录: %w", rel, err)
		}
		if err := state.WriteFileAtomic(full, c.Writes[rel], perm); err != nil {
			return Result{}, fmt.Errorf("写入 %s: %w", rel, err)
		}
		res.Written = append(res.Written, rel)
	}

	// 5. 纯 exec 变更：不写内容的 Noop 路径（R17 的落地通道）
	for _, rel := range sortedKeys(c.ExecBits) {
		if _, inWrites := c.Writes[rel]; inWrites || refused[rel] {
			continue
		}
		changed, err := adjustExec(projectDir, rel, c.ExecBits[rel])
		if err != nil {
			return Result{}, err
		}
		if changed {
			res.Chmodded = append(res.Chmodded, rel)
		}
	}

	// 6. conflicts/ 重建（§10.4）
	res.Conflicts, res.Resolved, err = rebuildConflicts(projectDir, c.Conflicts)
	if err != nil {
		return Result{}, err
	}
	return res, nil
}

// writeMode 计算写入用的 mode（§10.1）：
// 已有文件 = 磁盘当前 perm；hasExec 给出本轮 §8.3 的 exec 结论时按它调整 exec 位，
// 否则连 exec 一起保持（settle 第二分支「不动」的落地——用户改过权限）；
// 新文件 = 0666&^umask（结论是 exec 时 0777&^umask）。
// WriteFileAtomic 内部是 Chmod，不受 umask 影响——umask 必须在这里显式应用。
func writeMode(projectDir, rel string, wantExec, hasExec bool, umask uint32) (os.FileMode, error) {
	full := filepath.Join(projectDir, filepath.FromSlash(rel))
	fi, err := os.Lstat(full)
	switch {
	case err == nil && fi.Mode().IsRegular():
		perm := fi.Mode().Perm()
		if !hasExec {
			return perm, nil // §8.3 没给结论 → 一个位都不动
		}
		if wantExec {
			perm |= 0o111
		} else {
			perm &^= 0o111
		}
		return perm, nil
	case err == nil:
		return 0, fmt.Errorf("写入 %s: 路径在检查之后被占用（非普通文件）", rel)
	case os.IsNotExist(err):
		base := os.FileMode(0o666)
		if hasExec && wantExec {
			base = 0o777
		}
		return base &^ os.FileMode(umask), nil
	default:
		return 0, fmt.Errorf("写入 %s: %w", rel, err)
	}
}

// adjustExec 只调整 exec 位、保持其余权限位（R7），返回是否发生了改动。
// 磁盘上已不存在或已非普通文件 → 跳过（下一轮按新磁盘状态重新判定）。
func adjustExec(projectDir, rel string, wantExec bool) (bool, error) {
	full := filepath.Join(projectDir, filepath.FromSlash(rel))
	fi, err := os.Lstat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("调整 exec %s: %w", rel, err)
	}
	if !fi.Mode().IsRegular() {
		return false, nil
	}
	perm := fi.Mode().Perm()
	if (perm&0o111 != 0) == wantExec {
		return false, nil
	}
	if wantExec {
		perm |= 0o111
	} else {
		perm &^= 0o111
	}
	if err := os.Chmod(full, perm); err != nil {
		return false, fmt.Errorf("调整 exec %s: %w", rel, err)
	}
	return true, nil
}

// rebuildConflicts 重建 .xinit/conflicts/（§10.4）：本轮集合外的条目删除（Resolved）、
// 集合内重写（0644）；清空后连目录一起移除——「存在即未解决冲突」。
func rebuildConflicts(projectDir string, conflicts map[string][]byte) (written, resolved []string, err error) {
	root := filepath.Join(projectDir, state.DirName, "conflicts")

	have := map[string]bool{}
	if _, statErr := os.Lstat(root); statErr == nil {
		walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, werr error) error {
			if werr != nil {
				return werr
			}
			if d.IsDir() {
				return nil
			}
			rel, relErr := filepath.Rel(root, p)
			if relErr != nil {
				return relErr
			}
			have[filepath.ToSlash(rel)] = true
			return nil
		})
		if walkErr != nil {
			return nil, nil, walkErr
		}
	} else if !os.IsNotExist(statErr) {
		return nil, nil, statErr
	}

	for rel := range have {
		if _, keep := conflicts[rel]; keep {
			continue
		}
		if err := os.Remove(filepath.Join(root, filepath.FromSlash(rel))); err != nil && !os.IsNotExist(err) {
			return nil, nil, fmt.Errorf("清理冲突条目 %s: %w", rel, err)
		}
		resolved = append(resolved, rel)
	}
	sort.Strings(resolved)

	for _, rel := range sortedKeys(conflicts) {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return nil, nil, fmt.Errorf("创建冲突目录: %w", err)
		}
		if err := state.WriteFileAtomic(full, conflicts[rel], 0o644); err != nil {
			return nil, nil, fmt.Errorf("写冲突条目 %s: %w", rel, err)
		}
		written = append(written, rel)
	}

	pruneEmptyDirs(root)
	return written, resolved, nil
}

// pruneEmptyDirs 自底向上删除空目录（含 root 本身）；非空目录删除失败是预期行为。
func pruneEmptyDirs(root string) {
	var dirs []string
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			dirs = append(dirs, p)
		}
		return nil
	})
	sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
	for _, d := range dirs {
		os.Remove(d)
	}
}

func sortedKeys[M ~map[string]V, V any](m M) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
