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
	"github.com/xbcio/xinit/internal/renderstore"
	"github.com/xbcio/xinit/internal/state"
	"github.com/xbcio/xinit/internal/template"
)

// baseSnapshot 是 .xinit/base/ 的装载结果（§5.2）。
type baseSnapshot struct {
	Metas    map[string]plan.FileMeta // Dst → 元数据（Plan 的 base 参数）
	Bytes    map[string][]byte        // Dst → base 原始字节（merge 三方输入）
	Degraded []string                 // 无法重建、本轮按 base✗ 处理的路径（排序）
	Restored []string                 // §21.6-4：从渲染快照恢复的 Dst（排序 = need 序 = 字节序）
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
// 恢复链前置一级（§21.6）：先试渲染快照（renderstore.Fetch，只读；缓存
// 不存在/不可读/任何校验不符 → 静默 miss）——命中的写回**复用既有通道**
// （原子写、mode 由 fst.Exec 推导），余项才走既有链。混合态语义：余项
// 为空即完成（不打开模板源）；git 源余项走重渲染；dir 源零命中保持现行
// 硬错、有命中则余项记 Degraded 不再硬错。
//
// readOnly 时跳过唯一的落盘点（写回 .xinit/base/）：重建内容仍进 snap，
// 磁盘保持原样（dry-run/diff 零写契约，评审 I-1）。
func restoreBase(projectDir string, st *state.State, cacheRoot string, need []string, snap *baseSnapshot, readOnly bool) error {
	const adoptHint = "；请运行 xinit adopt --template <source> --revision <ref> --config <vars.yml> 重建基线（§5.2）"

	// §21.6 前置一级：快照命中即恢复（写回字节过同一记录 hash 门，§20.4
	// 守卫 7 不受影响）；miss 的路径留在 remaining 走既有链。
	restored := renderstore.Fetch(st, cacheRoot, need)
	base := state.BaseDir(projectDir)
	var remaining []string
	for _, dst := range need {
		content, ok := restored[dst]
		if !ok {
			remaining = append(remaining, dst)
			continue
		}
		fst := st.Files[dst]
		mode := fs.FileMode(0o644)
		if fst.Exec {
			mode = 0o755
		}
		if !readOnly {
			if err := state.WriteFileAtomic(filepath.Join(base, filepath.FromSlash(dst)), content, mode); err != nil {
				return fmt.Errorf("从渲染快照恢复 base/%s: %w%s", dst, err, adoptHint)
			}
		}
		snap.Metas[dst] = metaFromFileState(fst, content)
		snap.Bytes[dst] = content
		snap.Restored = append(snap.Restored, dst)
	}
	if len(remaining) == 0 {
		return nil // 全命中：不打开模板源（§21.7）
	}
	note := snapshotNote(snap.Restored)

	if st.Template.Kind != "git" {
		if len(snap.Restored) == 0 {
			// 零命中：保持现行硬错文案逐字（判据 5）。
			return fmt.Errorf("base 缺失或损坏，而模板源是 %s 类型——没有历史版本可供重渲染%s",
				st.Template.Kind, adoptHint)
		}
		// 部分恢复（§21.6-3 dir 混合态）：余项按 §5.2 既有降级语义处理，
		// 不再硬错（dir 与 git 在混合态汇合）。
		snap.Degraded = append(snap.Degraded, remaining...)
		sort.Strings(snap.Degraded)
		return nil
	}
	old, err := template.Open(st.Template.Source, st.Template.Commit, cacheRoot, false)
	if err != nil {
		return fmt.Errorf("重建 base：模板 %s@%s 已不可取到（commit 被删或强推？）：%v%s%s",
			st.Template.Source, st.Template.Commit, err, adoptHint, note)
	}
	mdata, err := fs.ReadFile(old.FS, "xinit.yml")
	if err != nil {
		return fmt.Errorf("重建 base：旧模板缺少 xinit.yml：%v%s%s", err, adoptHint, note)
	}
	m, err := manifest.Parse(mdata)
	if err != nil {
		return fmt.Errorf("重建 base：旧清单在本版 schema 下解析失败：%v%s%s", err, adoptHint, note)
	}
	c, err := m.Compile()
	if err != nil {
		return fmt.Errorf("重建 base：旧清单编译失败：%v%s%s", err, adoptHint, note)
	}

	// R9：vars 用 state.Vars 原样——不重跑 EvalVars（被删除的变量还在
	// state.vars 里，重跑会报「清单未声明的变量」）。
	// 告警抑制：重渲染只为取证，告警上一轮已报告过。
	entries, _, err := render.Paths(old.FS, c, st.Vars)
	if err != nil {
		return fmt.Errorf("重建 base：重渲染路径失败：%w%s%s", err, adoptHint, note)
	}
	byDst := make(map[string]render.Entry, len(entries))
	for _, e := range entries {
		byDst[e.Dst] = e
	}

	for _, dst := range remaining {
		fst := st.Files[dst]
		e, ok := byDst[dst]
		if !ok {
			snap.Degraded = append(snap.Degraded, dst) // 旧模板也产不出该 Dst
			continue
		}
		content, _, _, err := render.Content(old.FS, c, st.Vars, e)
		if err != nil {
			return fmt.Errorf("重建 base/%s: %w%s%s", dst, err, adoptHint, note)
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
				return fmt.Errorf("重建 base/%s: %w%s%s", dst, err, adoptHint, note)
			}
		}
		snap.Metas[dst] = metaFromFileState(fst, content)
		snap.Bytes[dst] = content
	}
	sort.Strings(snap.Degraded)
	return nil
}

// snapshotNote 是硬错误尾附的恢复行（判据 10）：有已恢复路径时 = "\n" +
// 恢复行；无则空串（错误文本逐字不变）。文案与报告行共用 restoredLineFmt
// （单一来源，report.go）。
func snapshotNote(restored []string) string {
	if len(restored) == 0 {
		return ""
	}
	return "\n" + fmt.Sprintf(restoredLineFmt, len(restored))
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
