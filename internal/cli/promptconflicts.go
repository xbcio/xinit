package cli

import (
	"fmt"

	"github.com/xbcio/xinit/internal/plan"
)

// takeTheirsNote 是交互选择「改用模板版」的动作行尾注（§19.4.3 C4）。
const takeTheirsNote = "交互选择「改用模板版」：本地版本已备份到 .xinit/backup/（§19.4）"

// conflictHeader 是交互表头（§19.4.3 C1）：四行整块，只在可问条数 > 0 时
// 打印一次、紧接在第一个提问之前。首行 %d = 可问条数。
const conflictHeader = "交互式冲突解决（%d 条，§19.4）：逐条选择，回答不被保存（效果即本轮的正常写入与 base 推进）。\n" +
	"  y / yes = 用模板版覆盖本地（模板新内容本轮尚未展示；原内容先备份到 .xinit/backup/，本轮即收敛）。\n" +
	"  其他回答（含直接回车）= 保留本地（与现状一致：模板版写入 .xinit/conflicts/<路径>，本轮退 2）。\n" +
	"  想先查看模板新内容：回答 n 或直接回车——模板版会写入 .xinit/conflicts/<路径>；查看后按报告给出的 --accept 命令收敛，或手工把文件改到与模板一致后重跑。\n"

// askableConflict 报告该冲突路径是否在可问闭集内（§19.4.1）：
// kind ∈ {Merge(判冲突), ConflictBinary, ConflictBothAdded} ∧ theirs 内容存在。
// 闭集而非「排除 ConflictKind」：将来新增 Kind 默认不提问，需显式加入。
func askableConflict(p string, kind plan.ActionKind, conflicted map[string]bool, theirsRendered map[string][]byte) bool {
	if theirsRendered[p] == nil {
		return false
	}
	switch kind {
	case plan.Merge:
		return conflicted[p]
	case plan.ConflictBinary, plan.ConflictBothAdded:
		return true
	}
	return false
}

// takeTheirs 把该路径的动作改写为「改用模板版」（§19.4.2）：走与行 2/13 同一
// 条正常写入通道（备份 + 原子写 + base 推进）。SettleMergeClean 用**改写前**
// 的三方快照跑 §8.3/§8.5 修整 ⇒ generated false→true 不会发生（比手工等价
// 路径更保守）。
func takeTheirs(a *plan.Action, base, ours, theirs plan.Snap) {
	a.Kind = plan.WriteTheirs
	a.Note = takeTheirsNote
	plan.SettleMergeClean(a, base, ours, theirs)
}

// acceptableConflicts 是 --accept 可用集（§10.3）：冲突全集里非 ConflictKind
// 的路径。由 update.go 原内联循环原样抽出（零行为变化）。
func acceptableConflicts(conflicted []string, actions []plan.Action) []string {
	var out []string
	for _, p := range conflicted {
		if a := findAction(actions, p); a != nil && a.Kind != plan.ConflictKind {
			out = append(out, p)
		}
	}
	return out
}

// promptConflicts 是第 4b 步的问答循环（§19.4.4）：对每条可问路径按字节序问
// 「改用模板版？」，回答 y 的就地改写动作与两个集合。返回被改写条数。
// 可问集合为空时不打印任何文本。
func promptConflicts(ps *promptSession, actions []plan.Action, conflicted map[string]bool,
	conflicts map[string][]byte, base, ours, theirs plan.Snap,
	theirsRendered map[string][]byte) (int, error) {
	askable := make([]string, 0, len(conflicted))
	for _, p := range sortedStringKeys(conflicted) { // 字节序，脚本可依赖（§19.4.1）
		if a := findAction(actions, p); a != nil && askableConflict(p, a.Kind, conflicted, theirsRendered) {
			askable = append(askable, p)
		}
	}
	if len(askable) == 0 {
		return 0, nil
	}
	fmt.Fprintf(ps.out, conflictHeader, len(askable))
	taken := 0
	for i, p := range askable {
		a := findAction(actions, p)
		yes, eof, err := ps.askYesNo(fmt.Sprintf("%s（%s）改用模板版？[y/N] ", p, kindLabel(*a, conflicted[p]))) // C2
		if err != nil {
			return taken, fmt.Errorf("冲突交互读取失败: %w", err)
		}
		if eof {
			fmt.Fprintf(ps.out, "输入已结束（EOF）：其余 %d 条冲突按「保留本地」处理（§19.4）。\n", len(askable)-i) // C3
			return taken, nil
		}
		if !yes {
			continue // 保留本地 = 零改写（现状）
		}
		takeTheirs(a, base, ours, theirs)
		delete(conflicted, p)
		delete(conflicts, p)
		taken++
	}
	return taken, nil
}
