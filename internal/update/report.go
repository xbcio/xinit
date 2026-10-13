package update

// 来源：internal/cli/report.go 行 43-427（报告类型 + 打印族搬迁，I-2 实施批 T2/M1）。
// 未搬：退出码常量 Code* 与 ExitError（§2.7 #1/#2，留 cli）。

import (
	"fmt"
	"io"
	"strings"

	"github.com/xbcio/xinit/internal/apply"
	"github.com/xbcio/xinit/internal/hooks"
	"github.com/xbcio/xinit/internal/plan"
	"github.com/xbcio/xinit/internal/template"
)

// conflictExitsMain 是出路一第一行（两种变体共用；§10.3：M3 起 --accept 真实
// 可用，出路一直接给出命令，不再是 M2 的占位注记）。kindOnly 轮（该节冲突全为
// ConflictKind）换用 conflictExitsMainKindOnly（M9 O1）。
const conflictExitsMain = "出路一：手工解决冲突，使内容与模板一致（或等模板回退），重跑 update 即收敛；\n"

// conflictExitsMainKindOnly 是「该节冲突全为类型冲突（ConflictKind）」时的出路一
// 第一行（M9 O1）：类型冲突无 --accept 出口，「使内容与模板一致」不成立——出口
// 是 prune 或移走障碍物（§8.1/§10.3）。
const conflictExitsMainKindOnly = "出路一：类型冲突无 --accept 出口——写进 .xinit/prune.txt（脱离管理），或移走障碍物，重跑 update 即收敛；\n"

// conflictExitsAccept 是出路一第二行——只在**该节确实打印过 accept? 行**时
// 拼接：否则「上面的 xinit update --accept」悬空；且 ConflictKind 根本不可
// --accept（§10.3「accept 改变不了类型」），对它的轮次给 accept 指令是误导
// （2026-10-11 M7）。
const conflictExitsAccept = "        若解决结果与模板不同，用上面的 xinit update --accept <路径> 声明已解决（把你看到并认可的版本记为 base，§10.3）。\n"

// conflictExitsPrune 是出路二。
const conflictExitsPrune = "出路二：把路径写进 .xinit/prune.txt，使其脱离管理（模板对它的改动不再同步）。\n"

// restoredLineFmt 是「base 恢复」行（§21.6-4 / §21.11 项 18）——restore.go
// 的硬错误尾注（snapshotNote，判据 10）共用同一串，文案单一来源。
const restoredLineFmt = "base 恢复（来自渲染快照，不依赖模板源，§5.2、§21）：%d 条"

// conflictFilesRef 只在确有 conflicts/ 内容条目时打印（评审 E1/m-3：
// ConflictKind 且 theirs 缺失时没有条目，指向 conflicts/ 是不实信息）。
// 尾部的 \n 是它的一部分：它是冲突报告的末行，缺了会与后续输出黏行
// （v1.0.1 F2/O6——dry-run 的尾注、备份行都紧跟在它后面）。
const conflictFilesRef = "参考：模板新内容已写到 .xinit/conflicts/<路径>。\n"

// conflictExitsFor 组合冲突出路文案；hasFiles=false 时省掉「参考：」句，
// hasAccept=false 时省掉出路一第二行（该节没有 accept? 行可指），kindOnly=true
// （该节冲突全为 ConflictKind）时出路一主句换成 kind-only 变体（M9 O1）。
func conflictExitsFor(hasFiles, hasAccept, kindOnly bool) string {
	s := conflictExitsMain
	if kindOnly {
		s = conflictExitsMainKindOnly
	}
	if hasAccept {
		s += conflictExitsAccept
	}
	s += conflictExitsPrune
	if hasFiles {
		s += conflictFilesRef
	}
	return s
}

// allKindConflicts 报告 paths（Blocked/Conflicted 之一）是否全为 ConflictKind；
// 空集或存在非 kind/无动作条目一律 false（保守回退既有主句）。
func allKindConflicts(paths []string, actions []plan.Action) bool {
	if len(paths) == 0 {
		return false
	}
	kind := make(map[string]plan.ActionKind, len(actions))
	for _, a := range actions {
		kind[a.Path] = a.Kind
	}
	for _, p := range paths {
		if k, ok := kind[p]; !ok || k != plan.ConflictKind {
			return false
		}
	}
	return true
}

// Report 是 update / diff 的完整结果；Print 的输出就是用户看到的报告。
type Report struct {
	Header      string // "xinit update" / "xinit diff"
	Source      string // 模板来源
	OldRevision string // 本轮开始时的记录（dir 源为空）
	NewRevision string
	OldCommit   string
	NewCommit   string
	DryRun      bool
	Pending     bool          // 本轮以 2 结束（冲突/折叠拒绝/障碍物待解决）：版本记录不推进
	Actions     []plan.Action // 全部动作（含 Noop），按路径排序（Plan 保证）
	Written     []string
	Deleted     []string
	Chmodded    []string // 只调了 exec 位的路径（R7 通道）
	Merged      []string // 干净合并、结果已写回工作树的路径（dry-run 为将写入的预测）
	Conflicted  []string // 四类冲突动作的路径（全集：含 conflicts/ 无内容条目的 ConflictKind）
	// ConflictFiles 是本轮 conflicts/ 有内容条目的路径（⊆ Conflicted；
	// dry-run 为将写入）。「参考：conflicts/」行只在它有值时打印。
	ConflictFiles []string
	// Acceptable 是冲突全集里可 --accept 的路径（非 ConflictKind；dry-run 同）。
	Acceptable []string
	Resolved   []string // 上轮 conflicts/ 里本轮已收敛、已清理的路径
	Blocked    []string // 第 4 步拒绝：conflicts/ 里仍未解决、必须先处理的路径
	Refused    []apply.Refusal
	Degraded   []string // §5.2 降级为 base✗ 的路径
	// Restored 是本轮由渲染快照恢复（§21.6）的路径（排序 = need 序 = 字节序；
	// dry-run/diff 的恢复同样进内存、同样报告）。「base 恢复」行仅 >0 时输出。
	Restored []string
	// Migrations 是 §17 路径迁移的报告行（dry-run 与实跑同产出，按 Old 排序）。
	Migrations    []MigrationNote
	HookName      string   // "post_update"；未声明钩子的清单为 ""
	HookOutput    []string // 钩子合并输出，按行（空输出为 nil）
	HookFailed    bool
	HookExitCode  int
	HookBlock     string   // 失败时重打失败命令用
	HookUnplanned []string // §10.6 计划外副作用
	HookModified  []string // §10.6 计划内被钩子改写
	BackupDir     string   // 相对项目根；未备份时 ""
	Overwritten   []string // §8.2 行 5：本地版本已另存的路径
	// Verdict 是终局语义（§2.3）；cli 用一张 5 行表映射为 §11.5 退出码。
	Verdict Verdict
}

// Print 输出报告。行前缀（kindLabel）是稳定标识，供脚本 grep。
func (r *Report) Print(w io.Writer) {
	fmt.Fprintf(w, "%s: %s\n", r.Header, r.Source)
	if r.OldRevision != "" || r.NewRevision != "" {
		fmt.Fprintf(w, "版本: %s → %s\n", orNone(r.OldRevision), orNone(r.NewRevision))
		if r.Pending {
			fmt.Fprintf(w, "（本轮仍待解决：state 里的版本记录保持 %s，收敛后重跑再推进；§10.2 第 9 步）\n",
				orNone(r.OldRevision))
		}
	}

	// accept 可用集：冲突全集里可 --accept 的路径（T7 评审 Nit-4：命令路径
	// 加引号，Blocked 节与冲突节共用同一格式与文本；v1.0.1 F1：引号必须是
	// shell 字面量——%q 是 Go 引号，路径含 `$`／反引号时复制执行会被求值，
	// 改用 shellQuote 的单引号包裹）。
	acceptable := make(map[string]bool, len(r.Acceptable))
	for _, p := range r.Acceptable {
		acceptable[p] = true
	}

	if len(r.Blocked) > 0 {
		fmt.Fprintf(w, "未解决的冲突（%d 条）——先处理它们，update 才会执行：\n", len(r.Blocked))
		for _, p := range r.Blocked {
			fmt.Fprintf(w, "  %-20s %s\n", "conflict", p)
		}
		// Blocked ∩ Acceptable（accept 也豁免第 4 步，update 侧 except）：
		// 这一轮正是用户最需要命令的时候，而出路文案指向「上面的 --accept
		// 命令」——不打就成了悬空引用（T7 评审 Minor-1）。出路一的 accept 句
		// 随 blockedAccept 出现（M7 T2）：一条 accept? 行都没打时它必悬空。
		blockedAccept := false
		for _, p := range r.Blocked {
			if acceptable[p] {
				fmt.Fprintf(w, "  %-20s xinit update --accept %s\n", "accept?", shellQuote(p))
				blockedAccept = true
			}
		}
		// 被阻断的路径由 listConflicts 从磁盘枚举而来——conflicts/ 文件确实
		// 在，参考句成立。
		fmt.Fprint(w, conflictExitsFor(true, blockedAccept, allKindConflicts(r.Blocked, r.Actions)))
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

	if len(r.Migrations) > 0 {
		fmt.Fprintf(w, "路径迁移（§17）：%d 条\n", len(r.Migrations))
		for _, m := range r.Migrations {
			fmt.Fprintf(w, "  %-20s %s → %s（%s）\n", m.Status, m.Old, m.New, migrationText(m))
		}
	}
	// 恢复行在「base 降级」块之前（§21.6-4，形状对齐区块头）。
	if len(r.Restored) > 0 {
		fmt.Fprintf(w, restoredLineFmt+"\n", len(r.Restored))
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
		conflictedAccept := false
		for _, p := range r.Conflicted {
			fmt.Fprintf(w, "  %-20s %s\n", "conflict", p)
			if acceptable[p] {
				fmt.Fprintf(w, "  %-20s xinit update --accept %s\n", "accept?", shellQuote(p))
				conflictedAccept = true
			}
		}
		fmt.Fprint(w, conflictExitsFor(len(r.ConflictFiles) > 0, conflictedAccept, allKindConflicts(r.Conflicted, r.Actions)))
	}
	if r.BackupDir != "" {
		fmt.Fprintf(w, "备份: %s\n", r.BackupDir)
	}
	if len(r.Overwritten) > 0 {
		fmt.Fprintf(w, "本地改动被模板覆盖（generated 行 5）：%d 条原内容另存 .xinit/overwritten/（持久保留，§8.2）\n", len(r.Overwritten))
		for _, p := range r.Overwritten {
			fmt.Fprintf(w, "  %-20s %s\n", "overwritten", p)
		}
	}
	if r.HookName != "" {
		exit := 0
		if r.HookFailed {
			exit = r.HookExitCode
		}
		hooks.PrintOutput(w, r.HookName, r.HookOutput, exit)
	}
	if r.HookFailed {
		hooks.PrintFailure(w, r.HookName, r.HookBlock, r.HookExitCode)
	}
	hooks.PrintSideEffects(w, r.HookUnplanned, r.HookModified)
	if r.DryRun {
		tail := "（--dry-run：以上为预测，未写任何文件、未改 state）\n"
		if r.Header == "xinit diff" {
			tail = "（diff 只读预览：以上为预测，未写任何文件、未改 state）\n"
		}
		fmt.Fprint(w, tail)
	}
}

// kindLabel 是报告行的稳定前缀。与 plan.ActionKind.String() 一致，
// 只有两处例外：迁移改写后的 Q（MigratedFrom ≠ ""，§17.3）单独成 migrate
// 标签——它虽是 Merge 动作，落地却是「新路径磁盘新建 + 内容来自旧路径」；
// 以及「Merge 且本轮判冲突」区分成 merge-conflict（它的落地是 conflicts/
// 而不是工作树，读者必须能一眼分开）。
func kindLabel(a plan.Action, conflicted bool) string {
	// migrate 判在最前：迁移动作的 Kind 仍是 Merge，冲突分支不得抢先
	// （§17.6 的标签由 MigratedFrom 驱动，不随 conflicted 变化）。
	if a.MigratedFrom != "" {
		return "migrate"
	}
	if a.Kind == plan.Merge && conflicted {
		return "merge-conflict"
	}
	return a.Kind.String()
}

// migrationText 是 §17.6 四态行的括号文案。MigratedFrom / 理由由迁移分类
// 产出，此处只负责措辞——fallback 与 skip 必须带上理由原文（不得弱化事实：
// fallback 行还要给人工去向）。
func migrationText(m MigrationNote) string {
	switch m.Status {
	case "migrated":
		return "本地改动已随迁移保留；旧路径按删除处理"
	case "fallback":
		return "迁移未完成：" + m.Reason + "——已按行 9 保留本地、行 13 写入模板版；本地改动需手工合并到新路径，或把旧路径写进 .xinit/prune.txt"
	case "rename":
		return "本地无改动，按行 8/13 完成改名"
	case "skip":
		return "跳过：" + m.Reason
	}
	// 四态由分类器独占产出，出现第五值即代码缺陷：原文照打，不静默。
	return m.Status + "：" + m.Reason
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

// shellQuote 把路径包成可安全复制执行的 shell 字面量：单引号包裹，内部的
// 单引号按 POSIX 惯用法转义（关闭单引号 + 反斜杠转义的单引号 + 重开）。
// 不能用 %q——那是 Go 字符串引号，`$`、反引号、`\` 在 shell 里仍各自有
// 含义（v1.0.1 F1）。
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// hookBlockLines 把钩子块/输出切成行（与 hooks 包的 splitLines 同语义，
// 空串 → nil）。
func hookBlockLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// hookVersion 是信任记录的版本值：git 源 = revision，dir 源 = 树 hash
// （§4.3；dir 源 Revision 为空，用 Commit 保证记录非空——空串是 Seen 的
// marker，两者不可混）。
func hookVersion(src *template.Source) string {
	if src.Revision != "" {
		return src.Revision
	}
	return src.Commit
}
