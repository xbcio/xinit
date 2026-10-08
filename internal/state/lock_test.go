package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLockProjectExcludesSecondHolder(t *testing.T) {
	dir := t.TempDir()

	l1, err := LockProject(dir)
	if err != nil {
		t.Fatalf("首次加锁失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, DirName, "lock")); err != nil {
		t.Fatalf("锁文件未创建: %v", err)
	}

	// flock 的锁挂在 open file description 上：同进程二次 open + flock 也会失败
	if _, err := LockProject(dir); err == nil {
		t.Fatal("同一项目第二次加锁应当失败")
	}

	l1.Unlock()
	l2, err := LockProject(dir)
	if err != nil {
		t.Fatalf("解锁后应可重新加锁: %v", err)
	}
	l2.Unlock()
	l2.Unlock() // Unlock 必须幂等（defer Unlock 与显式调用可能并存）
}

func TestLockUnlockNilSafe(t *testing.T) {
	var l *Lock
	l.Unlock() // 不得 panic
}
