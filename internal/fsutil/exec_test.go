package fsutil

import "testing"

// TestExecSupportedDefault 直测本平台的默认值（darwin = true）。三个平台档的
// 实现经 crosswin（windows 档编译）与 linux 容器（T7）覆盖——运行时直测仅
// darwin，故断言取 unix 侧真值。
func TestExecSupportedDefault(t *testing.T) {
	old := ExecSupported
	defer func() { ExecSupported = old }()
	if !ExecSupported {
		t.Fatal("darwin 上 ExecSupported 应为 true（exec_unix.go）")
	}
}
