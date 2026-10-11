//go:build windows

package state

import (
	"errors"

	"golang.org/x/sys/windows"
)

// renameRetryableErr：Windows 上杀软/索引器持句柄会瞬态占用目标
// （数十 ms 后即消失）——这两类错误值得有界重试（§22.8）。
func renameRetryableErr(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) ||
		errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}
