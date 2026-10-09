//go:build unix

package apply

import "syscall"

// currentUmask 读取本进程 umask（读一次要设置-恢复一次）。
// 仅 apply 的单线程执行路径使用；不并发调用。
func currentUmask() uint32 {
	old := syscall.Umask(0)
	syscall.Umask(old)
	return uint32(old)
}
