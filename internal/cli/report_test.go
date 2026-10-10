package cli

import (
	"os/exec"
	"testing"
)

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
