package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestHelpMentionsInit(t *testing.T) {
	out, err := exec.Command("go", "run", ".", "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("--help 退出码非零: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "init") {
		t.Errorf("--help 未列出 init 子命令:\n%s", out)
	}
}
