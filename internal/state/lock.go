//go:build unix

package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Lock 是 .xinit/lock 上的 flock 句柄。
//
// 用 flock(2) 而不是 O_CREATE|O_EXCL：后者在进程被 kill 后残留锁文件，
// 用户会永远卡住（规范 §9.4）。flock 的锁随 fd 关闭自动释放。
type Lock struct{ f *os.File }

// LockProject 对 .xinit/lock 加独占锁（非阻塞）。
//
// 拿不到锁说明同项目有另一个 xinit 在跑 —— 报错退出，绝不排队等待。
// 解锁后**不删除**锁文件：删除会引入「A 删掉文件、B 已在旧 inode 上加锁、
// C 又创建新文件加锁成功」的经典竞态。锁文件常驻是 flock 的正确用法。
func LockProject(projectDir string) (*Lock, error) {
	dir := filepath.Join(projectDir, DirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建 %s: %w", dir, err)
	}
	p := filepath.Join(dir, "lock")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("打开项目锁 %s: %w", p, err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("%s 被另一个 xinit 进程锁定：同一项目一次只能跑一个 update", p)
		}
		return nil, fmt.Errorf("获取项目锁 %s: %w", p, err)
	}
	return &Lock{f: f}, nil
}

// Unlock 释放锁。幂等；nil 接收者安全（defer l.Unlock() 可以无条件写）。
func (l *Lock) Unlock() {
	if l == nil || l.f == nil {
		return
	}
	_ = unix.Flock(int(l.f.Fd()), unix.LOCK_UN)
	_ = l.f.Close()
	l.f = nil
}
