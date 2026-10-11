package renderstore

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/xbcio/xinit/internal/state"
)

// Write 把一轮渲染的原文快照写到 <cacheRoot>/render/<familyKey>/（§21.3–21.5）。
// 前置契约：st 必须是本轮 st.Save **成功之后**的持久化状态——键的 vars 分量
// = st.Vars（停盘值），不得用本轮传入的 vars 参数（§21.3 读写同源）。
// rendered = 当轮渲染源（Dst → 原文）；清单键集 = st.Files 的键 ∩ rendered。
// 条目存在且与现算键 + anchor 全匹配（含 tree/ 存在性/条目数核对）→ 不重写；
// 否则整条覆写轮转（先 tree、后 manifest、再清旧）。任何失败返回 error
// （调用方 best-effort 记警告，不影响命令退出码）。
func Write(st *state.State, cacheRoot string, rendered map[string][]byte) error {
	// 清单键集 = st.Files 的键 ∩ 渲染源（§21.4）：st.Files 有而渲染源无 →
	// 静默略过（宁缺勿错）；坏的 Dst（绝对路径 / .. 段）不得写出条目目录——
	// 写路径和读路径一样过 CheckWithinBase（base.go:31 同门判据，§21.9）。
	dsts := make([]string, 0, len(st.Files))
	for dst := range st.Files {
		if _, ok := rendered[dst]; !ok {
			continue
		}
		if err := state.CheckWithinBase(dst); err != nil {
			continue
		}
		dsts = append(dsts, dst)
	}
	if len(dsts) == 0 {
		return nil // 空交集：不创建条目（连缓存根都不建）
	}
	sort.Strings(dsts)

	key := familyKey(st.EngineVersion, st.Template.Source, st.Template.Kind, st.Vars)
	dir := entryDir(cacheRoot, key)
	varsSHA := varsHash(st.Vars)

	if entryMatches(dir, st, varsSHA, dsts) {
		return nil // 全匹配：不重写（字节与 mtime 保持）
	}

	// 条目缺失/损坏 → 整条覆写轮转。缓存根创建沿用 mirror.go 的范式。
	if err := os.MkdirAll(cacheRoot, 0o755); err != nil {
		return fmt.Errorf("创建缓存目录: %w", err)
	}

	// 写序（§21.5）：tree/ 逐文件原子写（0644）→ manifest.json 最后原子写
	// （有效令牌）→ best-effort 清理旧文件。不加锁：tree 后 manifest 的
	// 写序 + 读侧校验链使并发读者最多 miss（安全回退）。
	treeDir := filepath.Join(dir, treeDirName)
	for _, dst := range dsts {
		full := filepath.Join(treeDir, filepath.FromSlash(dst))
		if err := state.WriteFileAtomic(full, rendered[dst], 0o644); err != nil {
			return fmt.Errorf("写快照 tree/%s: %w", dst, err)
		}
	}

	m := &manifest{
		Version:    manifestVersion,
		Engine:     st.EngineVersion,
		Source:     st.Template.Source,
		Kind:       st.Template.Kind,
		Anchor:     st.Template.Commit,
		VarsSHA256: varsSHA,
		Files:      make([]fileEntry, 0, len(dsts)),
	}
	for _, dst := range dsts {
		raw := rendered[dst]
		m.Files = append(m.Files, fileEntry{
			Dst:       dst,
			RawSHA256: sha256Hex(raw),
			Size:      len(raw),
		})
	}
	data, err := m.encode()
	if err != nil {
		return err
	}
	if err := state.WriteFileAtomic(filepath.Join(dir, manifestName), data, 0o644); err != nil {
		return fmt.Errorf("写快照清单: %w", err)
	}

	keep := make(map[string]bool, len(dsts))
	for _, dst := range dsts {
		keep[dst] = true
	}
	cleanupTree(treeDir, keep)
	return nil
}

// entryMatches 报告现有条目是否与现算键 + anchor 全匹配（§21.5 跳过判据，
// 全合取）。逐 dst 只核对存在且为普通文件——内容正确性由读侧 raw 门兜底，
// 写侧不必全量重读；tree/ 普通文件数核对把多余/丢失/被换成目录的条目
// 判为损坏，触发整条重写。
func entryMatches(dir string, st *state.State, varsSHA string, dsts []string) bool {
	m, err := readManifest(filepath.Join(dir, manifestName))
	if err != nil {
		return false
	}
	if m.Version != manifestVersion ||
		m.Engine != st.EngineVersion ||
		m.Source != st.Template.Source ||
		m.Kind != st.Template.Kind ||
		m.Anchor != st.Template.Commit ||
		m.VarsSHA256 != varsSHA ||
		len(m.Files) != len(dsts) {
		return false
	}
	listed := make(map[string]bool, len(m.Files))
	for _, f := range m.Files {
		if listed[f.Dst] {
			return false // 重复 dst：异常清单，整条重写
		}
		listed[f.Dst] = true
	}
	treeDir := filepath.Join(dir, treeDirName)
	for _, dst := range dsts {
		if !listed[dst] {
			return false
		}
		info, err := os.Lstat(filepath.Join(treeDir, filepath.FromSlash(dst)))
		if err != nil || !info.Mode().IsRegular() {
			return false
		}
	}
	return countRegularFiles(treeDir) == len(m.Files)
}

// cleanupTree best-effort 删除 tree/ 下不在 keep 里的旧条目（R9：删除失败
// 一律忽略——Windows 占用、被非空目录替换等；残留空目录对读侧无害，
// 跳过判据只数普通文件）。
func cleanupTree(treeDir string, keep map[string]bool) {
	_ = filepath.WalkDir(treeDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == treeDir {
			return nil // best-effort：遍历错误也吞掉
		}
		rel, err := filepath.Rel(treeDir, p)
		if err != nil {
			return nil
		}
		if !keep[filepath.ToSlash(rel)] {
			_ = os.Remove(p) // 非空目录会失败——忽略
		}
		return nil
	})
}

// countRegularFiles 数 root 下的普通文件（目录/符号链接等不计）。
func countRegularFiles(root string) int {
	n := 0
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() {
			n++
		}
		return nil
	})
	return n
}
