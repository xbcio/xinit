package backflow

import (
	"errors"
	"fmt"
	"io/fs"
	"sort"

	"github.com/xbcio/xinit/internal/fsutil"
	"github.com/xbcio/xinit/internal/manifest"
	"github.com/xbcio/xinit/internal/render"
	"github.com/xbcio/xinit/internal/state"
	"github.com/xbcio/xinit/internal/template"
)

// OpenTemplate 打开记录 commit 的模板源，并落 §20.7 表末两行的「模板源资格」：
//
//   - dir 源 → 拒绝：dir 源没有 git blob 对象可作补丁 pre-image，且目录可被
//     随时编辑、无版本锚（state 里 dir 源的 Commit 是树 hash）；
//   - git 源但记录 commit 不可解析（极旧 state / 本地镜像缺失）→ 拒绝：
//     无 T 则无翻译基线（错误语义同 template.ErrMirrorMissing 系）。
//
// 只读、不联网（template.Open 的 fetch=false，与 restore.go 取旧模板同口径），
// 返回的 Source 供全链复用（FS 重放与 blob 读取的同一坐标系）。
func OpenTemplate(st *state.State, cacheRoot string) (*template.Source, error) {
	if st.Template.Kind != "git" {
		return nil, fmt.Errorf(
			"回流要求模板源是 git 源：记录状态里的模板源类型是 %q（§20.7）——dir 源没有 git blob 对象可作补丁 pre-image，"+
				"且目录可被随时编辑、无版本锚；请改用 git 模板源后重跑",
			st.Template.Kind)
	}
	old, err := template.Open(st.Template.Source, st.Template.Commit, cacheRoot, false)
	if err != nil {
		return nil, fmt.Errorf(
			"记录 commit 的模板不可取到（%s@%s）：%v；§20.7：无 T 则无翻译基线——"+
				"请确认该 commit 仍在模板仓历史中（极旧 state 或本地镜像缺失都会走到这里）",
			st.Template.Source, st.Template.Commit, err)
	}
	return old, nil
}

// ConvergeResult 是守卫 7 的判定结果。
type ConvergeResult struct {
	Unconverged []string // 成因①：hash 不符（含 base 落盘字节加固不符）
	SourceGone  []string // 成因②：byDst 缺失
}

// CheckConverged 实现守卫 7（只读；不触碰 .xinit/base 的写回路径）。
//
// 只作用于受管路径：不在 state.Files 的键被跳过 —— 新增文件豁免（无 B 则无
// 同源问题；其路径解析以记录 commit 的清单为坐标系）。srcs 的键 = 待检的
// 受管候选 Dst 集；值 = 该 Dst 的有效 Src（供调用方复用），本判据按 Dst 取
// Entry —— 收敛性锚在「记录 commit 的重渲染产物」上，映射覆盖不改变判据。
//
// 对每个受管候选 Dst：按记录 commit 重渲染（template.Open +
// manifest.Parse/Compile + render.Paths，vars 用 state.Vars 原样、不重跑
// EvalVars——同 restore.go 的重建判据，只读重实现），取 byDst 中的 Entry 再
// render.Content，逐路径比对 fsutil.CompareHash(content, fst.Binary) ==
// fst.Hash（同 restore.go:146-160）：
//
//   - byDst 缺失 → 成因②：模板已移除/改名（KeepWarned 类），update 永远收敛
//     不了；调用方将该路径警告不导出（出口 `--map` / 人工判断），不改整体判定。
//   - hash 不符 → 成因①：base 与记录 commit 不同源（上一轮 pending 推进过
//     base 而不推进 Template.Commit）。调用方**整体拒绝**（fail-closed），文案
//     「base 与记录 commit 不同源（上一轮 pending 推进过 base）」+ 列出路径 +
//     出口「先跑 `xinit update` 收敛」。
//
// 可选加固照做：`.xinit/base/<Dst>` 落盘字节的 fsutil.ContentHash 必须等于
// st.Files[dst].Hash（防手工篡改 base 让 B 失真）；不符或落盘缺失（update 会
// 重建它）都归成因① 同一出口。
//
// 两个列表都按 Dst 字节序排序（确定性输出）；无重复。
func CheckConverged(projectDir string, st *state.State, cacheRoot string, srcs map[string]string) (ConvergeResult, error) {
	var res ConvergeResult

	old, err := OpenTemplate(st, cacheRoot)
	if err != nil {
		return res, err
	}
	mdata, err := fs.ReadFile(old.FS, "xinit.yml")
	if err != nil {
		// §20.5 句 3：清单来源 = 记录 commit 的清单；取不到即报错，不静默变 vacuous。
		return res, fmt.Errorf("记录 commit 的模板缺少 xinit.yml：%w", err)
	}
	m, err := manifest.Parse(mdata)
	if err != nil {
		return res, fmt.Errorf("记录 commit 的清单解析失败：%w", err)
	}
	c, err := m.Compile()
	if err != nil {
		return res, fmt.Errorf("记录 commit 的清单编译失败：%w", err)
	}
	entries, _, err := render.Paths(old.FS, c, st.Vars)
	if err != nil {
		return res, fmt.Errorf("按记录 commit 重渲染路径失败：%w", err)
	}
	byDst := make(map[string]render.Entry, len(entries))
	for _, e := range entries {
		byDst[e.Dst] = e
	}

	for _, dst := range sortedMapKeys(srcs) {
		fst, ok := st.Files[dst]
		if !ok {
			continue // 新增文件豁免（无 B 则无同源问题）
		}
		e, ok := byDst[dst]
		if !ok {
			res.SourceGone = append(res.SourceGone, dst) // 成因②
			continue
		}
		content, _, _, err := render.Content(old.FS, c, st.Vars, e)
		if err != nil {
			return res, fmt.Errorf("重渲染 %s 失败：%w", dst, err)
		}
		if fsutil.CompareHash(content, fst.Binary) != fst.Hash {
			res.Unconverged = append(res.Unconverged, dst) // 成因①
			continue
		}
		// 加固：base 落盘字节也必须与 state 记录一致。
		disk, readErr := state.ReadBase(projectDir, dst)
		switch {
		case readErr == nil:
			if fsutil.ContentHash(disk) != fst.Hash {
				res.Unconverged = append(res.Unconverged, dst)
			}
		case errors.Is(readErr, fs.ErrNotExist):
			res.Unconverged = append(res.Unconverged, dst) // update 会重建，同出口
		default:
			return res, fmt.Errorf("读取 base/%s 失败：%w", dst, readErr)
		}
	}
	return res, nil
}

// sortedMapKeys 返回 map 键的字节序切片（确定性输出）。
func sortedMapKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
