package cli

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/xbcio/xinit/internal/fsutil"
	"github.com/xbcio/xinit/internal/manifest"
	"github.com/xbcio/xinit/internal/plan"
	"github.com/xbcio/xinit/internal/render"
	"github.com/xbcio/xinit/internal/state"
	"github.com/xbcio/xinit/internal/template"
)

// baseSnapshot 是 .xinit/base/ 的装载结果（§5.2）。
type baseSnapshot struct {
	Metas    map[string]plan.FileMeta // Dst → 元数据（Plan 的 base 参数）
	Bytes    map[string][]byte        // Dst → base 原始字节（merge 三方输入）
	Degraded []string                 // 无法重建、本轮按 base✗ 处理的路径（排序）
}

// loadBase 装载 base 快照。base 缺失、单文件缺失或损坏时走 §5.2 恢复：
// 用 state 记录的旧模板 @commit + 旧 vars 重渲染，逐路径比对 files[P].hash
// ——一致的重建、不一致的降级。
//
// 每轮全量校验（R10）：base 是 update 的正确性根基，不能被缓存或抽查。
//
// readOnly（dry-run/diff）把恢复降级为纯内存操作：重建结果照常进 Metas/
// Bytes 供 Plan 计算、降级照常进 Degraded，但一个字节都不写回 .xinit/base/
// （零写契约，评审 I-1）。
func loadBase(projectDir string, st *state.State, cacheRoot string, readOnly bool) (*baseSnapshot, error) {
	present, err := readBaseTree(state.BaseDir(projectDir))
	if err != nil {
		return nil, err
	}

	snap := &baseSnapshot{Metas: map[string]plan.FileMeta{}, Bytes: map[string][]byte{}}
	var need []string
	for _, dst := range sortedStringKeys(st.Files) {
		fst := st.Files[dst]
		if content, ok := present[dst]; ok && fsutil.CompareHash(content, fst.Binary) == fst.Hash {
			snap.Metas[dst] = metaFromFileState(fst, content)
			snap.Bytes[dst] = content
			continue
		}
		need = append(need, dst) // 缺失或 hash 不符 → §5.2 恢复
	}
	if len(need) > 0 {
		if err := restoreBase(projectDir, st, cacheRoot, need, snap, readOnly); err != nil {
			return nil, err
		}
	}
	return snap, nil
}

// readBaseTree 精确列举 base 目录里的普通文件（不穿透符号链接）并读出全部字节。
func readBaseTree(base string) (map[string][]byte, error) {
	out := map[string][]byte{}
	if _, err := os.Lstat(base); os.IsNotExist(err) {
		return out, nil // 整棵树丢失：逐路径走恢复
	} else if err != nil {
		return nil, err
	}
	err := filepath.WalkDir(base, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if p == base || d.IsDir() || !d.Type().IsRegular() {
			return nil // 非普通文件不是 xinit 写的，忽略
		}
		rel, err := filepath.Rel(base, p)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = data
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("读取 base 树: %w", err)
	}
	return out, nil
}

// metaFromFileState 由 state 记录与 base 内容构造元数据。
// Exec/Generated 取 state 的权威记录；MixedEOL 从 base 内容探测（§9.1）。
func metaFromFileState(fst state.FileState, content []byte) plan.FileMeta {
	return plan.FileMeta{
		Kind:      render.KindRegular,
		Exec:      fst.Exec,
		Binary:    fst.Binary,
		MixedEOL:  fsutil.DetectEOL(content) == fsutil.EOLMixed,
		Generated: fst.Generated,
		Hash:      fst.Hash,
	}
}

// restoreBase 执行 §5.2 的逐路径恢复。硬错误一律指引 adopt（不猜）。
//
// readOnly 时跳过唯一的落盘点（写回 .xinit/base/）：重建内容仍进 snap，
// 磁盘保持原样（dry-run/diff 零写契约，评审 I-1）。
func restoreBase(projectDir string, st *state.State, cacheRoot string, need []string, snap *baseSnapshot, readOnly bool) error {
	const adoptHint = "；请运行 xinit adopt --template <source> --revision <ref> --config <vars.yml> 重建基线（§5.2）"

	if st.Template.Kind != "git" {
		return fmt.Errorf("base 缺失或损坏，而模板源是 %s 类型——没有历史版本可供重渲染%s",
			st.Template.Kind, adoptHint)
	}
	old, err := template.Open(st.Template.Source, st.Template.Commit, cacheRoot, false)
	if err != nil {
		return fmt.Errorf("重建 base：模板 %s@%s 已不可取到（commit 被删或强推？）：%v%s",
			st.Template.Source, st.Template.Commit, err, adoptHint)
	}
	mdata, err := fs.ReadFile(old.FS, "xinit.yml")
	if err != nil {
		return fmt.Errorf("重建 base：旧模板缺少 xinit.yml：%v%s", err, adoptHint)
	}
	m, err := manifest.Parse(mdata)
	if err != nil {
		return fmt.Errorf("重建 base：旧清单在本版 schema 下解析失败：%v%s", err, adoptHint)
	}
	c, err := m.Compile()
	if err != nil {
		return fmt.Errorf("重建 base：旧清单编译失败：%v%s", err, adoptHint)
	}

	// R9：vars 用 state.Vars 原样——不重跑 EvalVars（被删除的变量还在
	// state.vars 里，重跑会报「清单未声明的变量」）。
	// 告警抑制：重渲染只为取证，告警上一轮已报告过。
	entries, _, err := render.Paths(old.FS, c, st.Vars)
	if err != nil {
		return fmt.Errorf("重建 base：重渲染路径失败：%w%s", err, adoptHint)
	}
	byDst := make(map[string]render.Entry, len(entries))
	for _, e := range entries {
		byDst[e.Dst] = e
	}

	base := state.BaseDir(projectDir)
	for _, dst := range need {
		fst := st.Files[dst]
		e, ok := byDst[dst]
		if !ok {
			snap.Degraded = append(snap.Degraded, dst) // 旧模板也产不出该 Dst
			continue
		}
		content, _, _, err := render.Content(old.FS, c, st.Vars, e)
		if err != nil {
			return fmt.Errorf("重建 base/%s: %w%s", dst, err, adoptHint)
		}
		if fsutil.CompareHash(content, fst.Binary) != fst.Hash {
			snap.Degraded = append(snap.Degraded, dst) // 历史「保持旧值」路径，必然不一致
			continue
		}
		mode := fs.FileMode(0o644)
		if fst.Exec {
			mode = 0o755
		}
		full := filepath.Join(base, filepath.FromSlash(dst))
		if !readOnly {
			if err := state.WriteFileAtomic(full, content, mode); err != nil {
				return fmt.Errorf("重建 base/%s: %w%s", dst, err, adoptHint)
			}
		}
		snap.Metas[dst] = metaFromFileState(fst, content)
		snap.Bytes[dst] = content
	}
	sort.Strings(snap.Degraded)
	return nil
}

// sortedStringKeys 返回 map 键的排序切片（确定性输出）。
// 包内通用 helper，Task 12 起继续复用。
func sortedStringKeys[M ~map[string]V, V any](m M) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
