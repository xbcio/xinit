//go:build windows

package hooks

import (
	"os"

	"golang.org/x/sys/windows"
)

// Interactive 报告 f 是否接在控制台上（§22.11；unix 档的 Windows 对档）。
//
// 与 unix 档的 devnull 判据同构：真控制台 GetConsoleMode 成功、管道/NUL
// 句柄失败。已知盲区（接受 + 立注）：mintty/MSYS2 伪终端不实现
// GetConsoleMode ⇒ 判非交互（保守方向：需 --interactive 才提问）；不做
// GetFileType==FILE_TYPE_CHAR 叠判（重蹈 NUL 误判）。
// [未核实：mintty/MSYS2 与 ConPTY 真机探针见 §22.17 R14-9]
func Interactive(f *os.File) bool {
	var m uint32
	return windows.GetConsoleMode(windows.Handle(f.Fd()), &m) == nil
}
