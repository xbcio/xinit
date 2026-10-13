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
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/xbcio/xinit/internal/apply"
	"github.com/xbcio/xinit/internal/fsutil"
	"github.com/xbcio/xinit/internal/hooks"
	"github.com/xbcio/xinit/internal/merge"
	"github.com/xbcio/xinit/internal/plan"
	"github.com/xbcio/xinit/internal/render"
	"github.com/xbcio/xinit/internal/renderstore"
	"github.com/xbcio/xinit/internal/state"
	"github.com/xbcio/xinit/internal/template"
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

// headerFor 派生报告头（可观测字符串，逐字不变）：diff 恒 "xinit diff"；
// update 的 dry-run 轮带 --dry-run 后缀。cli 侧不再事后覆盖 Header
// （§2.4 报告归属段，评审 IM-5）。
func headerFor(mode Mode, dryRun bool) string {
	if mode == ModeDiff {
		return "xinit diff"
	}
	if dryRun {
		return "xinit update --dry-run"
	}
	return "xinit update"
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
func Run(in Input) (*Report, error) {
	s, err := prepare(in)
	if err != nil {
		return nil, err
	}
	defer s.close() // 解锁（I29）；dry-run 下 lock = nil 为 no-op

	rep := &Report{Header: headerFor(in.Mode, in.DryRun)}
	rep.DryRun = in.DryRun
	rep.Source = s.st.Template.Source
	rep.OldRevision = s.st.Template.Revision
	rep.OldCommit = s.st.Template.Commit

	t, err := loadTemplate(s, in.Revision, in.ConfigPath, in.Fetch)
	if err != nil {
		return nil, err
	}
	rep.NewRevision = t.Src.Revision
	rep.NewCommit = t.Src.Commit

	b, err := loadBase(s)
	if err != nil {
		return nil, err
	}
	rep.Degraded = b.Degraded
	rep.Restored = b.Restored

	o, err := snapshotOurs(s, t, b)
	if err != nil {
		return nil, err
	}

	// M2 中间态桥接（R-M2-2）：后半阶段逐步抽取期间保持块 13-32 逐字不变；
	// M2c 终局化时消除。
	projectDir, st, cacheRoot := s.dir, s.st, s.cacheRoot
	src, m, vars, theirsRendered, newSrc, theirsSnap := t.Src, t.Manifest, t.Vars, t.Rendered, t.SrcOf, t.Snap
	baseSnap := b // 别名引用：后半（块 14-32）的就地改写语义不变——baseSnap 与 b 是同一对象
	oursSnap, oursBytes := o.Snap, o.Bytes

	a, err := rewriteAccept(s, b, o, in.Accept)
	if err != nil {
		return nil, err
	}

	d, err := buildDecision(s, t, b, o, a, hooksRun(s, t))
	if err != nil {
		return nil, err
	}
	rep.Actions = d.Actions
	rep.Conflicted = sortedStringKeys(d.Conflicted)
	// conflicts/ 内容条目 ⊆ 冲突全集（ConflictKind 且 theirs 缺失时无条目）：
	// 报告据此决定是否打印「参考：conflicts/」行（评审 E1/m-3）。实跑写入的
	// 条目与 c.Conflicts 一致（apply.rebuildConflicts 全量重建），无需另取。
	rep.ConflictFiles = sortedStringKeys(d.Conflicts)
	actions, merged, conflicted := d.Actions, d.Merged, d.Conflicted
	hooksRun := hooksRun(s, t)

	if err := refineDecision(s, t, b, o, d, a); err != nil {
		return nil, err
	}
	rep.Migrations = d.Migrations
	rep.Acceptable = d.Acceptable

	g, v, done, err := gate(s, t, b, o, d, a)
	if err != nil {
		return nil, err
	}
	rep.Blocked = g.Blocked // 原第 4 步两行赋值折叠一行（值等价；D2 先例）
	if g.Taken > 0 {
		// 派生字段与两个 map 同步：按与创建时逐字相同的表达式重算
		// （不逐字段手工增删，避免静默不一致）。
		rep.Conflicted = sortedStringKeys(d.Conflicted)
		rep.ConflictFiles = sortedStringKeys(d.Conflicts)
		rep.Acceptable = acceptableConflicts(rep.Conflicted, d.Actions)
	}
	if done { // 终局①：pending / dry-run
		if v == VerdictPending {
			// 早退轮同样「版本记录不推进」（第 9 步的推进根本不会走到）：报告
			// 顶部的版本行必须跟 AR 注解，否则与下方待解决轮的报告自相矛盾。
			rep.Pending = true
			rep.Verdict = v
			return rep, nil
		}
		refused := refusalSet(g.Forecast)
		rep.Refused = g.Forecast
		rep.Written = keysExcept(g.Changes.Writes, refused)
		rep.Deleted = except(g.Changes.Deletes, refused)
		rep.Chmodded = chmodPredictions(g.Changes, refused)
		rep.Merged = sortedStringKeys(d.Merged)
		rep.Overwritten = overwrittenPaths(d.Actions, refused)
		rep.Resolved = except(g.Existing, d.Conflicted)
		rep.Verdict = v
		return rep, nil
	}
	changes := g.Changes // 桥接（R-M2-2）：块 24-32 消费

	if done, err := trust(s, t, in.NoHooks, in.Yes); err != nil {
		return nil, err
	} else if done {
		rep.Verdict = VerdictNeedTrust // 原 :217（rep 留守）
		return rep, nil                // 原 :218
	}

	// 13. 备份（§10.2 第 7 步、§10.1）：计划内会被写入或删除的路径；目标以
	// oursSnap（Scan，不穿透软链）为准过滤——中间段是软链的路径（将被
	// CheckWrites 拒绝）不得进备份，否则 Backup 的穿透 Lstat 会错位备份
	// 链后文件（裁定 U/Z）。
	backupDir, err := apply.Backup(projectDir, time.Now().Format("20060102-150405"), backupTargets(changes, oursSnap))
	if err != nil {
		return nil, fmt.Errorf("备份: %w", err)
	}
	rep.BackupDir = backupDir

	// 13b. 行 5 覆盖保护（§8.2 :614）：本轮会被 generated 覆盖的路径，其
	// 本地版本先另存 .xinit/overwritten/<P>——持久保留，不随 update 清理
	//（同一路径再次被覆盖时更新为最近一次副本）。
	//
	// 先取「写前检查的拒绝集」再落盘：被折叠拒绝/障碍物挡下的路径**没有被
	// 覆盖**，§8.2 的另存义务只在真的发生覆盖时触发——既不写副本、也不进
	// 报告。拒绝集原样来自 predictRefusals（与 Execute 步骤 1-2 同一组函数、
	// 同一 changes），绝不手写近似判据：手写判据会开出「Execute 会写、却没
	// 另存」的窗口。
	if slices.ContainsFunc(actions, func(a plan.Action) bool { return a.OverwriteGenerated }) {
		refusals, err := predictRefusals(projectDir, changes)
		if err != nil {
			return nil, err
		}
		for _, p := range overwrittenPaths(actions, refusalSet(refusals)) {
			data := oursBytes[p]
			if data == nil {
				return nil, fmt.Errorf("内部错误：%q 判为 generated 覆盖但本地内容缺失", p)
			}
			mode := os.FileMode(0o644)
			if m, ok := oursSnap[p]; ok && m.Exec {
				mode = 0o755
			}
			full := filepath.Join(projectDir, state.DirName, "overwritten", filepath.FromSlash(p))
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return nil, fmt.Errorf("另存被覆盖的本地版本: %w", err)
			}
			if err := state.WriteFileAtomic(full, data, mode); err != nil {
				return nil, fmt.Errorf("另存被覆盖的本地版本: %w", err)
			}
			rep.Overwritten = append(rep.Overwritten, p)
		}
	}

	// 14. 执行（§10.2 第 8 步）——折叠拒绝/障碍物路径跳过、不中止其余路径
	res, err := apply.Execute(projectDir, changes)
	if err != nil {
		return nil, err
	}
	rep.Written = res.Written
	rep.Deleted = res.Deleted
	rep.Chmodded = res.Chmodded
	// rep.Conflicted 保持第 8 步的冲突全集（不覆盖为 res.Conflicts：后者只含
	// 已写盘条目，会把 ConflictKind 且 theirs 缺失的冲突漏报成「冲突 0」——
	// 评审 E1。conflicts/ 条目由 rep.ConflictFiles 单独承载）。
	rep.Resolved = res.Resolved
	rep.Refused = res.Refusals
	rep.Merged = sortedStringKeys(merged)

	// 15. 被拒路径把 NewBase 还原为「本轮开始时的 base 条目」（§8.5
	//     折叠拒绝行）——必须在第 9 步之前，否则被拒路径的 base 会被推进。
	actions = restoreRefused(actions, baseSnap.Metas, refusalSet(res.Refusals))

	// 本轮是否仍有待解决项（冲突/折叠拒绝/障碍物）——决定第 9 步是否推进
	// 模板版本与 commit，也决定第 19 步的退出码（§11.5）。冲突用第 8 步全集
	// （评审 E1：res.Conflicts 只有已写盘条目，ConflictKind 且 theirs 缺失时
	// 会漏算 → 退 0 且版本照推，绕开 D3 门禁）。
	pending := len(res.Refusals) > 0 || len(conflicted) > 0
	rep.Pending = pending

	// 16. 第 9 步：推进 state 与 .xinit/base/（先 base 后 state，含写入自检）
	if err := advanceState(projectDir, st, baseSnap, actions, theirsRendered, newSrc, vars, src, pending, cacheRoot, in.EngineVersion); err != nil {
		return nil, err
	}

	// 17. 第 10 步：幂等写 .git/info/exclude
	if err := state.EnsureGitExcluded(projectDir); err != nil {
		return nil, fmt.Errorf("写 .git/info/exclude: %w", err)
	}

	// 18. post_update（§10.2 第 11 步）——在 state/base 落盘之后。文件已按
	// 本轮结果写完，钩子失败也只是退出码 3（文件与 state 保留，§10.2 :845）。
	if hooksRun {
		rep.HookName = "post_update"
		rep.HookBlock = m.Hooks.PostUpdate
		before, err := hooks.Snapshot(projectDir)
		if err != nil {
			return nil, fmt.Errorf("钩子前快照: %w", err)
		}
		res, err := hooks.Run(projectDir, m.Hooks.PostUpdate)
		if err != nil {
			return nil, err // sh 起不来：硬错误（§3.1 明确报错）
		}
		after, err := hooks.Snapshot(projectDir)
		if err != nil {
			return nil, fmt.Errorf("钩子后快照: %w", err)
		}
		rep.HookExitCode = res.ExitCode
		rep.HookOutput = hookBlockLines(string(res.Output))
		// planned = 本轮受管并集（Actions 全部路径；prune 与行 12 已被 Plan
		// 排除）；generated 取本轮模板侧位（M3-R6）。
		planned := make(map[string]bool, len(rep.Actions))
		for _, a := range rep.Actions {
			planned[a.Path] = true
		}
		generated := make(map[string]bool)
		for p, m := range theirsSnap {
			if m.Generated {
				generated[p] = true
			}
		}
		rep.HookUnplanned, rep.HookModified = hooks.Classify(before, after, planned, generated)
		if res.ExitCode != 0 {
			rep.HookFailed = true
		}
	}

	// 19. 终局语义（§11.5；M3-R2 优先级 3 ＞ 2 ＞ 0；M1 起只产 Verdict，
	// cli 单点映射为码）：冲突/折叠拒绝/障碍物待解决 → 2；钩子失败 → 3
	//（本轮没有完整做完；2 的状态已持久化，下一轮照旧会报）。
	rep.Verdict = VerdictOK
	if pending {
		rep.Verdict = VerdictPending
	}
	if rep.HookFailed {
		rep.Verdict = VerdictHookFail
	}
	return rep, nil
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

// warnIfDirty 是 §10.2 第 5 步：git 仓脏 → 警告；非 git 仓 → 警告后继续。
// --force 跳过检查（§11.2）。两条路都不阻止执行。
func warnIfDirty(projectDir string, force bool) {
	if force {
		return
	}
	out, err := exec.Command("git", "-C", projectDir, "status", "--porcelain").Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, "xinit: 警告: 当前目录不是 git 仓库：出问题只能靠 .xinit/backup/ 还原")
		return
	}
	if s := strings.TrimSpace(string(out)); s != "" {
		n := len(strings.Split(s, "\n"))
		fmt.Fprintf(os.Stderr,
			"xinit: 警告: 工作树有 %d 处未提交改动；update 会直接改写工作树，建议先提交（或用 --force 跳过检查）\n", n)
	}
}

// backupTargets 是 §10.1 的备份对象：计划内会被写入或删除、且 ours 侧确实是
// 常规文件的路径（含纯 exec 变更；删除也记入备份）。
//
// 过滤依据是 worktree.Scan 的快照（不穿透符号链接），而不是让 apply.Backup
// 的 os.Lstat 判定（穿透语义）：中间段是软链的路径在 ours 侧「不存在」，
// 穿透的 Lstat 却会看到链后的文件——错位备份（裁定 U/Z）。
// ours 不存在 → 无原内容可备份；目录/符号链接 → 本轮不会被改动。
func backupTargets(c apply.Changes, oursSnap plan.Snap) []string {
	targets := make([]string, 0, len(c.Writes)+len(c.Deletes)+len(c.ExecBits))
	add := func(p string) {
		if m, ok := oursSnap[p]; ok && m.Kind == render.KindRegular {
			targets = append(targets, p)
		}
	}
	for _, p := range sortedStringKeys(c.Writes) {
		add(p)
	}
	for _, p := range c.Deletes {
		add(p)
	}
	for _, p := range sortedStringKeys(c.ExecBits) {
		if _, inWrites := c.Writes[p]; !inWrites {
			add(p)
		}
	}
	sort.Strings(targets)
	return targets
}

// restoreRefused 把被拒路径（折叠拒绝、障碍物、删除路径逃逸）的 NewBase
// 还原为「本轮开始时的 base 条目」（§8.5 折叠拒绝行）——base✗ → nil
// （与行 15 的「保持旧值 = nil」同形）。必须在第 9 步之前调用。
func restoreRefused(actions []plan.Action, baseMeta plan.Snap, refused map[string]bool) []plan.Action {
	for i := range actions {
		a := &actions[i]
		if !refused[a.Path] {
			continue
		}
		if old, ok := baseMeta[a.Path]; ok {
			m := old
			a.NewBase = &m
		} else {
			a.NewBase = nil
		}
		a.Note = joinNote(a.Note, "本轮被拒绝（折叠/障碍物）：未落盘，保持旧 base，下一轮重新判定")
	}
	return actions
}

// joinNote 把追加文案并入 Note（与 plan 内部的 noteJoin 同形；Note 是
// 用户可见文案，不做去重）。
func joinNote(note, extra string) string {
	if note == "" {
		return extra
	}
	return note + "；" + extra
}

// advanceState 按 §10.2 第 9 步推进 state.json 与 .xinit/base/。
//
// 顺序固定：先写 base 文件（SyncBase 全量同步 + 清残片）→ 写入自检
// （不变量 5：落盘字节的 hash == 记账的 hash）→ 最后写 state.json。
//
// 新 files/base 只从 Action 构建：NewBase == nil 与「不在 Action 里的旧键」
// （prune §8.0、行 12，R5）都自动移出；对应的 base/<Dst> 残片由 SyncBase 的
// removeStale 清掉。「旧值」一律指本轮开始时的 files[P]（§10.2 第 9 步）。
//
// pending（本轮仍以退出码 2 结束：有冲突或折叠拒绝/障碍物）时**不推进
// 模板 revision/commit**：冲突与被拒路径的 base 保持旧版本，而 §5.2 的
// 逐路径恢复要用记录的 commit 重渲染这些路径——提前推进 commit 会让它们
// 的重建 hash 必然不符 → 降级成 base✗。收敛后的重跑再推进（F6 语义保证
// 「没有更高的 tag 时取当前 revision」，重跑不会空转）。
func advanceState(projectDir string, st *state.State, baseSnap *Base,
	actions []plan.Action, theirsRendered map[string][]byte,
	newSrc map[string]string, vars render.Vars, src *template.Source, pending bool,
	cacheRoot, engineVersion string,
) error {
	newFiles := make(map[string]state.FileState, len(actions))
	entries := make([]render.Entry, 0, len(actions))
	blobs := make(map[string][]byte, len(actions))

	for _, a := range actions {
		if a.NewBase == nil {
			continue // 移除（行 8/12/16、prune）
		}
		old, hadOld := st.Files[a.Path]
		oldMeta, hadMeta := baseSnap.Metas[a.Path]
		advanced := !hadMeta || *a.NewBase != oldMeta // 推进到 theirs（含位被 settle 调整的行）

		var content []byte
		var srcPath string
		switch {
		case advanced:
			b, ok := theirsRendered[a.Path]
			if !ok {
				return fmt.Errorf("内部错误：%q 推进 base 但模板没有对应内容", a.Path)
			}
			content = b
			if srcPath = newSrc[a.Path]; srcPath == "" {
				return fmt.Errorf("内部错误：%q 推进 base 但拿不到模板原路径", a.Path)
			}
		default:
			b, ok := baseSnap.Bytes[a.Path]
			if !ok {
				return fmt.Errorf("内部错误：%q 保持旧 base 但旧 base 内容缺失", a.Path)
			}
			if !hadOld {
				// base✗（BothAdded）的 --accept 干净路径：base′ 由 7b 重写为
				// conflicts/<P> 内容，files 条目缺失——Src 取本轮模板映射
				//（M9 C-1；修复前此分支报「内部错误」、accept 不可用）。
				if srcPath = newSrc[a.Path]; srcPath == "" {
					return fmt.Errorf("内部错误：%q 保持旧 base 但 files 里没有它（且拿不到模板原路径）", a.Path)
				}
			} else {
				srcPath = old.Src
			}
			content = b
		}

		newFiles[a.Path] = state.FileState{
			Src:       srcPath,
			Hash:      a.NewBase.Hash,
			Exec:      a.NewBase.Exec,
			Binary:    a.NewBase.Binary,
			MixedEOL:  a.NewBase.MixedEOL,
			Generated: a.NewBase.Generated,
		}
		entries = append(entries, render.Entry{
			Src:       srcPath,
			Dst:       a.Path,
			Kind:      a.NewBase.Kind,
			Exec:      a.NewBase.Exec,
			Binary:    a.NewBase.Binary,
			Generated: a.NewBase.Generated,
		})
		blobs[a.Path] = content
	}

	if err := state.SyncBase(projectDir, entries, blobs); err != nil {
		return fmt.Errorf("同步 base: %w", err)
	}

	// 写入自检（不变量 5 / §7.6）：落盘字节的 hash 必须等于记账的 hash。
	for p, fst := range newFiles {
		data, err := state.ReadBase(projectDir, p)
		if err != nil {
			return err
		}
		if got := fsutil.ContentHash(data); got != fst.Hash {
			return fmt.Errorf("写入自检失败：base/%s 落盘 hash %s ≠ 记账 %s（中止）", p, got, fst.Hash)
		}
	}

	// vars：解析出的覆盖冻结值；清单里已删除的变量保留旧值（§3.4）
	if st.Vars == nil {
		st.Vars = map[string]string{}
	}
	for k, v := range vars {
		st.Vars[k] = v
	}

	st.Files = newFiles
	st.EngineVersion = engineVersion
	// Source 不变：openTarget 就是从 st.Template.Source 打开的
	//（template.Source 不携带来源字符串，报告侧用 st.Template.Source）。
	st.Template.Kind = src.Kind.String()
	if !pending {
		// 只有本轮全清（无冲突、无折叠拒绝/障碍物）才推进模板版本与
		// commit——未收敛时保持旧值，理由见函数注释。
		st.Template.Revision = src.Revision
		st.Template.Commit = src.Commit
	}
	if err := st.Save(projectDir); err != nil {
		return err
	}

	// 快照写侧挂点（§21.5）：必须在 st.Save 成功之后（键的 vars 分量 =
	// 停盘值）；pending 轮 commit 不推进，写了 anchor 由 st.Template.Commit
	// 派生即等于它，tree 装的却是本轮（新目标 commit）渲染原文——会把当下
	// 唯一对该 anchor 有效的条目覆写成与旧 commit 原文不一致的内容（旧原文
	// 从此不可恢复），故跳过。字节源 = theirsRendered（全量渲染原文；不得取
	// blobs 混合体——保持旧值路径装的是旧 base 字节）。best-effort：失败只
	// 警告，不影响命令。
	if !pending {
		renderstore.WriteBestEffort(st, cacheRoot, theirsRendered, os.Stderr)
	}
	return nil
}

// acceptDetachedNote 是 --accept 对「模板已删除该路径」的覆盖文案（§10.3）。
const acceptDetachedNote = "已按 --accept 声明解决：模板已删除该路径，本轮脱离管理（不再有 base 记录；磁盘不动）"

// overwrittenPaths 是本轮「会被 generated 覆盖（§8.2 行 5）、且未被写前检查
// 拒绝」的路径（排序）——13b 的落盘清单与 dry-run 的预测共用同一出处。
//
// refused 必须原样来自 predictRefusals（与 apply.Execute 步骤 1-2 同一组
// 函数）：被拒的路径没有被覆盖，另存义务不触发；换成手写近似判据会同时开出
// 「Execute 写而没另存」（漏档）与「没写而记档」（虚账）两个窗口。
func overwrittenPaths(actions []plan.Action, refused map[string]bool) []string {
	var out []string
	for _, a := range actions {
		if a.OverwriteGenerated && !refused[a.Path] {
			out = append(out, a.Path)
		}
	}
	sort.Strings(out)
	return out
}
