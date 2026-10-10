//go:build darwin || linux

package hooks

import (
	"os"

	"golang.org/x/sys/unix"
)

// Interactive 报告 f 是否连着终端（TTY，§19.2.1）。判据是一次 termios
// ioctl：只有真终端成功。`/dev/null` 是字符设备但不是终端——旧判据
// os.ModeCharDevice 会误判为可交互，紧接着读到 EOF（T2-F2 的修正点）。
// 只取两个常量与一个 ioctl，故不引入 golang.org/x/term（x/sys 已是直接依赖）。
func Interactive(f *os.File) bool {
	_, err := unix.IoctlGetTermios(int(f.Fd()), ioctlReadTermios)
	return err == nil
}
