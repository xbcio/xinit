package update

// 来源：internal/update/update.go 的 Run 块 19-23（M2-P7 gate 抽取 + 终局段
// 重构，R-M2-13）+ dry-run 预测族/ListConflicts/step4Unresolved 整函数搬家；
// internal/update/conflicts.go 整文件并入（promptConflicts 族，R-M2-14；该文
// 件原 1:1 自 cli/promptconflicts.go，T2/M1）。I-2 实施批 T5/M2b。阶段判据
// （§3-M2）：函数体与源区间逐字对应，差异仅「参数接入」「返回值包装」与附加
// 裁定类（R-M2-2/3/13/14）——逐条见 task-5-report.md。

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/xbcio/xinit/internal/apply"
	"github.com/xbcio/xinit/internal/plan"
	"github.com/xbcio/xinit/internal/state"
)

// Gate 是「写不写」的门后事实（P7 写；§2.3）。
type Gate struct {
	Changes  apply.Changes
	Forecast []apply.Refusal // apply.Forecast 的产物；仅 dry-run 终局时计算（非 dry-run 走 P9 的按需重算）
	Existing []string        // conflicts/ 现有条目（P9 的 Resolved 基准）
	Blocked  []string        // 第 4 步拒绝（I7）
	Taken    int             // 交互改写条数（I18）
}

// gate 是 P7（§2.2）：第 4 步阻塞检查（块 19，含 pending 终局）+ 交互冲突
// 解决（块 20）+ 组装变更集（块 21）+ dry-run 预测终局（块 22）+ 脏检查
// （块 23）。返回值第二项 = 终局语义（终局时有效），第三项 = 是否终局；
// rep.* 留守段依 R-M2-3/R-M2-13 在 Run（值经 g/d）。
func gate(s *session, t *Template, b *Base, o *Ours, d *Decision, a *AcceptRewrite) (*Gate, Verdict, bool, error) {
	// 9. 第 4 步：未解决冲突检查（conflicts/ 现有条目 ∩ 本轮冲突集合）
	existing, err := ListConflicts(s.dir)
	if err != nil {
		return nil, VerdictOK, false, err
	}
	g := &Gate{}
	g.Blocked = step4Unresolved(existing, d.Conflicted)
	// --accept 豁免第 4 步检查（§10.3）：这是这套设计里唯一「必须先豁免
	// 才能退出循环」的接口，不能省。
	g.Blocked = except(g.Blocked, a.Set)
	g.Existing = existing
	if len(g.Blocked) > 0 && !s.dryRun {
		return g, VerdictPending, true, nil // 不执行任何写操作、不推进任何状态（§10.2 第 4 步）
	}

	// 9c. 第 4b 步：交互式冲突解决（§10.2 第 4b 步、§19.4）——仅可交互时，
	// 落在第 4 步之后、组装变更集之前，且在写任何内容之前。--dry-run / diff
	// 恒不提问（只读预测）。
	if !s.dryRun && s.interactive != nil {
		taken, err := promptConflicts(s.interactive, d.Actions, d.Conflicted, d.Conflicts, b.Metas, o.Snap, t.Snap, t.Rendered)
		if err != nil {
			return nil, VerdictOK, false, err
		}
		g.Taken = taken
	}

	// 10. 组装变更集（第 8 步的输入）
	changes, err := buildChanges(b.Metas, o.Snap, t.Snap, d.Actions, t.Rendered, d.Merged, d.Conflicts)
	if err != nil {
		return nil, VerdictOK, false, err
	}
	g.Changes = changes

	if s.dryRun {
		// 只读预测：拒绝项检查与 Execute 前两步同判据，但不落盘、不改状态
		g.Forecast, err = predictRefusals(s.dir, changes)
		if err != nil {
			return nil, VerdictOK, false, err
		}
		return g, dryRunExit(d.Actions), true, nil
	}

	// 11. 脏检查（§10.2 第 5 步；--force 跳过；两条路都不阻止执行）
	warnIfDirty(s.dir, s.force)

	return g, VerdictOK, false, nil
}

// takeTheirsNote 是交互选择「改用模板版」的动作行尾注（§19.4.3 C4）。
const takeTheirsNote = "交互选择「改用模板版」：本地版本已备份到 .xinit/backup/（§19.4）"

// conflictHeader 是交互表头（§19.4.3 C1）：四行整块，只在可问条数 > 0 时
// 打印一次、紧接在第一个提问之前。首行 %d = 可问条数。
const conflictHeader = "交互式冲突解决（%d 条，§19.4）：逐条选择，回答不被保存（效果即本轮的正常写入与 base 推进）。\n" +
	"  y / yes = 用模板版覆盖本地（模板新内容本轮尚未展示；原内容先备份到 .xinit/backup/，本轮即收敛）。\n" +
	"  其他回答（含直接回车）= 保留本地（与现状一致：模板版写入 .xinit/conflicts/<路径>，本轮退 2）。\n" +
	"  想先查看模板新内容：回答 n 或直接回车——模板版会写入 .xinit/conflicts/<路径>；查看后按报告给出的 --accept 命令收敛，或手工把文件改到与模板一致后重跑。\n"

// askableConflict 报告该冲突路径是否在可问闭集内（§19.4.1）：
// kind ∈ {Merge(判冲突), ConflictBinary, ConflictBothAdded} ∧ theirs 表里有
// 该路径。判据是**键存在性**（不是值非 nil）：空渲染的模板文件（Go template
// 渲染出空内容）同样是一条合法冲突——「改用模板版」就是写一个空文件，与
// buildChanges / advanceState 的 `b, ok := theirsRendered[P]` 同语义。
// 闭集而非「排除 ConflictKind」：将来新增 Kind 默认不提问，需显式加入。
func askableConflict(p string, kind plan.ActionKind, conflicted map[string]bool, theirsRendered map[string][]byte) bool {
	if _, ok := theirsRendered[p]; !ok {
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
func promptConflicts(ps Interactive, actions []plan.Action, conflicted map[string]bool,
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
	fmt.Fprintf(ps.Out(), conflictHeader, len(askable))
	taken := 0
	for i, p := range askable {
		a := findAction(actions, p)
		yes, eof, err := ps.AskConflict(fmt.Sprintf("%s（%s）改用模板版？[y/N] ", p, kindLabel(*a, conflicted[p]))) // C2
		if err != nil {
			return taken, fmt.Errorf("冲突交互读取失败: %w", err)
		}
		if eof {
			fmt.Fprintf(ps.Out(), "输入已结束（EOF）：其余 %d 条冲突按「保留本地」处理（§19.4）。\n", len(askable)-i) // C3
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

// ListConflicts 精确列举 .xinit/conflicts/ 的条目（相对项目根、/ 分隔）。
// 目录不存在 = 没有未解决冲突。
func ListConflicts(projectDir string) ([]string, error) {
	root := filepath.Join(projectDir, state.DirName, "conflicts")
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("列举 conflicts/: %w", err)
	}
	sort.Strings(out)
	return out, nil
}

// step4Unresolved 是 §10.2 第 4 步：conflicts/ 里的条目若本轮仍判为冲突动作
// → 拒绝（必须先解决）。正向判据（F9）：不穷举「已解决」的诸情形；本轮不再
// 判冲突的条目一概放行，其删除由第 8 步的 rebuild 统一做。
func step4Unresolved(existing []string, conflicted map[string]bool) []string {
	var out []string
	for _, p := range existing {
		if conflicted[p] {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// predictRefusals 是 dry-run 的只读预测：经 apply.Forecast 单点，与 Execute
// 的前两步同源（删除的 §9.5、写前检查），只是不落盘——diff 因此能在不改任何
// 东西的前提下报告折叠拒绝与障碍物。
func predictRefusals(projectDir string, c apply.Changes) ([]apply.Refusal, error) {
	return apply.Forecast(projectDir, c)
}

func refusalSet(refs []apply.Refusal) map[string]bool {
	set := make(map[string]bool, len(refs))
	for _, r := range refs {
		set[r.Path] = true
	}
	return set
}

// keysExcept 是 map 的键（排序）减去 drop 集合。
func keysExcept(m map[string][]byte, drop map[string]bool) []string {
	var out []string
	for _, p := range sortedStringKeys(m) {
		if !drop[p] {
			out = append(out, p)
		}
	}
	return out
}

// except 从 xs 里剔除 drop 中的元素（保持输入顺序；输入均已排序）。
func except(xs []string, drop map[string]bool) []string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		if !drop[x] {
			out = append(out, x)
		}
	}
	return out
}

// chmodPredictions 预测 dry-run 下会发生的纯 exec 变更（R7 通道）：
// ExecBits 里不在 Writes、也未被拒的路径。
func chmodPredictions(c apply.Changes, refused map[string]bool) []string {
	var out []string
	for _, p := range sortedStringKeys(c.ExecBits) {
		if _, inWrites := c.Writes[p]; inWrites || refused[p] {
			continue
		}
		out = append(out, p)
	}
	return out
}

// dryRunExit 是 diff/dry-run 的终局语义（§11.5；M1 起返回 Verdict，cli 单点
// 映射为码）：有非 Noop → VerdictChanged（5），否则 VerdictOK（0）。
// 不模拟 2——没有执行，就没有「待解决」的写盘后果要报。
func dryRunExit(actions []plan.Action) Verdict {
	for _, a := range actions {
		if a.Kind != plan.Noop {
			return VerdictChanged
		}
	}
	return VerdictOK
}
