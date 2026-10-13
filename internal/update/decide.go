package update

// 来源：internal/update/update.go 的 Run 块 14-15 + hooksRun 行（M2-P5
// buildDecision 抽取；I-2 实施批 T5/M2b）+ classifyMerges/buildChanges/
// execConclusion 整函数搬家（§2.6 decide.go 行；T6/M2c，体逐字节）。
// 阶段判据（§3-M2）：函数体与源区间逐字对应，差异仅「参数接入」「返回值
// 包装」与附加裁定类（R-M2-3/12）——逐条见 task-5/6-report.md。

import (
	"fmt"

	"github.com/xbcio/xinit/internal/apply"
	"github.com/xbcio/xinit/internal/fsutil"
	"github.com/xbcio/xinit/internal/merge"
	"github.com/xbcio/xinit/internal/plan"
)

// Decision 是本轮决策表结果（P5 建立 → P6 补全/改写 → P7 交互改写
// （Actions 与 Conflicted/Conflicts，I18）→ P9 restoreRefused 改写 Actions 的
// NewBase/Note；§2.3）。
type Decision struct {
	Actions    []plan.Action // 按路径排序（Plan 保证）
	Merged     map[string][]byte
	Conflicted map[string]bool   // 全集合（含 ConflictKind；I11）
	Conflicts  map[string][]byte // ⊆ Conflicted 的内容条目（I21）
	Migrations []MigrationNote   // §17.6（P6 补）
	Acceptable []string          // §10.3（P6 补；I17）
}

// hooksRun（§4.2）：未给 --no-hooks 且清单声明了 post_update。
// R-M2-12：原 Run 内联行（含本注释）抽成函数；Run 侧 bridge 行为
// `hooksRun := hooksRun(s, t)`（RHS 解析包级函数——ShortVarDecl 作用域自
// 声明起）。
func hooksRun(s *session, t *Template) bool { return !s.noHooks && t.Manifest.Hooks.PostUpdate != "" }

// buildDecision 是 P5（§2.2）：plan（块 14）+ merge 分类（块 15）——就地改写
// actions 的元素。rep.Actions/Conflicted/ConflictFiles 依 R-M2-3 留守 Run
// （值经 d）。签名含 s/a 为 §2.2 形状（体内未消费——plan 的 pruned 参数按
// §10.3 口径用 Ours.Prune，绝不用 accept 候选集）。
func buildDecision(s *session, t *Template, b *Base, o *Ours, a *AcceptRewrite, hooksRun bool) (*Decision, error) {
	actions := plan.Plan(b.Metas, o.Snap, t.Snap, o.Prune, hooksRun)

	// 8. merge 分类（§10.2 第 3 步；R3/R19）——就地改写 actions 的元素
	merged, conflicted, conflicts, err := classifyMerges(actions, b, o.Snap, o.Bytes, t.Snap, t.Rendered)
	if err != nil {
		return nil, err
	}
	return &Decision{Actions: actions, Merged: merged, Conflicted: conflicted, Conflicts: conflicts}, nil
}

// classifyMerges 对每个 Merge 动作跑 merge-file 并落定 NewBase（§10.2 第 3
// 步、§8.6、R3/R19）：
//
//	rc 0      → plan.SettleMergeClean（NewBase = theirs[P] + §8.3/§8.5 修整），
//	            合并结果按 ours 的 EOL 还原、写回工作树（进 merged）；
//	rc 1–127  → 占位保持旧值（Plan 的 Merge 占位就是 files[P]，无需还原），
//	            theirs 渲染字节进 conflicts/（§8.6「冲突的 theirs 写到 conflicts/」）；
//	rc ≥128   → 改判 ConflictBinary，同上进 conflicts/。
//
// 四类冲突动作（Merge 冲突、ConflictBinary、ConflictBothAdded、ConflictKind）
// 都进 conflicts/ 内容表；内容 = theirsRendered[P]，**键缺失**时跳过——键缺失
// = 模板没有该路径（如行 6 的 binary 在 theirs 里存在则不会缺；Kind 冲突在
// theirs 里存在）；键在且值为空切片 = 空内容文件（renderTheirs 已把空渲染
// 归一成非 nil 空切片，2026-10-11 M7），照常落 0 字节条目。
func classifyMerges(actions []plan.Action, baseSnap *Base, oursSnap plan.Snap,
	oursBytes map[string][]byte, theirsSnap plan.Snap, theirsRendered map[string][]byte,
) (map[string][]byte, map[string]bool, map[string][]byte, error) {
	merged := map[string][]byte{}
	conflicted := map[string]bool{}
	conflicts := map[string][]byte{}

	for i := range actions {
		switch actions[i].Kind {
		case plan.ConflictBinary, plan.ConflictBothAdded, plan.ConflictKind:
			conflicted[actions[i].Path] = true
			if b := theirsRendered[actions[i].Path]; b != nil {
				conflicts[actions[i].Path] = b
			}
		}
	}

	for i := range actions {
		a := &actions[i]
		if a.Kind != plan.Merge {
			continue
		}
		ob := oursBytes[a.Path]
		bb := baseSnap.Bytes[a.Path]
		tb := theirsRendered[a.Path]
		if ob == nil || bb == nil || tb == nil {
			return nil, nil, nil, fmt.Errorf("内部错误：Merge 路径 %q 缺三方内容", a.Path)
		}
		res, err := merge.ThreeWay(ob, bb, tb)
		if err != nil {
			return nil, nil, nil, err
		}
		switch res.Outcome {
		case merge.Clean:
			plan.SettleMergeClean(a, baseSnap.Metas, oursSnap, theirsSnap)
			merged[a.Path] = fsutil.FromLF(res.Bytes, fsutil.DetectEOL(ob))
		case merge.Conflicts:
			conflicted[a.Path] = true
			conflicts[a.Path] = tb
		case merge.Error:
			a.Kind = plan.ConflictBinary
			conflicted[a.Path] = true
			conflicts[a.Path] = tb
		}
	}
	return merged, conflicted, conflicts, nil
}

// buildChanges 把动作翻译成 apply.Changes（§10.2 第 8 步的输入）。
//
// Writes：WriteTheirs（theirs 渲染字节）与干净 Merge（合并结果，EOL 已还原）；
// Deletes：Delete 行的路径（§9.5 安全由 Execute 先查）；
// ExecBits：§8.3 给结论的路径（R7 前件，见 execConclusion）；
// Conflicts：四类冲突动作的 theirs 字节（重建式，§10.4）。
func buildChanges(base plan.Snap, oursSnap, theirsSnap plan.Snap, actions []plan.Action,
	theirsRendered map[string][]byte, merged, conflicts map[string][]byte,
) (apply.Changes, error) {
	c := apply.Changes{
		Writes:    map[string][]byte{},
		ExecBits:  map[string]bool{},
		Conflicts: map[string][]byte{},
	}
	for _, a := range actions {
		switch a.Kind {
		case plan.WriteTheirs:
			b, ok := theirsRendered[a.Path]
			if !ok {
				return c, fmt.Errorf("内部错误：%q 判为写入但模板没有对应内容", a.Path)
			}
			c.Writes[a.Path] = b
		case plan.Merge:
			// 冲突的 Merge（§8.6 rc 1–127）占位保持旧值：不写工作树，
			// 内容只进 conflicts/（见 classifyMerges）——merged 里没有它
			// 是正常态，不是内部错误。判据取 conflicts 表：Merge 动作被
			// 判冲突时必在其中（tb 非 nil，classifyMerges 已确证）。
			if _, isConflict := conflicts[a.Path]; isConflict {
				break
			}
			b, ok := merged[a.Path]
			if !ok {
				return c, fmt.Errorf("内部错误：%q 判为干净合并但没有合并结果", a.Path)
			}
			c.Writes[a.Path] = b
		case plan.Delete:
			c.Deletes = append(c.Deletes, a.Path)
		}
		if target, ok := execConclusion(a, base, oursSnap, theirsSnap); ok {
			c.ExecBits[a.Path] = target
		}
	}
	for p, b := range conflicts {
		c.Conflicts[p] = b
	}
	return c, nil
}

// execConclusion 报告该路径本轮是否有 §8.3 的 exec 结论（R7 前件）：
//
//	迁移 Merge（MigratedFrom ≠ ""，§17.3）       → 恒有结论（见 migratedExec）；
//	Kind ∉ {Noop, WriteTheirs, Merge}          → 无结论；
//	三方都在 ∧ ours.exec == base.exec          → 有结论，目标 = NewBase.Exec；
//	ours✗ ∧ WriteTheirs（行 13，模板新增）      → 有结论 = theirs 的 exec；
//	其余（§8.3「不动」、base✗ 的行 14/15、行 10/11、冲突行）→ 无结论（磁盘原样）。
//
// 结论与磁盘一致（NewBase.Exec == ours.exec）也不给出——省掉一次无意义的
// chmod 记账（R7 的「NewBase.Exec ≠ 磁盘 exec」条件），dry-run 的预测因此准确。
func execConclusion(a plan.Action, base, ours, theirs plan.Snap) (bool, bool) {
	if a.NewBase == nil {
		return false, false
	}
	// §22.6：无 x 位语义平台（Windows）入口短路——不产 ExecBits/chmod 结论。
	// dry-run 的 chmodPredictions 与真跑 res.Chmodded 同源本函数 ⇒ 两径同口径（均 0）。
	if !fsutil.ExecSupported {
		return false, false
	}
	switch a.Kind {
	case plan.Noop, plan.WriteTheirs, plan.Merge:
	default:
		return false, false
	}
	// §17.3：Q 是磁盘新建文件——必须在 !ook 提前返回之前，且不套用「与磁盘
	// 一致则省略」（无结论即以默认权限落盘、丢 exec 位，N3）。
	if a.Kind == plan.Merge && a.MigratedFrom != "" {
		return migratedExec(a, base, ours), true
	}
	o, ook := ours[a.Path]
	if !ook {
		if a.Kind == plan.WriteTheirs {
			return a.NewBase.Exec, true // 行 13：模板新增，磁盘上没有可保持的 exec
		}
		return false, false // 行 10/11：文件不存在（用户删除），不涉及 exec
	}
	b, bok := base[a.Path]
	if _, tok := theirs[a.Path]; !bok || !tok {
		return false, false // base✗（行 14/15）或 theirs✗：无 §8.3 结论
	}
	if o.Exec != b.Exec {
		return false, false // §8.3 第二分支：本地改过权限 → 不动（每轮重复警告，出口 prune）
	}
	if a.NewBase.Exec == o.Exec {
		return false, false // 与磁盘一致：无需 chmod
	}
	return a.NewBase.Exec, true
}

// derivedFields 是派生字段的单点重算（I17/I18，M3a）：Conflicted/ConflictFiles/
// Acceptable 的唯一表达式出处——report() 主装配、taken 段重算与 P6 的
// d.Acceptable 落值共用。内部序固定：conflicted 先于 acceptable（后者依赖它；
// ConflictFiles 与两者无依赖，居中不变）。
func derivedFields(d *Decision) (conflicted, conflictFiles, acceptable []string) {
	conflicted = sortedStringKeys(d.Conflicted)
	conflictFiles = sortedStringKeys(d.Conflicts)
	acceptable = acceptableConflicts(conflicted, d.Actions)
	return conflicted, conflictFiles, acceptable
}
