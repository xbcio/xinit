package update

// T7b/M3b C1 断言：accept 域（I1/I3/I16/I31/I32 + T5 O-1）。
// 落点 internal/update（§4.3 规则 1「被测符号在哪，测试在哪」）。

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/xbcio/xinit/internal/fsutil"
	"github.com/xbcio/xinit/internal/manifest"
	"github.com/xbcio/xinit/internal/plan"
	"github.com/xbcio/xinit/internal/render"
	"github.com/xbcio/xinit/internal/state"
	"github.com/xbcio/xinit/internal/template"
)

// acceptEnv 是一次 accept 域测试的最小环境（rewriteAccept/refineDecision 用）。
// 夹具复用 helpers_test.go 的 sessionT（R-M3b-2）。
type acceptEnv struct {
	s *session
	t *Template
	b *Base
	o *Ours
	d *Decision
	a *AcceptRewrite
}

func newAcceptEnv(t *testing.T) *acceptEnv {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, state.DirName), 0o755); err != nil {
		t.Fatal(err)
	}
	s := sessionT(dir, &state.State{Files: map[string]state.FileState{}}, "", false)
	return &acceptEnv{
		s: s,
		t: &Template{
			Src:      &template.Source{Kind: template.KindDir, Commit: "c1"},
			Manifest: &manifest.Manifest{},
			Snap:     plan.Snap{},
			Rendered: map[string][]byte{},
			SrcOf:    map[string]string{},
			Vars:     render.Vars{},
		},
		b: &Base{Metas: plan.Snap{}, Bytes: map[string][]byte{}},
		o: &Ours{Snap: plan.Snap{}, Bytes: map[string][]byte{}, Prune: plan.PruneSet{}},
		d: &Decision{Actions: nil, Merged: map[string][]byte{}, Conflicted: map[string]bool{}, Conflicts: map[string][]byte{}},
		a: &AcceptRewrite{Set: map[string]bool{}, Olds: map[string]oldEntry{}},
	}
}

// writeConflictT 写 .xinit/conflicts/<p>（rewriteAccept 的读入口）。
func writeConflictT(t *testing.T, dir, p, content string) {
	t.Helper()
	full := filepath.Join(dir, state.DirName, "conflicts", filepath.FromSlash(p))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// metaPtr 是 *plan.FileMeta 构造器（动作 NewBase 用）。
func metaPtr(m plan.FileMeta) *plan.FileMeta { return &m }

// assertMetasBytesInSync 断言 I1：Metas/Bytes 键集恒等 + 每键 Hash 与字节自洽。
func assertMetasBytesInSync(t *testing.T, b *Base, ctx string) {
	t.Helper()
	mk, bk := sortedStringKeys(b.Metas), sortedStringKeys(b.Bytes)
	if !slices.Equal(mk, bk) {
		t.Errorf("%s：Metas 键集 %v ≠ Bytes 键集 %v（I1）", ctx, mk, bk)
	}
	for p, m := range b.Metas {
		data, ok := b.Bytes[p]
		if !ok {
			continue
		}
		if got := fsutil.CompareHash(data, m.Binary); got != m.Hash {
			t.Errorf("%s：Metas[%s].Hash = %s，字节实际 = %s（I1 同步破）", ctx, p, m.Hash, got)
		}
	}
}

// TestAcceptRewriteMetasBytesInSync 钉 I1（§4.1 :508 字面）：rewrite 与 restore
// 两个写点都必须保持 Metas/Bytes 键集恒等且 Hash 与字节自洽（binary 口径）。
func TestAcceptRewriteMetasBytesInSync(t *testing.T) {
	for _, had := range []bool{true, false} {
		ctx := map[bool]string{true: "had=true", false: "had=false"}[had]
		env := newAcceptEnv(t)
		p := "p.txt"
		writeConflictT(t, env.s.dir, p, "resolved")
		if had {
			env.b.Metas[p] = plan.FileMeta{Kind: render.KindRegular, Hash: fsutil.CompareHash([]byte("old"), false)}
			env.b.Bytes[p] = []byte("old")
		}
		a, err := rewriteAccept(env.s, env.b, env.o, []string{p})
		if err != nil {
			t.Fatal(err)
		}
		assertMetasBytesInSync(t, env.b, "rewrite/"+ctx)

		// 段2：accept 重写后仍判冲突（Merge + conflicted）→ restore 还原 →
		// 同步性仍须恒等（还原路径的 Metas/Bytes 必须同源）。
		env.d.Actions = []plan.Action{{Path: p, Kind: plan.Merge, NewBase: metaPtr(env.b.Metas[p])}}
		env.d.Conflicted[p] = true
		if err := refineDecision(env.s, env.t, env.b, env.o, env.d, a); err != nil {
			t.Fatal(err)
		}
		assertMetasBytesInSync(t, env.b, "restore/"+ctx)
	}
}

// TestAcceptPruneCandidatesAreSeparate 钉 I3（§1.3 :94）：prune 判定的候选集
// 独立展开（并集 ∪ Accept），绝不并入喂给 plan 的 Ours.Prune——
// 并集外的陈旧 conflicts 字面行照样拒绝 accept，且 Ours.Prune 不受污染。
func TestAcceptPruneCandidatesAreSeparate(t *testing.T) {
	// 段1：陈旧 conflicts 项（不在并集）的字面 prune 行 → 拒绝（§10.3 :906）。
	env := newAcceptEnv(t)
	if err := os.WriteFile(filepath.Join(env.s.dir, state.DirName, "prune.txt"), []byte("stale/*\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env.o.Union = []string{"keep.txt"}
	writeConflictT(t, env.s.dir, "stale/x.txt", "z")
	_, err := rewriteAccept(env.s, env.b, env.o, []string{"stale/x.txt"})
	if err == nil || !strings.Contains(err.Error(), "prune.txt") {
		t.Fatalf("陈旧 conflicts 项的字面 prune 行应拒绝 accept（先于读文件判）：err = %v", err)
	}
	if len(env.o.Prune) != 0 {
		t.Errorf("Ours.Prune（喂 plan 的一份）不得被 accept 候选污染：%v", env.o.Prune)
	}

	// 段2：正常 accept（未被 prune 命中）→ PrunedWithAccept 从并集 ∪ Accept
	// 独立展开；Ours.Prune 保持原值（两者是不同对象、不同口径）。
	env2 := newAcceptEnv(t)
	if err := os.WriteFile(filepath.Join(env2.s.dir, state.DirName, "prune.txt"), []byte("keep/*\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env2.o.Union = []string{"keep/a.txt", "kept.txt"}
	env2.o.Prune = plan.PruneSet{"keep/a.txt": true}
	writeConflictT(t, env2.s.dir, "kept.txt", "r")
	a, err := rewriteAccept(env2.s, env2.b, env2.o, []string{"kept.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if !a.PrunedWithAccept["keep/a.txt"] || a.PrunedWithAccept["kept.txt"] {
		t.Errorf("PrunedWithAccept = %v，want 仅 keep/a.txt（并集 ∪ Accept 展开）", a.PrunedWithAccept)
	}
	if !slices.Equal(sortedStringKeys(env2.o.Prune), []string{"keep/a.txt"}) {
		t.Errorf("Ours.Prune 被改写：%v", env2.o.Prune)
	}
}

// TestAcceptPostProcessingFiveBranches 钉 I16（§1.3 :98）：accept 后处理 5 分支
// + 2 类硬拒绝，表驱动。
//
// **SkipNote 分支（accept.go:149-152）现无任何 txtar 覆盖——本条是其唯一覆盖
// （评审 WN-6）。** 含 T5 O-3：base✗ BothAdded + accept 后仍冲突 → 条目按
// 还原删除处理（b.Metas/Bytes 删键、NewBase=nil——「仍冲突」由
// isConflictAction 的 ConflictBothAdded 分支判定）。
func TestAcceptPostProcessingFiveBranches(t *testing.T) {
	const p = "p.txt"
	tests := []struct {
		name  string
		build func(env *acceptEnv)
		check func(t *testing.T, env *acceptEnv, err error)
	}{
		{
			name:  "无 action（行 12）：条目按已不再冲突清理（Conflicts 删键）",
			build: func(env *acceptEnv) {},
			check: func(t *testing.T, env *acceptEnv, err error) {
				if err != nil {
					t.Fatal(err)
				}
				if _, ok := env.d.Conflicts[p]; ok {
					t.Error("无 action 分支应 delete(d.Conflicts, p)")
				}
			},
		},
		{
			name: "仍冲突 + 有旧条目：restore 还原（NewBase 与 base 同源）",
			build: func(env *acceptEnv) {
				env.b.Metas[p] = plan.FileMeta{Kind: render.KindRegular, Hash: fsutil.CompareHash([]byte("old"), false)}
				env.b.Bytes[p] = []byte("old")
				env.d.Conflicted[p] = true
				env.d.Actions = []plan.Action{{Path: p, Kind: plan.Merge}}
			},
			check: func(t *testing.T, env *acceptEnv, err error) {
				if err != nil {
					t.Fatal(err)
				}
				act := findAction(env.d.Actions, p)
				if act.NewBase == nil || act.NewBase.Hash != fsutil.CompareHash([]byte("old"), false) {
					t.Errorf("NewBase 应还原为旧条目：%+v", act.NewBase)
				}
				if got := env.b.Bytes[p]; string(got) != "old" {
					t.Errorf("base 字节应还原：%q", got)
				}
			},
		},
		{
			name: "仍冲突 + 无旧条目（base✗）：还原为删除（删键 + NewBase=nil）——含 T5 O-3（BothAdded）",
			build: func(env *acceptEnv) {
				env.d.Conflicted[p] = true
				env.d.Actions = []plan.Action{{Path: p, Kind: plan.ConflictBothAdded}}
			},
			check: func(t *testing.T, env *acceptEnv, err error) {
				if err != nil {
					t.Fatal(err)
				}
				act := findAction(env.d.Actions, p)
				if act.NewBase != nil {
					t.Errorf("base✗ 还原应 NewBase=nil：%+v", act.NewBase)
				}
				if _, ok := env.b.Metas[p]; ok {
					t.Error("base✗ 还原应 delete(b.Metas, p)")
				}
				if _, ok := env.b.Bytes[p]; ok {
					t.Error("base✗ 还原应 delete(b.Bytes, p)")
				}
			},
		},
		{
			name: "脱离管理（模板已删该路径）：Noop + 脱管 Note + 内容条目清理",
			build: func(env *acceptEnv) {
				env.d.Actions = []plan.Action{{Path: p, Kind: plan.WriteTheirs}}
			},
			check: func(t *testing.T, env *acceptEnv, err error) {
				if err != nil {
					t.Fatal(err)
				}
				act := findAction(env.d.Actions, p)
				if act.Kind != plan.Noop || act.NewBase != nil || act.Note != acceptDetachedNote {
					t.Errorf("脱管分支：Kind=%v NewBase=%+v Note=%q", act.Kind, act.NewBase, act.Note)
				}
				if _, ok := env.d.Conflicts[p]; ok {
					t.Error("脱管分支应 delete(d.Conflicts, p)")
				}
			},
		},
		{
			name: "照常（重写后干净、模板仍在）：决策表结论不动",
			build: func(env *acceptEnv) {
				env.t.Snap[p] = plan.FileMeta{Kind: render.KindRegular, Hash: fsutil.CompareHash([]byte("new"), false)}
				env.d.Actions = []plan.Action{{Path: p, Kind: plan.WriteTheirs, NewBase: metaPtr(plan.FileMeta{Kind: render.KindRegular, Hash: fsutil.CompareHash([]byte("new"), false)})}}
			},
			check: func(t *testing.T, env *acceptEnv, err error) {
				if err != nil {
					t.Fatal(err)
				}
				act := findAction(env.d.Actions, p)
				if act.Kind != plan.WriteTheirs || act.NewBase == nil || act.Note != "" {
					t.Errorf("照常分支不得改写动作：Kind=%v NewBase=%+v Note=%q", act.Kind, act.NewBase, act.Note)
				}
			},
		},
		{
			name: "硬拒绝：ConflictKind（accept 改变不了类型）",
			build: func(env *acceptEnv) {
				env.d.Actions = []plan.Action{{Path: p, Kind: plan.ConflictKind}}
			},
			check: func(t *testing.T, env *acceptEnv, err error) {
				if err == nil || !strings.Contains(err.Error(), "ConflictKind") {
					t.Errorf("ConflictKind 应硬拒绝：err = %v", err)
				}
			},
		},
		{
			name: "硬拒绝：SkipNote（跳过优先，§8.2 L7）——本条为该分支唯一覆盖（WN-6）",
			build: func(env *acceptEnv) {
				env.d.Actions = []plan.Action{{Path: p, Kind: plan.Noop, Note: plan.SkipNote}}
			},
			check: func(t *testing.T, env *acceptEnv, err error) {
				if err == nil || !strings.Contains(err.Error(), "跳过优先") {
					t.Errorf("SkipNote 应硬拒绝：err = %v", err)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := newAcceptEnv(t)
			writeConflictT(t, env.s.dir, p, "resolved")
			tc.build(env)
			a, err := rewriteAccept(env.s, env.b, env.o, []string{p})
			if err != nil {
				t.Fatalf("rewriteAccept 前置失败：%v", err)
			}
			env.d.Conflicts[p] = []byte("entry")
			err = refineDecision(env.s, env.t, env.b, env.o, env.d, a)
			tc.check(t, env, err)
		})
	}
}

// TestAcceptExecThreeBranches 钉 I31（§1.3 :102）：accept 重写的 Exec 三分支——
// base 有 → old.meta.Exec；base✗ → oursSnap[p].Exec；否则 false。
func TestAcceptExecThreeBranches(t *testing.T) {
	tests := []struct {
		name     string
		had      bool
		oldExec  bool
		oursHasP bool
		oursExec bool
		wantExec bool
	}{
		{"base 有：沿用旧条目 exec 位（true）", true, true, false, false, true},
		{"base 有：沿用旧条目 exec 位（false）", true, false, true, true, false},
		{"base✗：取 ours.exec", false, false, true, true, true},
		{"base✗ 且 ours 无该路径：false", false, false, false, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := newAcceptEnv(t)
			writeConflictT(t, env.s.dir, "p.txt", "resolved")
			if tc.had {
				env.b.Metas["p.txt"] = plan.FileMeta{Kind: render.KindRegular, Hash: fsutil.CompareHash([]byte("old"), false), Exec: tc.oldExec}
				env.b.Bytes["p.txt"] = []byte("old")
			}
			if tc.oursHasP {
				env.o.Snap["p.txt"] = plan.FileMeta{Kind: render.KindRegular, Hash: fsutil.CompareHash([]byte("new"), false), Exec: tc.oursExec}
			}
			if _, err := rewriteAccept(env.s, env.b, env.o, []string{"p.txt"}); err != nil {
				t.Fatal(err)
			}
			if got := env.b.Metas["p.txt"].Exec; got != tc.wantExec {
				t.Errorf("Exec = %v，want %v", got, tc.wantExec)
			}
		})
	}
}

// TestAcceptRejectsStalePruneLiteral 钉 I32 前半（§1.3 :103）：陈旧 conflicts
// 字面（P 已不在并集）的 prune 行同样拒绝——且校验序「先 prune 判定、后读
// 文件」（conflicts 文件不写；若顺序反了错误文案会变）。
func TestAcceptRejectsStalePruneLiteral(t *testing.T) {
	env := newAcceptEnv(t)
	if err := os.WriteFile(filepath.Join(env.s.dir, state.DirName, "prune.txt"), []byte("gone/*\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env.o.Union = []string{"keep.txt"}
	_, err := rewriteAccept(env.s, env.b, env.o, []string{"gone/x.txt"})
	if err == nil || !strings.Contains(err.Error(), "已在 .xinit/prune.txt 中") {
		t.Fatalf("陈旧 prune 字面应拒绝（且先于读文件）：err = %v", err)
	}
}

// TestAcceptRejectsNonCanonicalPath 钉 I32 后半：非规范相对路径（空串/绝对/
// 上跳/含反斜杠）在校验序最前拒绝——conflicts 文件不写（顺序证据）。
func TestAcceptRejectsNonCanonicalPath(t *testing.T) {
	for _, p := range []string{"", "/abs.txt", "../up.txt", `a\b.txt`} {
		env := newAcceptEnv(t)
		_, err := rewriteAccept(env.s, env.b, env.o, []string{p})
		if err == nil || !strings.Contains(err.Error(), "规范相对路径") {
			t.Errorf("--accept %q 应被路径校验拒绝：err = %v", p, err)
		}
	}
}

// TestAcceptCleanConvergeStateAdvances 钉 T5 O-1：accept 干净收敛场景的 state
// 断言——覆盖 baseSnap 别名通道。rewrite 就地改写 b.Metas/b.Bytes（map 别名
// 共享），advanceState 用同一 b 判定 advanced 并以 b.Bytes 为重写后内容落盘；
// 断言 state.files 与 .xinit/base/ 都推进到重写值（若别名在某处断开，这里
// 会看到旧值/自检中止）。
func TestAcceptCleanConvergeStateAdvances(t *testing.T) {
	newHash := fsutil.CompareHash([]byte("new"), false)
	oldHash := fsutil.CompareHash([]byte("old"), false)
	dir := projectWithState(t,
		state.TemplateRef{Source: "dir-src", Kind: "dir", Commit: "c0"},
		map[string]state.FileState{"p.txt": {Src: "p.txt", Hash: oldHash}})
	writeFilesT(t, dir, map[string]string{"p.txt": "new"})
	writeConflictT(t, dir, "p.txt", "new")
	st := mustLoadState(t, dir)
	s := sessionT(dir, st, t.TempDir(), false)

	b := &Base{
		Metas: plan.Snap{"p.txt": {Kind: render.KindRegular, Hash: oldHash}},
		Bytes: map[string][]byte{"p.txt": []byte("old")},
	}
	o := &Ours{
		Union: []string{"p.txt"},
		Snap:  plan.Snap{"p.txt": {Kind: render.KindRegular, Hash: newHash}},
		Bytes: map[string][]byte{"p.txt": []byte("new")},
		Prune: plan.PruneSet{},
	}
	tpl := &Template{
		Src:      &template.Source{Kind: template.KindDir, Commit: "c1"},
		Manifest: &manifest.Manifest{},
		Snap:     plan.Snap{"p.txt": {Kind: render.KindRegular, Hash: newHash}},
		Rendered: map[string][]byte{"p.txt": []byte("new")},
		SrcOf:    map[string]string{"p.txt": "p.txt"},
		Vars:     render.Vars{},
	}
	a, err := rewriteAccept(s, b, o, []string{"p.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if got := b.Metas["p.txt"].Hash; got != newHash {
		t.Fatalf("rewrite 后 base′.Hash = %s，want conflicts 内容 hash %s", got, newHash)
	}
	d := &Decision{
		Actions:    []plan.Action{{Path: "p.txt", Kind: plan.WriteTheirs, NewBase: metaPtr(plan.FileMeta{Kind: render.KindRegular, Hash: newHash})}},
		Merged:     map[string][]byte{},
		Conflicted: map[string]bool{},
		Conflicts:  map[string][]byte{},
	}
	if err := refineDecision(s, tpl, b, o, d, a); err != nil {
		t.Fatal(err)
	}
	if err := advanceState(s.dir, s.st, b, d.Actions, tpl.Rendered, tpl.SrcOf, tpl.Vars, tpl.Src, false, s.cacheRoot, engineVersionT); err != nil {
		t.Fatal(err)
	}
	st2 := mustLoadState(t, dir)
	fs, ok := st2.Files["p.txt"]
	if !ok || fs.Hash != newHash {
		t.Errorf("state.files[p.txt] = %+v（ok=%v），want Hash=%s（干净收敛应推进）", fs, ok, newHash)
	}
	data, err := state.ReadBase(dir, "p.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new" {
		t.Errorf("base/p.txt 落盘 = %q，want %q（别名通道的重写值）", data, "new")
	}
}
