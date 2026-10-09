package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunExecutesInProjectDir(t *testing.T) {
	dir := t.TempDir()
	res, err := Run(dir, "echo hi > out.txt")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode=%d，想要 0", res.ExitCode)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "out.txt")); err != nil || strings.TrimSpace(string(b)) != "hi" {
		t.Fatalf("钩子未在项目根执行: %v %q", err, b)
	}
}

func TestRunWholeBlockOneShell(t *testing.T) {
	// 整块交一个 sh：块内 cd/变量/&& 在后续行可见（逐行执行会失败）。
	dir := t.TempDir()
	// T2 裁定：块失败经由 && 传播、以 ExitCode 观察（err 仅表示起不来，见 TestRunCapturesOutputAndNonZeroExit）。
	res, err := Run(dir, "cd sub && echo $PWD > abs.txt")
	if err != nil {
		t.Fatalf("块内命令失败不应是 error（err 仅用于起不来）: %v", err)
	}
	if res.ExitCode == 0 {
		t.Fatal("cd 到不存在的目录应整块失败") // 先失败：sub 不存在
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(dir, "cd sub\npwd > p.txt\ncd ..\necho done"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "sub", "p.txt"))
	if err != nil || !strings.Contains(string(b), "sub") {
		t.Fatalf("块内状态未跨行可见（逐行执行？）: %v %q", err, b)
	}
}

func TestRunCapturesOutputAndNonZeroExit(t *testing.T) {
	dir := t.TempDir()
	res, err := Run(dir, "echo out; echo err >&2; exit 7")
	if err != nil {
		t.Fatalf("命令非零退出不应是 error: %v", err)
	}
	if res.ExitCode != 7 {
		t.Fatalf("ExitCode=%d，想要 7", res.ExitCode)
	}
	s := string(res.Output)
	if !strings.Contains(s, "out") || !strings.Contains(s, "err") {
		t.Fatalf("输出未合并捕获: %q", s)
	}
}

func TestRunMissingSh(t *testing.T) {
	t.Setenv("PATH", "")
	if _, err := Run(t.TempDir(), "echo hi"); err == nil {
		t.Fatal("sh 不可寻时必须报错（§3.1），而不是静默")
	}
}

func TestConfirmAcceptsYAndYes(t *testing.T) {
	for _, in := range []string{"y\n", "Y\n", "yes\n", "YES\n"} {
		var out strings.Builder
		ok, err := Confirm(strings.NewReader(in), &out)
		if err != nil || !ok {
			t.Fatalf("输入 %q 应接受: ok=%v err=%v", in, ok, err)
		}
		if !strings.Contains(out.String(), "[y/N]") {
			t.Fatalf("提示行缺失: %q", out.String())
		}
	}
}

func TestConfirmRejectsOtherAndEOF(t *testing.T) {
	for _, in := range []string{"n\n", "\n", "foo\n", ""} {
		var out strings.Builder
		ok, err := Confirm(strings.NewReader(in), &out)
		if err != nil || ok {
			t.Fatalf("输入 %q 应拒绝: ok=%v err=%v", in, ok, err)
		}
	}
}
