package hooks

import (
	"strings"
	"testing"
)

func TestLineDiffEqualReturnsEmpty(t *testing.T) {
	if got := LineDiff("a\nb\n", "a\nb\n"); got != "" {
		t.Fatalf("相同内容应返回空串: %q", got)
	}
}

func TestLineDiffChange(t *testing.T) {
	got := LineDiff("set -e\necho one\n", "set -e\necho two\n")
	if !strings.Contains(got, "- echo one") || !strings.Contains(got, "+ echo two") {
		t.Fatalf("变更行未按 -/+ 标出:\n%s", got)
	}
	if !strings.Contains(got, "  set -e") {
		t.Fatalf("未变行应带两空格前缀:\n%s", got)
	}
}

func TestLineDiffPureAddition(t *testing.T) {
	got := LineDiff("a\n", "a\nb\n")
	if strings.Contains(got, "- ") {
		t.Fatalf("纯新增不应有删除行:\n%s", got)
	}
	if !strings.Contains(got, "+ b") {
		t.Fatalf("缺新增行:\n%s", got)
	}
}

func TestLineDiffEmptyOld(t *testing.T) {
	got := LineDiff("", "a\n")
	if !strings.Contains(got, "+ a") || strings.Contains(got, "- ") {
		t.Fatalf("空旧文本 → 全为新增:\n%s", got)
	}
}
