//go:build unix

package fsutil

import "testing"

// TestExecSupportedDefault 直测本平台的默认值（darwin = true）。三个平台档的
// 实现经 crosswin（windows 档编译）与 linux 容器（T7）覆盖——运行时直测仅
// darwin，故断言取 unix 侧真值。
//
// `//go:build unix`（终审 M9）：断言的是 unix 侧真值，本文件不参与 Windows
// 编译面——真机 `go test ./...` 不再因此翻红；Windows 档 `ExecSupported=false`
// 的语义由四消费方代理单测（显式覆写接缝）与 crosswin 编译门禁覆盖。
func TestExecSupportedDefault(t *testing.T) {
	old := ExecSupported
	defer func() { ExecSupported = old }()
	if !ExecSupported {
		t.Fatal("darwin 上 ExecSupported 应为 true（exec_unix.go）")
	}
}
