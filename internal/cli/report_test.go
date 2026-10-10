package cli

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/xbcio/xinit/internal/plan"
)

// §17.6 的四态行逐字断言（纯文本，不经 shellQuote）。节标题与行格式是
// 稳定报告面，e2e 与用户脚本按它断言——列宽与文案即契约。
// 第 5 行是 migrationText 的防御分支（四态之外的值原文照打、不静默），
// 不属于 §17.6 契约，随本节一并锁定列宽与「不吞掉缺陷」的行为。
func TestReportMigrationsSection(t *testing.T) {
	rep := &UpdateReport{
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
	rep := &UpdateReport{Header: "xinit diff", Source: "/tpl"}
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
