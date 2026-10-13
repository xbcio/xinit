package update

// 来源：internal/update/update.go（M2-P1 抽取：Run 块 7-9 → loadTemplate；
// 4 个 helper 整函数搬家；I-2 实施批 T4/M2a）。阶段判据（§3-M2）：函数体与
// 源区间逐字对应，差异仅「参数接入」与「返回值包装」两类（逐条见
// task-4-report.md）。
// M5：renderTheirs 导出为 RenderTheirs——init 渲染块改走本单点（共用渲染）。

import (
	"fmt"
	"io"
	"io/fs"

	"github.com/xbcio/xinit/internal/fsutil"
	"github.com/xbcio/xinit/internal/manifest"
	"github.com/xbcio/xinit/internal/plan"
	"github.com/xbcio/xinit/internal/render"
	"github.com/xbcio/xinit/internal/state"
	"github.com/xbcio/xinit/internal/template"
)

// Template 是模板侧本轮的全部事实（P1 写，P2-P11 只读；§2.3）。Commit 是
// src 的最终形态（块 8 的 dir 分支就地改写后的值，I28）。
type Template struct {
	Src      *template.Source
	Manifest *manifest.Manifest // hooks 块挂在它上面（Compiled 无 Hooks 字段）
	Compiled *manifest.Compiled
	Vars     render.Vars
	Entries  []render.Entry
	Rendered map[string][]byte // Dst → 渲染字节；空渲染 = 非 nil 空切片（I20）
	SrcOf    map[string]string // Dst → 模板原路径（§17.1 Part A 用）
	Snap     plan.Snap         // theirs 快照（含 Generated 位）
	Commit   string            // git: sha / dir: 树 hash（I28）
}

// loadTemplate 是 P1（§2.2）：打开模板并选定目标版本（块 7）、读清单与解析
// vars（块 8）、渲染 theirs（块 9）。rep.NewRevision/NewCommit 依 R-M2-3 留守
// Run（值经 t.Src）。
func loadTemplate(s *session, revision, configPath string, fetch bool) (*Template, error) {
	// 2. 打开模板 + 选定目标版本（§11.3 / R8）
	src, err := openTarget(s.st, revision, s.cacheRoot, fetch, s.io.Err)
	if err != nil {
		return nil, err
	}

	// 3. 清单 + vars（R15）
	m, compiled, vars, err := loadManifestVars(src, s.st, configPath)
	if err != nil {
		return nil, err
	}
	// dir 源的「版本」= 树 hash（与 init 同一算法：清单编译后、排除 exclude 命中）
	if src.Kind == template.KindDir {
		if src.Commit, err = template.TreeHashOf(src.DirRoot, compiled.Exclude); err != nil {
			return nil, err
		}
	}

	// 4. 渲染 theirs（模板侧；告警与 init 同格式）
	entries, rendered, srcOf, err := RenderTheirs(src, compiled, vars, s.io.Err)
	if err != nil {
		return nil, err
	}
	return &Template{
		Src:      src,
		Manifest: m,
		Compiled: compiled,
		Vars:     vars,
		Entries:  entries,
		Rendered: rendered,
		SrcOf:    srcOf,
		Snap:     snapFromRendered(entries, rendered),
		Commit:   src.Commit,
	}, nil
}

// openTarget 打开模板源并选定目标版本（§11.3、R8）。errW = 降级警告通道
// （R-M4-3：末位形参，不引入 session 依赖；调用点传 s.io.Err）。
func openTarget(st *state.State, rev, cacheRoot string, fetch bool, errW io.Writer) (*template.Source, error) {
	target := rev
	switch {
	case rev != "":
		// 显式指定：允许任意 ref、允许降级（下面给后代警告）
	case st.Template.Kind == template.KindDir.String():
		// dir 源没有版本概念：原样重开（Open 忽略 revision）
	default:
		sel, err := template.SelectRevision(st.Template.Source, cacheRoot, fetch, st.Template.Revision)
		if err != nil {
			return nil, err
		}
		target = sel
	}

	// allowClone=true：镜像优先，缺失回退首次 clone（update 的功能性回退；
	// 取已记录版本时模板可能尚未镜像）。
	src, err := template.Open(st.Template.Source, target, cacheRoot, fetch, true)
	if err != nil {
		return nil, err
	}

	// 显式 --revision 的降级警告（§11.3）：目标不是当前 revision 的后代时
	// 警告但不阻止——模板发了坏版本时用户需要能回退。回退不是「模板改动不出
	// 现」那么轻：磁盘上已有的内容会被改写或删除、回退到目标版本的旧内容
	//（未被本地改过的路径走行 2 覆盖／行 8 删除，原内容在 .xinit/backup/
	// 保留），本地改动走合并/冲突、本地删除保留删除（行 6/7/10/11，不会被静默回退）。发布批次
	// O2/场景 7.5 实测：模板 HEAD 领先 tag 时 init 带入的未发布改动正是这样
	// 被写回 tag 内容的。
	if rev != "" && st.Template.Revision != "" && rev != st.Template.Revision {
		anc, err := template.IsAncestor(st.Template.Source, cacheRoot, fetch, st.Template.Revision, rev)
		switch {
		case err != nil:
			fmt.Fprintf(errW, "xinit: 警告: 无法判断 %s 是否为 %s 的后代: %v\n", rev, st.Template.Revision, err)
		case !anc:
			fmt.Fprintf(errW,
				"xinit: 警告: 目标版本 %s 不是当前版本 %s 的后代（降级/回退）：磁盘上已有的内容会被改写或删除、回退到 %s 的版本（原内容在 .xinit/backup/ 保留；本地改动走合并/冲突或保留删除、均不会被静默回退）；两版之间模板的改动不会出现在本项目里，之后可能被误判为本地改动（§11.3）\n",
				rev, st.Template.Revision, rev)
		}
	}
	return src, nil
}

// loadManifestVars 读模板清单并解析本轮 vars（R15：--config > 冻结值 > default）。
// 同时返回 Manifest——hooks 块挂在它上面（Compiled 无 Hooks 字段）。
func loadManifestVars(src *template.Source, st *state.State, configPath string) (*manifest.Manifest, *manifest.Compiled, render.Vars, error) {
	manifestData, err := fs.ReadFile(src.FS, "xinit.yml")
	if err != nil {
		return nil, nil, nil, fmt.Errorf("模板缺少 xinit.yml: %w", err)
	}
	m, err := manifest.Parse(manifestData)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("解析模板的 xinit.yml: %w", err)
	}
	compiled, err := m.Compile()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("编译模板的 xinit.yml: %w", err)
	}
	var config map[string]string
	if configPath != "" {
		if config, err = manifest.LoadVarsFile(configPath); err != nil {
			return nil, nil, nil, err
		}
	}
	vars, err := ResolveUpdateVars(compiled, st.Vars, config)
	if err != nil {
		return nil, nil, nil, err
	}
	return m, compiled, vars, nil
}

// RenderTheirs 是模板侧渲染+告警的单点（§7；init 与 update 共用，I-2 M5）：
// 路径渲染警告逐条；替换规则零命中按**规则**汇总（§7.4）。errW = 警告通道
// （R-M4-3：末位形参；update 传 s.io.Err，init 传 os.Stderr）。
func RenderTheirs(src *template.Source, compiled *manifest.Compiled, vars render.Vars, errW io.Writer,
) ([]render.Entry, map[string][]byte, map[string]string, error) {
	entries, warnings, err := render.Paths(src.FS, compiled, vars)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("渲染模板路径: %w", err)
	}
	for _, w := range warnings {
		fmt.Fprintf(errW, "xinit: 警告: %s: %s\n", w.Path, w.Msg)
	}

	rendered := make(map[string][]byte, len(entries))
	srcOf := make(map[string]string, len(entries))
	hitTotals := make(map[int]int)
	for _, e := range entries {
		data, hits, warns, err := render.Content(src.FS, compiled, vars, e)
		if err != nil {
			return nil, nil, nil, err
		}
		for _, h := range hits {
			hitTotals[h.Index] += h.Count
		}
		for _, w := range warns {
			fmt.Fprintf(errW, "xinit: 警告: %s: %s\n", w.Path, w.Msg)
		}
		// 空渲染（Go template 渲染出空内容）是**空内容文件**，不是「模板没有它」：
		// 归一成非 nil 空切片，使下游按「键存在」判定的全链（classifyMerges 的
		// b != nil / tb == nil 守卫、conflicts/ 内容条目、--accept、答 n 后不再重问）
		// 与 buildChanges:791 / advanceState:1102 的键存在语义一致（2026-10-11 M7）。
		if data == nil {
			data = []byte{}
		}
		rendered[e.Dst] = data
		srcOf[e.Dst] = e.Src
	}
	for i, cs := range compiled.Subst {
		if hitTotals[i] == 0 {
			fmt.Fprintf(errW,
				"xinit: 警告: 替换规则 %d (%q) 在渲染结果中命中 0 次 —— 模板里可能已无此字面量\n",
				i, cs.Spec.From)
		}
	}
	return entries, rendered, srcOf, nil
}

// snapFromRendered 把模板侧的渲染结果变成 plan 快照——对派生侧用
// worktree.Scan，两边同一套探测与 hash（fsutil.ContentHash/IsBinary/DetectEOL），
// 同一份内容在两侧得到同一个 hash。
func snapFromRendered(entries []render.Entry, rendered map[string][]byte) plan.Snap {
	snap := make(plan.Snap, len(entries))
	for _, e := range entries {
		content := rendered[e.Dst]
		snap[e.Dst] = plan.FileMeta{
			Kind:      e.Kind,
			Exec:      e.Exec,
			Binary:    e.Binary,
			MixedEOL:  fsutil.DetectEOL(content) == fsutil.EOLMixed,
			Generated: e.Generated,
			Hash:      fsutil.ContentHash(content),
		}
	}
	return snap
}
