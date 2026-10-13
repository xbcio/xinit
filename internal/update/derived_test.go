package update

// M3a C2 断言：派生字段单点（I17/I18，设计稿 §4.1 行）。
// 落点 internal/update（§4.3 规则 1「被测符号在哪，测试在哪」）。

import (
	"slices"
	"testing"

	"github.com/xbcio/xinit/internal/manifest"
	"github.com/xbcio/xinit/internal/plan"
	"github.com/xbcio/xinit/internal/state"
	"github.com/xbcio/xinit/internal/template"
)

// TestAcceptableRecomputedAfterAccept 钉 I17：Acceptable 必须按「当前」冲突
// 全集与 Actions 重算——accept 解决后的集合收缩、ConflictKind 剔除、无
// action 与内容条目缺失等边界都同步反映，不依赖任何缓存值。
//
// 降级口径（brief C2 允许、报告登记）：真 refineDecision（P6）需要 session
// 级夹具（读盘 + Template/Base/Ours 全套），构造成本过重；此处以单点函数
// derivedFields 的表驱动 + 变异红证替代。P6 落值点（accept.go）与 report()
// 两处的等价性由「同一表达式出处」的调用点核 + 变异覆盖。
func TestAcceptableRecomputedAfterAccept(t *testing.T) {
	act := func(p string, k plan.ActionKind) plan.Action { return plan.Action{Path: p, Kind: k} }
	tests := []struct {
		name       string
		conflicted map[string]bool
		conflicts  map[string][]byte
		actions    []plan.Action
		wantConf   []string
		wantFiles  []string
		wantAccept []string
	}{
		{
			name:       "空集",
			conflicted: map[string]bool{},
			conflicts:  map[string][]byte{},
			wantConf:   nil,
			wantFiles:  nil,
			wantAccept: nil,
		},
		{
			name:       "混合：ConflictKind 剔除（accept 不可用），其余按全集保留",
			conflicted: map[string]bool{"a.txt": true, "b.txt": true, "c.txt": true},
			conflicts:  map[string][]byte{"a.txt": []byte("x"), "c.txt": []byte("y")}, // b.txt（ConflictKind）无内容条目
			actions:    []plan.Action{act("a.txt", plan.Merge), act("b.txt", plan.ConflictKind), act("c.txt", plan.Merge)},
			wantConf:   []string{"a.txt", "b.txt", "c.txt"},
			wantFiles:  []string{"a.txt", "c.txt"},
			wantAccept: []string{"a.txt", "c.txt"},
		},
		{
			name:       "accept 解决后：集合收缩，Acceptable 同步重算",
			conflicted: map[string]bool{"b.txt": true, "c.txt": true}, // a.txt 已被 accept 解决（从全集与内容条目移除）
			conflicts:  map[string][]byte{"c.txt": []byte("y")},
			actions:    []plan.Action{act("a.txt", plan.WriteTheirs), act("b.txt", plan.ConflictKind), act("c.txt", plan.Merge)},
			wantConf:   []string{"b.txt", "c.txt"},
			wantFiles:  []string{"c.txt"},
			wantAccept: []string{"c.txt"},
		},
		{
			name:       "全 ConflictKind：Acceptable 为空",
			conflicted: map[string]bool{"b.txt": true},
			conflicts:  map[string][]byte{},
			actions:    []plan.Action{act("b.txt", plan.ConflictKind)},
			wantConf:   []string{"b.txt"},
			wantFiles:  nil,
			wantAccept: nil,
		},
		{
			name:       "内容条目缺失的边界：Acceptable 从冲突全集起算，不随 Conflicts 收窄",
			conflicted: map[string]bool{"x.txt": true},
			conflicts:  map[string][]byte{},
			actions:    []plan.Action{act("x.txt", plan.Merge)},
			wantConf:   []string{"x.txt"},
			wantFiles:  nil,
			wantAccept: []string{"x.txt"},
		},
		{
			name:       "无 action（findAction 为 nil）：不入 Acceptable",
			conflicted: map[string]bool{"p.txt": true},
			conflicts:  map[string][]byte{"p.txt": []byte("z")},
			actions:    nil,
			wantConf:   []string{"p.txt"},
			wantFiles:  []string{"p.txt"},
			wantAccept: nil,
		},
		{
			name:       "字节序：乱序输入 → 三个返回都排序",
			conflicted: map[string]bool{"d.txt": true, "b.txt": true, "a.txt": true, "c.txt": true},
			conflicts:  map[string][]byte{"d.txt": []byte("1"), "b.txt": []byte("2"), "a.txt": []byte("3"), "c.txt": []byte("4")},
			actions:    []plan.Action{act("d.txt", plan.Merge), act("b.txt", plan.Merge), act("a.txt", plan.Merge), act("c.txt", plan.Merge)},
			wantConf:   []string{"a.txt", "b.txt", "c.txt", "d.txt"},
			wantFiles:  []string{"a.txt", "b.txt", "c.txt", "d.txt"},
			wantAccept: []string{"a.txt", "b.txt", "c.txt", "d.txt"},
		},
	}
	for _, tc := range tests {
		d := &Decision{Conflicted: tc.conflicted, Conflicts: tc.conflicts, Actions: tc.actions}
		conf, files, accept := derivedFields(d)
		if !slices.Equal(conf, tc.wantConf) {
			t.Errorf("%s：conflicted = %v，want %v", tc.name, conf, tc.wantConf)
		}
		if !slices.Equal(files, tc.wantFiles) {
			t.Errorf("%s：conflictFiles = %v，want %v", tc.name, files, tc.wantFiles)
		}
		if !slices.Equal(accept, tc.wantAccept) {
			t.Errorf("%s：acceptable = %v，want %v", tc.name, accept, tc.wantAccept)
		}
	}
}

// TestDerivedFieldsRecomputedAfterPrompt 钉 I18 的时序入口：taken（P7 交互
// 改写）轮里 report() 的三个派生字段必须是「改写后集合」的重算值——断言
// 报告只消费当前集合（批末回写：Decision 缓存字段已删，report() 三字段
// 一律由 derivedFields 单点派生）。
func TestDerivedFieldsRecomputedAfterPrompt(t *testing.T) {
	s := sessionT(t.TempDir(),
		&state.State{Template: state.TemplateRef{Source: "/tpl", Revision: "r1", Commit: "c1"}}, "", false)
	tpl := &Template{
		Src:      &template.Source{Revision: "r2", Commit: "c2"},
		Manifest: &manifest.Manifest{}, // 无 post_update → hooksRun false
	}
	d := &Decision{
		Actions: []plan.Action{
			{Path: "a.txt", Kind: plan.WriteTheirs}, // P7 已改写（takeTheirs）
			{Path: "b.txt", Kind: plan.Merge},
			{Path: "c.txt", Kind: plan.ConflictKind},
		},
		Conflicted: map[string]bool{"b.txt": true, "c.txt": true}, // a.txt 已被 takeTheirs 解决
		Conflicts:  map[string][]byte{"b.txt": []byte("x")},
	}
	g := &Gate{Taken: 1}
	w := &Delivery{}
	h := &HookOutcome{}
	rep := report(s, tpl, &Base{}, &Ours{}, d, g, w, VerdictOK, h)

	if !slices.Equal(rep.Conflicted, []string{"b.txt", "c.txt"}) {
		t.Errorf("rep.Conflicted = %v，want [b.txt c.txt]（P7 后的当前集合）", rep.Conflicted)
	}
	if !slices.Equal(rep.ConflictFiles, []string{"b.txt"}) {
		t.Errorf("rep.ConflictFiles = %v，want [b.txt]", rep.ConflictFiles)
	}
	if !slices.Equal(rep.Acceptable, []string{"b.txt"}) {
		t.Errorf("rep.Acceptable = %v，want [b.txt]（按当前集合重算）", rep.Acceptable)
	}
}

// TestDerivedFieldsRecomputedWithoutTake 钉 O-1（T7a 评审转入口，R-M3b-6）：
// 既有用例只走 Taken>0 的 P7 重算支；本用例把 Taken 置 0——report() 只剩
// 主装配点一次 derivedFields 赋值，派生字段必须同样按当前集合重算（堵住
// 主装配点改读 Decision 缓存字段的形式等价缺口）。
func TestDerivedFieldsRecomputedWithoutTake(t *testing.T) {
	s := sessionT(t.TempDir(),
		&state.State{Template: state.TemplateRef{Source: "/tpl", Revision: "r1", Commit: "c1"}}, "", false)
	tpl := &Template{
		Src:      &template.Source{Revision: "r2", Commit: "c2"},
		Manifest: &manifest.Manifest{}, // 无 post_update → hooksRun false
	}
	d := &Decision{
		Actions: []plan.Action{
			{Path: "a.txt", Kind: plan.WriteTheirs},
			{Path: "b.txt", Kind: plan.Merge},
			{Path: "c.txt", Kind: plan.ConflictKind},
		},
		Conflicted: map[string]bool{"b.txt": true, "c.txt": true},
		Conflicts:  map[string][]byte{"b.txt": []byte("x")},
	}
	g := &Gate{} // Taken == 0：不进 P7 重算支
	rep := report(s, tpl, &Base{}, &Ours{}, d, g, &Delivery{}, VerdictOK, &HookOutcome{})

	if !slices.Equal(rep.Conflicted, []string{"b.txt", "c.txt"}) {
		t.Errorf("rep.Conflicted = %v，want [b.txt c.txt]", rep.Conflicted)
	}
	if !slices.Equal(rep.ConflictFiles, []string{"b.txt"}) {
		t.Errorf("rep.ConflictFiles = %v，want [b.txt]", rep.ConflictFiles)
	}
	if !slices.Equal(rep.Acceptable, []string{"b.txt"}) {
		t.Errorf("rep.Acceptable = %v，want [b.txt]（Taken==0 的主装配点同样重算）", rep.Acceptable)
	}
}
