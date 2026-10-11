//go:build windows

package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// Lock 是 .xinit/lock 上的 LockFileEx 句柄（unix 档 flock 的对档，§22.3）。
//
// 语义对应：flock 的锁挂在 open file description 上、LockFileEx 的锁挂在文件
// 句柄上——两者都随关闭（f.Close）释放、进程死亡由内核回收，都不用
// O_CREATE|O_EXCL（后者在进程被 kill 后残留锁文件，用户会永远卡住，§9.4）。
// 锁文件常驻不删（与 unix 同理由：删除会引入「A 删、B 已锁旧对象、C 又建新
// 文件锁成功」的竞态）。
//
// 共享模式立注（§22.3）：Go 的 os.OpenFile 在 Windows 以
// FILE_SHARE_READ|FILE_SHARE_WRITE 打开（不含 FILE_SHARE_DELETE）——持锁期间
// 外部工具删不掉 .xinit/lock（与 unix 不同；README 已知限制）。
// [未核实：同进程二次加锁与双进程互斥的真机探针见 §22.17 R14-3]
type Lock struct{ f *os.File }

// LockProject 对 .xinit/lock 加独占锁（非阻塞；LOCKFILE_FAIL_IMMEDIATELY）。
//
// 拿不到锁说明同项目有另一个 xinit 在跑——报错退出，绝不排队等待（对档 unix）。
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
	var ol windows.Overlapped // 1 字节区间 = 整锁（任意非零区间都互斥）
	err = windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &ol)
	if err != nil {
		f.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
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
	var ol windows.Overlapped
	_ = windows.UnlockFileEx(windows.Handle(l.f.Fd()), 0, 1, 0, &ol)
	_ = l.f.Close()
	l.f = nil
}
