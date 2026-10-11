//go:build !unix && !windows

package hooks

import "os"

// Interactive：非 unix 且非 Windows 的平台恒 false（§19.2.1 末行）。仓库不在
// 这类平台编译（锁实现只有 unix / windows 两档）；Windows 档见
// interactive_windows.go（§22.11）。
func Interactive(*os.File) bool { return false }
