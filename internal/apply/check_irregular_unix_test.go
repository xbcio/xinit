//go:build unix

package apply

import (
	"fmt"
	"path/filepath"
	"syscall"
	"testing"
)

// TestCheckWritesIrregularParent 是 §22.5（R2e）的 darwin 覆盖面：非常规类型
// （此机用 FIFO 代理 Windows 的 reparse 类）作**中间段**时必须显式 fail-closed，
// 且走专用文案——不得落进「常规文件」那支（§22.18 验收 #6）。
//
// 本文件仅在 unix 构建：`syscall.Mkfifo` 在 windows 不存在，而 `make crosswin`
// 要逐包 `go test -c`。
func TestCheckWritesIrregularParent(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "fp"), 0o644); err != nil {
		t.Skipf("本机不支持 mkfifo：%v", err)
	}
	refusals, err := CheckWrites(dir, []string{"fp/x.txt"}, nil)
	if err != nil {
		t.Fatalf("FIFO 中间段只应拒绝该路径，不应中止整轮：%v", err)
	}
	r, ok := refusalFor(t, refusals, "fp/x.txt")
	if !ok {
		t.Fatalf("非常规类型中间段必须被拒（fail-closed）：%v", refusals)
	}
	want := fmt.Sprintf("父路径 %q 是特殊文件形态（重解析点/特殊文件等），不受支持", "fp")
	if r.Reason != want {
		t.Errorf("§22.5 专用文案逐字不符：\n got %q\nwant %q", r.Reason, want)
	}
}
