//go:build darwin || linux

package hooks

import (
	"os"
	"path/filepath"
	"testing"
)

// 真值表（§19.2.1）里 true 的那一格只能由真 PTY 覆盖（T4 的
// interactive_tty.txtar）；单测不开 pty，这里只钉死「非终端 ⇒ false」。
// 本文件限定 darwin/linux：legacy unix 分支（unix ∧ !darwin ∧ !linux）按
// §19.2.1 保留旧的 ModeCharDevice 判据，/dev/null 在那些平台判 true。

func TestInteractiveDevNullFalse(t *testing.T) {
	// 修正点（§19.2.1）：/dev/null 是字符设备但**不是终端**。旧判据
	// （ModeCharDevice）会把它判为可交互，紧接着读到 EOF —— 「打印提示后
	// 拒绝」的隐藏耦合，`go test` 的 fd0 就是 /dev/null。
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if Interactive(f) {
		t.Fatalf("os.DevNull 应判 false（§19.2.1 的修正点）")
	}
}

func TestInteractiveRegularFileFalse(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(p, []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if Interactive(f) {
		t.Fatalf("普通文件（%s 重定向）应判 false", p)
	}
}

func TestInteractivePipeFalse(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if Interactive(r) {
		t.Fatal("管道（`|`、testscript 的 stdin 注入）应判 false")
	}
}

func TestInteractiveDirFalse(t *testing.T) {
	f, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if Interactive(f) {
		t.Fatal("目录 fd 应判 false")
	}
}
