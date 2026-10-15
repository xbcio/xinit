package cli

import (
	"bytes"
	"strings"
	"testing"
)

// runVersionFlag 以 --version 跑一遍根命令，返回标准输出。
func runVersionFlag(t *testing.T) string {
	t.Helper()
	var out bytes.Buffer
	root := newRootCmd()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--version"})
	if err := root.Execute(); err != nil {
		t.Fatalf("--version 执行失败: %v", err)
	}
	return out.String()
}

func TestVersionFlagUsesInjectedVersion(t *testing.T) {
	old := Version
	t.Cleanup(func() { Version = old })
	Version = "v9.9.9"

	got := strings.TrimSpace(runVersionFlag(t))
	if got != "xinit version v9.9.9" {
		t.Errorf("--version 输出 = %q，期望 %q", got, "xinit version v9.9.9")
	}
}

// 没有 ldflags 注入时（go run / go test / 直接 go build）必须回退到构建信息，
// 不能输出空串——否则用户拿到的是 "xinit version "。
func TestVersionFlagFallsBackWithoutInjection(t *testing.T) {
	old := Version
	t.Cleanup(func() { Version = old })
	Version = ""

	got := strings.TrimSpace(runVersionFlag(t))
	rest := strings.TrimPrefix(got, "xinit version ")
	if rest == got || strings.TrimSpace(rest) == "" {
		t.Errorf("--version 回退输出 = %q，期望 \"xinit version <非空>\"", got)
	}
}
