package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestHelpMentionsCommands(t *testing.T) {
	out, err := exec.Command("go", "run", ".", "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("--help 退出码非零: %v\n%s", err, out)
	}
	// 评审 F2 M-2：root 接线（AddCommand）删一行就会静默失去命令——五个
	// 子命令名一并在 --help 上钉住（命令行为另有 txtar 端到端覆盖）。
	for _, name := range []string{"init", "update", "diff", "status", "adopt"} {
		if !strings.Contains(string(out), name) {
			t.Errorf("--help 未列出 %s 子命令:\n%s", name, out)
		}
	}
}
