//go:build !unix

package hooks

import "os"

// Interactive：非 unix 平台恒 false（§19.2.1 末行）。仓库当前不在非 unix
// 平台编译（state/lock.go 是 //go:build unix）；随 Windows 线一并评估。
func Interactive(*os.File) bool { return false }
