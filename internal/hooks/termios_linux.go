//go:build linux

package hooks

import "golang.org/x/sys/unix"

// ioctlReadTermios 是「读 termios」的 ioctl 号：darwin 用 TIOCGETA、
// linux 用 TCGETS，数值不同，必须分平台（与 x/term 同构）。
const ioctlReadTermios = unix.TCGETS
