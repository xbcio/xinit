package update

// 来源：internal/cli/report_test.go（§4.3 整文件随迁，I-2 实施批 T2/M1）。

import (
	"bytes"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/xbcio/xinit/internal/apply"
	"github.com/xbcio/xinit/internal/manifest"
	"github.com/xbcio/xinit/internal/plan"
	"github.com/xbcio/xinit/internal/state"
	"github.com/xbcio/xinit/internal/template"
)

// §17.6 的四态行逐字断言（纯文本，不经 shellQuote）。节标题与行格式是
// 稳定报告面，e2e 与用户脚本按它断言——列宽与文案即契约。
// 第 5 行是 migrationText 的防御分支（四态之外的值原文照打、不静默），
// 不属于 §17.6 契约，随本节一并锁定列宽与「不吞掉缺陷」的行为。
func TestReportMigrationsSection(t *testing.T) {
	rep := &Report{
		Header: "xinit diff",
		Source: "/tpl",
		DryRun: true,
		Migrations: []MigrationNote{
			{Old: "old/a.txt", New: "new/a.txt", Status: "migrated"},
			{Old: "old/b.txt", New: "new/b.txt", Status: "fallback", Reason: "模板与本地都在改且相冲突"},
			{Old: "old/c.txt", New: "new/c.txt", Status: "rename"},
			{Old: "old/d.txt", New: "new/d.txt", Status: "skip", Reason: "生成物路径不参与迁移"},
			{Old: "old/e.txt", New: "new/e.txt", Status: "unknown", Reason: "x"},
		},
	}
	var buf strings.Builder
	rep.Print(&buf)
	got := buf.String()
	want := "路径迁移（§17）：5 条\n" +
		"  migrated             old/a.txt → new/a.txt（本地改动已随迁移保留；旧路径按删除处理）\n" +
		"  fallback             old/b.txt → new/b.txt（迁移未完成：模板与本地都在改且相冲突——已按行 9 保留本地、行 13 写入模板版；本地改动需手工合并到新路径，或把旧路径写进 .xinit/prune.txt）\n" +
		"  rename               old/c.txt → new/c.txt（本地无改动，按行 8/13 完成改名）\n" +
		"  skip                 old/d.txt → new/d.txt（跳过：生成物路径不参与迁移）\n" +
		"  unknown              old/e.txt → new/e.txt（unknown：x）\n"
	if !strings.Contains(got, want) {
		t.Errorf("路径迁移节与 §17.6 文案不符：\n--- got ---\n%s\n--- want 含 ---\n%s", got, want)
	}
}

// 无迁移行时不得打印节标题（空节会误导读者以为本轮有迁移）。
func TestReportMigrationsSectionAbsentWhenEmpty(t *testing.T) {
	rep := &Report{Header: "xinit diff", Source: "/tpl"}
	var buf strings.Builder
	rep.Print(&buf)
	if strings.Contains(buf.String(), "路径迁移") {
		t.Errorf("Migrations 为空时不得有迁移节:\n%s", buf.String())
	}
}

// kindLabel 的 migrate 分支（§17.6）：MigratedFrom 由 cli 迁移分类独占设置，
// 且必须判在 merge-conflict 之前（Q 的 Kind 仍是 Merge，标签不可回退成
// merge-conflict 而丢掉迁移事实）。
func TestKindLabelMigrate(t *testing.T) {
	tests := []struct {
		name       string
		a          plan.Action
		conflicted bool
		want       string
	}{
		{"迁移的 Q（MigratedFrom ≠ \"\" ∧ 非冲突）", plan.Action{Kind: plan.Merge, MigratedFrom: "old/a.txt"}, false, "migrate"},
		{"迁移分支先于冲突分支", plan.Action{Kind: plan.Merge, MigratedFrom: "old/a.txt"}, true, "migrate"},
		{"普通 Merge 冲突", plan.Action{Kind: plan.Merge}, true, "merge-conflict"},
		{"普通 Merge", plan.Action{Kind: plan.Merge}, false, "merge"},
		{"迁移的 P 仍是 delete 标签", plan.Action{Kind: plan.Delete}, false, "delete"},
	}
	for _, tc := range tests {
		if got := kindLabel(tc.a, tc.conflicted); got != tc.want {
			t.Errorf("%s: kindLabel = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// shellQuote 的形状断言（v1.0.1 F1）：单引号包裹，内部字面量不被 shell
// 求值；单引号自身走 POSIX 的关闭/转义/重开序列。
func TestShellQuote(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"a.txt", "'a.txt'"},
		{"a b.txt", "'a b.txt'"},           // 空格：不拆词
		{"a$(date).txt", "'a$(date).txt'"}, // $ 保持字面（%q 会触发命令替换）
		{"a`date`.txt", "'a`date`.txt'"},   // 反引号保持字面
		{`a"b.txt`, `'a"b.txt'`},           // 双引号在单引号里无需处理
		{`a\b.txt`, `'a\b.txt'`},           // 反斜杠在单引号里无需处理
		{"it's.txt", `'it'\''s.txt'`},      // 单引号：关闭 + 转义 + 重开
		{"", "''"},
	}
	for _, tc := range tests {
		if got := shellQuote(tc.in); got != tc.want {
			t.Errorf("shellQuote(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

// 真正的验收标准是「整条命令复制进 shell 后路径仍是原值」（F1 的本义：
// 原 %q 形态在含 $ 或反引号时会先被 shell 求值）——用 sh 做一次往返。
func TestShellQuoteRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("没有 sh：跳过往返验证")
	}
	paths := []string{
		"a.txt",
		"a b.txt",
		"a$(date).txt",
		"a`date`.txt",
		`a"b.txt`,
		`a\b.txt`,
		"it's.txt",
		"$HOME/`x`/'y'",
	}
	for _, p := range paths {
		quoted := shellQuote(p)
		out, err := exec.Command("sh", "-c", "printf %s "+quoted).Output()
		if err != nil {
			t.Fatalf("sh -c printf %s: %v", quoted, err)
		}
		if string(out) != p {
			t.Errorf("sh 往返还原 %q, want %q（quoted = %s）", out, p, quoted)
		}
	}
}

// 快照恢复行（§21.6-4 / §21.11 项 18）：文案逐字、位置在「base 降级」块
// 之前、仅 Restored > 0 时输出。
func TestUpdateReportPrintRestoredLine(t *testing.T) {
	rep := &Report{
		Header:   "xinit update",
		Source:   "/tpl",
		Restored: []string{"a.txt", "b.txt"},
		Degraded: []string{"c.txt"},
	}
	var buf strings.Builder
	rep.Print(&buf)
	out := buf.String()
	line := "base 恢复（来自渲染快照，不依赖模板源，§5.2、§21）：2 条\n"
	if !strings.Contains(out, line) {
		t.Errorf("恢复行缺失或文案不符（want 含 %q）：\n%s", line, out)
	}
	i, j := strings.Index(out, "base 恢复"), strings.Index(out, "base 降级")
	if i < 0 || j < 0 || i > j {
		t.Errorf("恢复行应在「base 降级」块之前：i=%d j=%d\n%s", i, j, out)
	}

	// 空 Restored：恢复行零出现（>0 才输出）。
	rep2 := &Report{Header: "xinit diff", Source: "/tpl", Degraded: []string{"c.txt"}}
	var buf2 strings.Builder
	rep2.Print(&buf2)
	if strings.Contains(buf2.String(), "base 恢复") {
		t.Errorf("Restored 为空时不得有恢复行:\n%s", buf2.String())
	}
}

// reportTplT 是 report() 直调的最小模板夹具（dir 源、无 hooks、无条目）。
func reportTplT() *Template {
	return &Template{
		Src:      &template.Source{Kind: template.KindDir, Revision: "r2", Commit: "c2"},
		Manifest: &manifest.Manifest{},
		Rendered: map[string][]byte{},
		SrcOf:    map[string]string{},
		Snap:     plan.Snap{},
	}
}

// reportSessionT 是 report() 直调的最小会话夹具（模板记录齐备）。
func reportSessionT(t *testing.T, dryRun bool) *session {
	t.Helper()
	return sessionT(t.TempDir(),
		&state.State{Template: state.TemplateRef{Source: "/tpl", Revision: "r1", Commit: "c1"}},
		"", dryRun)
}

// TestReportConflictedNotOverwrittenByResult 钉 I11（§2.3 Report.Conflicted /
// report.go 终局③）：实跑报告保持决策表的冲突全集（含 ConflictKind 且
// theirs 缺失的条目），不被 res.Conflicts（只含已写盘条目）覆盖——后者会把
// 该冲突漏报成「冲突 0」并绕开 D3 门禁（评审 E1）。
func TestReportConflictedNotOverwrittenByResult(t *testing.T) {
	const p = "linkish"
	d := &Decision{
		Actions:    []plan.Action{{Path: p, Kind: plan.ConflictKind}},
		Merged:     map[string][]byte{},
		Conflicted: map[string]bool{p: true},
		Conflicts:  map[string][]byte{}, // theirs 缺失：conflicts/ 无内容条目
	}
	w := &Delivery{Result: apply.Result{}} // 本轮没有条目写出 → res.Conflicts 空
	rep := report(reportSessionT(t, false), reportTplT(), &Base{}, &Ours{}, d,
		&Gate{}, w, VerdictOK, &HookOutcome{})
	if len(w.Result.Conflicts) != 0 {
		t.Fatalf("夹具前提：res.Conflicts 应为空；got %v", w.Result.Conflicts)
	}
	if !slices.Equal(rep.Conflicted, []string{p}) {
		t.Errorf("rep.Conflicted = %v，want [%s]（不得被 res.Conflicts 覆盖）", rep.Conflicted, p)
	}
	if len(rep.ConflictFiles) != 0 {
		t.Errorf("conflicts/ 无内容条目：ConflictFiles = %v", rep.ConflictFiles)
	}
}

// TestReportMergedTwoPaths 钉 I35（§2.3 Report.Merged）：dry-run（终局①，
// 预测径）与实跑（终局③）都取 Decision.Merged 的排序键集——两径同值且按
// 字节序（本夹具键序与插入序相反，未排序的实现无法稳定通过）。
func TestReportMergedTwoPaths(t *testing.T) {
	d := &Decision{
		Actions:    []plan.Action{},
		Merged:     map[string][]byte{"b.txt": []byte("B"), "a.txt": []byte("A")},
		Conflicted: map[string]bool{},
		Conflicts:  map[string][]byte{},
	}
	g := &Gate{Changes: apply.Changes{Writes: map[string][]byte{}, ExecBits: map[string]bool{}, Conflicts: map[string][]byte{}}}
	want := []string{"a.txt", "b.txt"}

	// 径 1：dry-run（w=nil，终局① 的预测赋值段）。
	repDry := report(reportSessionT(t, true), reportTplT(), &Base{}, &Ours{}, d,
		g, nil, VerdictChanged, nil)
	// 径 2：实跑（w 非 nil，终局③）。
	repRun := report(reportSessionT(t, false), reportTplT(), &Base{}, &Ours{}, d,
		g, &Delivery{Result: apply.Result{}}, VerdictOK, &HookOutcome{})
	if !slices.Equal(repDry.Merged, want) {
		t.Errorf("dry-run 径 Merged = %v，want %v（sortedKeys(Decision.Merged)）", repDry.Merged, want)
	}
	if !slices.Equal(repRun.Merged, want) {
		t.Errorf("实跑径 Merged = %v，want %v", repRun.Merged, want)
	}
	if !slices.Equal(repDry.Merged, repRun.Merged) {
		t.Errorf("两径 Merged 应同值：dry=%v run=%v", repDry.Merged, repRun.Merged)
	}
}

// TestOrNone 钉 T2 B-O1（R-M4-5）：orNone（report.go:395）只测 update 侧副本
// ——cli 侧同名副本零改动（§2.7 #8 两处各留，BASE 继承缺口在此收口）。表驱动
// + 报告行端到端：OldRevision 为空时版本行渲染「-」（report.go:229 消费点）。
func TestOrNone(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", "-"},
		{"main", "main"},
		{"v1.0.0", "v1.0.0"},
	}
	for _, tc := range tests {
		if got := orNone(tc.in); got != tc.want {
			t.Errorf("orNone(%q) = %q，want %q", tc.in, got, tc.want)
		}
	}

	// 端到端一行：版本行经 orNone 渲染空值为「-」。
	rep := &Report{Header: "xinit: 更新", Source: "/tpl", OldRevision: "", NewRevision: "main"}
	var buf bytes.Buffer
	rep.Print(&buf)
	if !strings.Contains(buf.String(), "版本: - → main") {
		t.Errorf("报告版本行应经 orNone 把空值渲染为「-」；got:\n%s", buf.String())
	}
}
