//go:build unix && !darwin && !linux

package hooks

import "os"

// Interactive 报告 f 是否连着一个字符设备（TTY）。非 TTY（管道、CI）下
// 不提示交互，直接走 --yes / 退出码 4 的路径。
//
// 其他 unix（BSD 系等）没有本包引入的 termios ioctl 号，保持 v1 判据——
// 该档零回归（含 /dev/null 被误判为可交互的既有行为，§19.2.1）。代价是
// 这些平台若 ModeCharDevice 判 true 而读失败，仍走「提示后拒绝」的既有路径。
func Interactive(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
