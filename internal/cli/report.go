package cli

import (
	"fmt"

	"github.com/xbcio/xinit/internal/template"
)

// 本文件是 §2.7 的「留守段」：报告类型与打印族已随管线迁入 internal/update
// （T2/M1），这里只留进程级符号——退出码常量、ExitError，以及两处各留一份
// 的 6 行/2 行纯函数副本（§2.7 #8/#9）。

// 退出码（§11.5）。
const (
	CodeOK        = 0 // 成功，无待处理项
	CodeError     = 1 // 错误
	CodePending   = 2 // 有冲突、折叠拒绝或障碍物待解决
	CodeHookFail  = 3 // 钩子失败
	CodeNeedTrust = 4 // 需要信任确认
	CodeChanged   = 5 // diff 有非 Noop / status --fetch 有更新
	CodeCheck     = 6 // check 有 FAIL 项（§18.1）
)

// ExitError 携带进程退出码；main 用 errors.As 取出它。
//
// Silent=true 表示 stdout 上的报告已经把问题说全（update/diff 的全部非零
// 退出都属此类），main 不再补一行重复的错误说明。
type ExitError struct {
	Code   int
	Silent bool
	msg    string
}

func (e *ExitError) Error() string { return e.msg }

// exitError 供「只有一行错误、没有报告」的场合构造带码的错误
// （Task 13 的 diff 参数校验、Task 14 的 status --fetch）。
func exitError(code int, format string, args ...any) *ExitError {
	return &ExitError{Code: code, msg: fmt.Sprintf(format, args...)}
}

// orNone 供空值渲染成 "-"（§2.7 #8：与 internal/update 的同名副本两处各留
// 一份——6 行纯函数，抽公共包不值；adopt/status/backflow 三个留守消费方继续用）。
func orNone(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// hookVersion 是信任记录的版本值：git 源 = revision，dir 源 = 树 hash
// （§4.3；dir 源 Revision 为空，用 Commit 保证记录非空——空串是 Seen 的
// marker，两者不可混）。
//
// §2.7 #9：与 internal/update 的同名副本两处各留一份（2 行）＋**同式约束**
// ——两处派生必须同式，否则 init 与 update 的信任记录漂移。
func hookVersion(src *template.Source) string {
	if src.Revision != "" {
		return src.Revision
	}
	return src.Commit
}
