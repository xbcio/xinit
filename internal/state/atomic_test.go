package state

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// transientRenameErr：模拟 Windows 上杀软/索引器持句柄的瞬态 rename 失败。
// 用 os.LinkError 包 EACCES：与 Windows 的 Errno 5（ERROR_ACCESS_DENIED）同形，
// 且 os.IsPermission 口径成立（§22.8）。
func transientRenameErr(oldpath, newpath string) error {
	return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EACCES}
}

func renameTestDirNames(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

// TestRenameBoundedRetry：瞬态错误在有界重试内成功；耗尽上界后失败且保留原始错误。
func TestRenameBoundedRetry(t *testing.T) {
	t.Run("瞬态错误重试后成功", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, "a.txt")

		origFile, origRetryable := renameFile, renameRetryable
		defer func() { renameFile, renameRetryable = origFile, origRetryable }()

		calls := 0
		renameFile = func(oldpath, newpath string) error {
			calls++
			if calls <= 3 {
				return transientRenameErr(oldpath, newpath)
			}
			return os.Rename(oldpath, newpath)
		}
		renameRetryable = func(error) bool { return true }

		if err := WriteFileAtomic(p, []byte("payload"), 0o644); err != nil {
			t.Fatalf("第 4 次 rename 应成功: %v", err)
		}
		if calls != 4 {
			t.Errorf("rename 调用次数 = %d, want 4", calls)
		}
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "payload" {
			t.Errorf("内容 = %q, want %q", got, "payload")
		}
	})

	t.Run("恒瞬态失败恰重试上界次", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, "a.txt") // 目标不存在：只读清位分支不介入

		origFile, origRetryable := renameFile, renameRetryable
		defer func() { renameFile, renameRetryable = origFile, origRetryable }()

		calls := 0
		renameFile = func(oldpath, newpath string) error {
			calls++
			return transientRenameErr(oldpath, newpath)
		}
		renameRetryable = func(error) bool { return true }

		err := WriteFileAtomic(p, []byte("payload"), 0o644)
		if err == nil {
			t.Fatal("恒瞬态错误应当失败")
		}
		if calls != renameAttempts {
			t.Errorf("rename 调用次数 = %d, want %d（有界重试上界）", calls, renameAttempts)
		}
		if !errors.Is(err, syscall.EACCES) {
			t.Errorf("错误应含原始 err（errors.Is EACCES）: %v", err)
		}
		if names := renameTestDirNames(t, dir); len(names) != 0 {
			t.Errorf("失败后 tmp 应已清理，残留: %v", names)
		}
	})
}

// TestRenameReadOnlyTargetClearBit：ACCESS_DENIED 且目标只读 → 清目标只读位 → 重试一次成功。
func TestRenameReadOnlyTargetClearBit(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(p, []byte("old"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o444); err != nil { // 显式 chmod，不吃 umask
		t.Fatal(err)
	}

	origFile, origRetryable := renameFile, renameRetryable
	defer func() { renameFile, renameRetryable = origFile, origRetryable }()

	// 显式覆写恒 false，不吃默认分类器：默认恒 false 仅 unix 档成立，
	// Windows 档会把 EACCES（Errno 5 = ERROR_ACCESS_DENIED）认作可重试，
	// 用例会结构性必败。显式覆写使(a)用例平台无关、(b)语义完好保留
	// 「分类器不认、但属权限类」的只读清位路径、(c)交叉编译面完整。
	renameRetryable = func(error) bool { return false }
	calls := 0
	renameFile = func(oldpath, newpath string) error {
		calls++
		if calls == 1 {
			return transientRenameErr(oldpath, newpath)
		}
		return os.Rename(oldpath, newpath)
	}

	if err := WriteFileAtomic(p, []byte("new"), 0o644); err != nil {
		t.Fatalf("清位重试应成功: %v", err)
	}
	if calls != 2 {
		t.Errorf("rename 调用次数 = %d, want 2（首调 EACCES + 清位后重试一次）", calls)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o200 == 0 {
		t.Errorf("目标只读位未清除: %v", info.Mode().Perm())
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Errorf("内容 = %q, want %q", got, "new")
	}
	if names := renameTestDirNames(t, dir); len(names) != 1 || names[0] != "a.txt" {
		t.Errorf("tmp 不应残留: %v", names)
	}
}

// TestRenameReadOnlyFailClosed：清位后仍失败 → 钉死文案的 fail-closed 错误；tmp 已清理。
func TestRenameReadOnlyFailClosed(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(p, []byte("old"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o444); err != nil {
		t.Fatal(err)
	}

	origFile, origRetryable := renameFile, renameRetryable
	defer func() { renameFile, renameRetryable = origFile, origRetryable }()

	// 显式覆写恒 false（理由同 TestRenameReadOnlyTargetClearBit：默认恒 false
	// 仅 unix 档成立，Windows 档会重试 EACCES 致计数断言必败）。
	renameRetryable = func(error) bool { return false }
	calls := 0
	renameFile = func(oldpath, newpath string) error {
		calls++
		return transientRenameErr(oldpath, newpath)
	}

	err := WriteFileAtomic(p, []byte("new"), 0o644)
	if err == nil {
		t.Fatal("清位后仍失败应当报错（fail-closed）")
	}
	if !strings.Contains(err.Error(), "目标只读位已清除、写入仍失败") {
		t.Errorf("错误应含钉死文案: %v", err)
	}
	if !errors.Is(err, syscall.EACCES) {
		t.Errorf("错误应以 %%w 包裹原始 err: %v", err)
	}
	if calls != 2 {
		t.Errorf("rename 调用次数 = %d, want 2（首调 + 清位后重试一次）", calls)
	}
	got, rerr := os.ReadFile(p)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(got) != "old" {
		t.Errorf("失败路径目标内容应保持原样 = %q, want %q", got, "old")
	}
	if names := renameTestDirNames(t, dir); len(names) != 1 || names[0] != "a.txt" {
		t.Errorf("tmp 已清理（defer 契约），残留: %v", names)
	}
}
