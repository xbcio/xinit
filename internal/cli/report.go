package cli

import (
	"fmt"
	"io"

	"github.com/xbcio/xinit/internal/apply"
	"github.com/xbcio/xinit/internal/plan"
)

// 退出码（§11.5）。
const (
	CodeOK        = 0 // 成功，无待处理项
	CodeError     = 1 // 错误
	CodePending   = 2 // 有冲突、折叠拒绝或障碍物待解决
	CodeHookFail  = 3 // 钩子失败（M3）
	CodeNeedTrust = 4 // 需要信任确认（M3）
	CodeChanged   = 5 // diff 有非 Noop / status --fetch 有更新
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

// conflictExits 是冲突的两条出路（R11：M2 不打印 --accept 命令——该旗标
// M3 才存在，打印一个跑不通的命令比不打印更糟）。
const conflictExits = "出路一：手工解决冲突，使内容与模板一致（或等模板回退），重跑 update 即收敛；\n" +
	"出路二：把路径写进 .xinit/prune.txt，使其脱离管理（模板对它的改动不再同步）。\n" +
	"参考：模板新内容已写到 .xinit/conflicts/<路径>。注：--accept（声明已解决）在 M3 提供。\n"

// RenameNote 是 §7.5 的「疑似改名」一行。动作本身仍按行 8/9/13 走——
// v1 不做自动改名，只提示本地改动需要手工迁移。
type RenameNote struct {
	Old, New  string
	LocalKept bool // 旧路径是 KeepWarned（行 9）：本地改动需手动迁移
}

// UpdateReport 是 update / diff 的完整结果；Print 的输出就是用户看到的报告。
type UpdateReport struct {
	Header      string // "xinit update" / "xinit diff"
	Source      string // 模板来源
	OldRevision string // 本轮开始时的记录（dir 源为空）
	NewRevision string
	OldCommit   string
	NewCommit   string
	DryRun      bool
	Actions     []plan.Action // 全部动作（含 Noop），按路径排序（Plan 保证）
	Written     []string
	Deleted     []string
	Chmodded    []string // 只调了 exec 位的路径（R7 通道）
	Merged      []string // 干净合并、结果已写回工作树的路径
	Conflicted  []string // 四类冲突动作的路径（= 本轮 conflicts/ 重建清单）
	Resolved    []string // 上轮 conflicts/ 里本轮已收敛、已清理的路径
	Blocked     []string // 第 4 步拒绝：conflicts/ 里仍未解决、必须先处理的路径
	Refused     []apply.Refusal
	Degraded    []string     // §5.2 降级为 base✗ 的路径
	Renames     []RenameNote // §7.5：只在 dry-run（diff / --dry-run）产出
	BackupDir   string       // 相对项目根；未备份时 ""
	ExitCode    int
}

// Print 输出报告。行前缀（kindLabel）是稳定标识，供脚本 grep。
func (r *UpdateReport) Print(w io.Writer) {
	fmt.Fprintf(w, "%s: %s\n", r.Header, r.Source)
	if r.OldRevision != "" || r.NewRevision != "" {
		fmt.Fprintf(w, "版本: %s → %s\n", orNone(r.OldRevision), orNone(r.NewRevision))
	}

	if len(r.Blocked) > 0 {
		fmt.Fprintf(w, "未解决的冲突（%d 条）——先处理它们，update 才会执行：\n", len(r.Blocked))
		for _, p := range r.Blocked {
			fmt.Fprintf(w, "  %-20s %s\n", "conflict", p)
		}
		fmt.Fprint(w, conflictExits)
		if !r.DryRun {
			return
		}
		fmt.Fprintln(w, "（以下为冲突解决后会执行的动作，仅供参考）")
	}

	conflicted := make(map[string]bool, len(r.Conflicted))
	for _, p := range r.Conflicted {
		conflicted[p] = true
	}
	for _, a := range r.Actions {
		if a.Kind == plan.Noop && a.Note == "" {
			continue // 无备注的 Noop 不占行（否则每轮刷屏）
		}
		fmt.Fprintf(w, "  %-20s %s%s\n", kindLabel(a, conflicted[a.Path]), a.Path, noteSuffix(a.Note))
	}

	fmt.Fprintf(w, "汇总: 写入 %d、删除 %d、chmod %d、冲突 %d", len(r.Written), len(r.Deleted), len(r.Chmodded), len(r.Conflicted))
	if len(r.Merged) > 0 {
		fmt.Fprintf(w, "、合并 %d", len(r.Merged))
	}
	if len(r.Resolved) > 0 {
		fmt.Fprintf(w, "、已解决 %d", len(r.Resolved))
	}
	fmt.Fprintln(w)

	if len(r.Renames) > 0 {
		fmt.Fprintf(w, "疑似改名（§7.5，diff 只报告、不自动迁移）：%d 条\n", len(r.Renames))
		for _, rn := range r.Renames {
			hint := "旧路径按行 8 删除，本地无改动"
			if rn.LocalKept {
				hint = "你本地对旧路径的改动需手动迁移到新路径"
			}
			fmt.Fprintf(w, "  %-20s %s → %s（%s）\n", "rename?", rn.Old, rn.New, hint)
		}
	}
	if len(r.Degraded) > 0 {
		fmt.Fprintf(w, "base 降级（无法重建，本轮按「没有 base」处理，§5.2）：%d 条\n", len(r.Degraded))
		for _, p := range r.Degraded {
			fmt.Fprintf(w, "  %-20s %s\n", "degraded", p)
		}
	}
	if len(r.Refused) > 0 {
		fmt.Fprintf(w, "折叠拒绝 / 障碍物（%d 条，已跳过、保持旧 base；其余路径照常处理，§9.3）：\n", len(r.Refused))
		for _, rf := range r.Refused {
			fmt.Fprintf(w, "  %-20s %s: %s\n", "refuse", rf.Path, rf.Reason)
		}
	}
	if len(r.Conflicted) > 0 {
		fmt.Fprintf(w, "冲突（%d 条）需要你处理：\n", len(r.Conflicted))
		for _, p := range r.Conflicted {
			fmt.Fprintf(w, "  %-20s %s\n", "conflict", p)
		}
		fmt.Fprint(w, conflictExits)
	}
	if r.BackupDir != "" {
		fmt.Fprintf(w, "备份: %s\n", r.BackupDir)
	}
	if r.DryRun {
		fmt.Fprint(w, "（--dry-run：以上为预测，未写任何文件、未改 state）\n")
	}
}

// kindLabel 是报告行的稳定前缀。与 plan.ActionKind.String() 一致，
// 只有「Merge 且本轮判冲突」要区分成 merge-conflict（它的落地是
// conflicts/ 而不是工作树，读者必须能一眼分开）。
func kindLabel(a plan.Action, conflicted bool) string {
	if a.Kind == plan.Merge && conflicted {
		return "merge-conflict"
	}
	return a.Kind.String()
}

func noteSuffix(note string) string {
	if note == "" {
		return ""
	}
	return "（" + note + "）"
}

func orNone(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
