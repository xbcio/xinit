package update

// 来源：internal/cli/update.go 行 92-1283（迁移集：runUpdate + 全部消费方辅助
// 函数闭包；I-2 实施批 T2/M1）。本文件头部承载 Input/IO/Mode/Verdict 接缝
// 声明（§2.3、§2.6 M2 布局：update.go = 包文档/Input/IO/Mode/Verdict/Run）；
// Interactive 已随 P0 迁 session.go、模板侧 helper 随 P1 迁 template.go、
// base 装载随 P2 阶段化、ours 快照随 P3 迁 ours.go（M2a/T4）；accept 预检随
// P4/P6 迁 accept.go、plan/merge 分类随 P5 迁 decide.go、gate 随 P7 迁
// gate.go（M2b/T5）。M2 拆阶段进行中：块 1-23 已抽，块 24-32 仍逐字留在 Run
// （bridge 兜住引用，R-M2-2）。

import (
	"fmt"
	"io"

	"github.com/xbcio/xinit/internal/apply"
	"github.com/xbcio/xinit/internal/fsutil"
	"github.com/xbcio/xinit/internal/merge"
	"github.com/xbcio/xinit/internal/plan"
)

// Mode 是管线入口模式（§2.4：报告头等可观测字符串的单一来源）。
type Mode int

const (
	ModeUpdate Mode = iota // xinit update
	ModeDiff               // xinit diff（只读镜像；DryRun 恒为真）
)

// Verdict 是终局语义（§2.3），cli 用一张 5 行表映射为 §11.5 数字。
type Verdict int

const (
	VerdictOK        Verdict = iota // → 0
	VerdictPending                  // → 2（冲突/折叠拒绝/障碍物）
	VerdictHookFail                 // → 3
	VerdictNeedTrust                // → 4（零写）
	VerdictChanged                  // → 5（dry-run 预测有非 Noop）
)

// verdictOf 是终局语义的单点（§11.5、§2.3、I27：优先级 3 ＞ 2 ＞ 0；M1 起
// 只产 Verdict，cli 单点映射为码）：冲突/折叠拒绝/障碍物待解决 → 2；
// 钩子失败 → 3（本轮没有完整做完；2 的状态已持久化，下一轮照旧会报）。
func verdictOf(pending, hookFailed bool) Verdict {
	if hookFailed {
		return VerdictHookFail
	}
	if pending {
		return VerdictPending
	}
	return VerdictOK
}

// IO 是管线输出通道组（§2.6 update.go 行；M2 期间无消费者——M4 接
// Input.IO。P0 的 session.io 初始化 = os.Stdout/os.Stderr 对）。
type IO struct {
	Out io.Writer
	Err io.Writer
}

// Input 是 update 的全部输入（cli 侧由 flag 经 inputOf 映射，§2.7 #3）。
// M1 只落「搬运 + 编译必需的最小接缝」：IO/Now 留 M4（§2.4 交互行）。
type Input struct {
	ProjectDir    string
	Revision      string
	ConfigPath    string
	Fetch         bool
	Force         bool
	DryRun        bool
	NoHooks       bool
	Yes           bool
	Accept        []string
	Mode          Mode        // ModeDiff = diff（报告头派生；M1 起不再由 cli 覆盖 Header）
	Interactive   Interactive // nil = 不可交互（cli 侧 interactiveSession 判定）
	EngineVersion string      // §2.7 #13：cli 为唯一真源，经此注入（M2 落 session）
}

// Run 是 §10.2 的十二步本体（不打印；diff 与 cli 测试复用它）。
// 返回的报告总是非 nil；退出码见 §11.5。
// Run 是 §10.2 的十二步本体（不打印；diff 与 cli 测试复用它）。
// 返回的报告总是非 nil；退出码见 §11.5。
func Run(in Input) (*Report, error) {
	s, err := prepare(in)
	if err != nil {
		return nil, err
	}
	defer s.close() // 解锁（I29）；dry-run 下 lock = nil 为 no-op

	t, err := loadTemplate(s, in.Revision, in.ConfigPath, in.Fetch)
	if err != nil {
		return nil, err
	}
	b, err := loadBase(s)
	if err != nil {
		return nil, err
	}
	o, err := snapshotOurs(s, t, b)
	if err != nil {
		return nil, err
	}
	a, err := rewriteAccept(s, b, o, in.Accept)
	if err != nil {
		return nil, err
	}
	d, err := buildDecision(s, t, b, o, a, hooksRun(s, t))
	if err != nil {
		return nil, err
	}
	if err := refineDecision(s, t, b, o, d, a); err != nil {
		return nil, err
	}
	g, v, done, err := gate(s, t, b, o, d, a)
	if err != nil {
		return nil, err
	}
	if done {
		return report(s, t, b, o, d, g, nil, v, nil), nil // 终局①：pending / dry-run
	}
	if done, err := trust(s, t, in.NoHooks, in.Yes); err != nil {
		return nil, err
	} else if done {
		return report(s, t, b, o, d, g, nil, VerdictNeedTrust, nil), nil // 终局②：rc4（零写）
	}
	w, err := deliver(s, b, o, d, g)
	if err != nil {
		return nil, err
	}
	if err := advance(s, t, b, d, w); err != nil {
		return nil, err
	}
	h, err := runHook(s, t, d.Actions, hooksRun(s, t))
	if err != nil {
		return nil, err
	}
	return report(s, t, b, o, d, g, w, verdictOf(w.Pending, h.Failed), h), nil // 终局③
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

// joinNote 把追加文案并入 Note（与 plan 内部的 noteJoin 同形；Note 是
// 用户可见文案，不做去重）。
func joinNote(note, extra string) string {
	if note == "" {
		return extra
	}
	return note + "；" + extra
}

// acceptDetachedNote 是 --accept 对「模板已删除该路径」的覆盖文案（§10.3）。
const acceptDetachedNote = "已按 --accept 声明解决：模板已删除该路径，本轮脱离管理（不再有 base 记录；磁盘不动）"
