package cli

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/xbcio/xinit/internal/apply"
	"github.com/xbcio/xinit/internal/fsutil"
	"github.com/xbcio/xinit/internal/hooks"
	"github.com/xbcio/xinit/internal/manifest"
	"github.com/xbcio/xinit/internal/merge"
	"github.com/xbcio/xinit/internal/plan"
	"github.com/xbcio/xinit/internal/render"
	"github.com/xbcio/xinit/internal/state"
	"github.com/xbcio/xinit/internal/template"
	"github.com/xbcio/xinit/internal/worktree"
)

// UpdateOptions 是 update 的全部输入。R13：作用于 cwd（无位置参数）。
type UpdateOptions struct {
	ProjectDir string
	Revision   string
	ConfigPath string
	Fetch      bool
	Force      bool
	DryRun     bool
	NoHooks    bool
	Yes        bool
	Accept     []string // --accept <路径>...：声明这些冲突已解决（§10.3）
}

func newUpdateCmd() *cobra.Command {
	var o UpdateOptions
	cmd := &cobra.Command{
		Use:   "update",
		Short: "把派生项目同步到模板的新版本",
		Long: "按模板的 xinit.yml 与 .xinit/ 基线（base）做三方比较，把模板变更同步到当前目录。\n\n" +
			"冲突路径写入 .xinit/conflicts/，解决后重跑即收敛；\n" +
			"计划内路径的原文备份在 .xinit/backup/<时间戳>/。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			o.ProjectDir = "."
			return Update(o)
		},
	}
	cmd.Flags().StringVar(&o.Revision, "revision", "", "目标版本（默认取模板最新的 semver tag）")
	cmd.Flags().StringVar(&o.ConfigPath, "config", "", "变量文件（扁平 YAML；补齐新增或失效的 vars）")
	cmd.Flags().BoolVar(&o.Fetch, "fetch", false, "联网更新模板镜像缓存")
	cmd.Flags().BoolVar(&o.Force, "force", false, "跳过脏工作树检查")
	cmd.Flags().BoolVar(&o.NoHooks, "no-hooks", false, "跳过 post_update 执行（仍记录本次内容，下次带钩子运行仍需确认）")
	cmd.Flags().BoolVar(&o.Yes, "yes", false, "跳过钩子信任确认（等同于在交互终端回答 y）")
	cmd.Flags().BoolVar(&o.DryRun, "dry-run", false, "只输出动作列表，不落盘、不改状态")
	cmd.Flags().StringArrayVar(&o.Accept, "accept", nil,
		"声明这些冲突路径已手工解决：把 .xinit/conflicts/<路径> 的内容记为 base（工具无法验证，§10.3）")
	return cmd
}

// Update 执行一次 update，打印报告，并把非零退出码包进 ExitError。
func Update(o UpdateOptions) error {
	rep, err := runUpdate(o)
	if err != nil {
		return err
	}
	if rep.ExitCode == CodeNeedTrust {
		// 信任通知已由第 12 步直接打印；本轮没有执行任何动作，不打印动作报告。
		return &ExitError{Code: CodeNeedTrust, Silent: true, msg: "update 需要信任确认（exit 4）"}
	}
	rep.Print(os.Stdout)
	if rep.ExitCode != CodeOK {
		return &ExitError{
			Code:   rep.ExitCode,
			Silent: true, // 报告已写全，main 不再补行
			msg:    fmt.Sprintf("update 以退出码 %d 结束", rep.ExitCode),
		}
	}
	return nil
}

// runUpdate 是 §10.2 的十二步本体（不打印；Task 13 的 diff 复用它）。
// 返回的报告总是非 nil；退出码见 §11.5。
func runUpdate(o UpdateOptions) (*UpdateReport, error) {
	if len(o.Accept) > 0 && o.DryRun {
		return nil, fmt.Errorf("--accept 与 --dry-run 不能组合：accept 的本义是修改状态（§10.3）")
	}
	projectDir, err := filepath.Abs(o.ProjectDir)
	if err != nil {
		return nil, err
	}
	rep := &UpdateReport{Header: "xinit update"}
	if o.DryRun {
		rep.Header = "xinit update --dry-run"
		rep.DryRun = true
	}

	// 先 Load 再取锁：Load 失败（不是 xinit 项目）时不该凭空创建 .xinit/。
	st, err := state.Load(projectDir)
	if err != nil {
		return nil, err
	}
	rep.Source = st.Template.Source
	rep.OldRevision = st.Template.Revision
	rep.OldCommit = st.Template.Commit

	// 写前安全检查（§5.5，与 init 同一判据）。
	if tracked, err := state.IsGitTracked(projectDir, state.DirName); err != nil {
		return nil, fmt.Errorf("无法确认 %s/ 是否被 git 跟踪: %w", state.DirName, err)
	} else if tracked {
		return nil, fmt.Errorf(
			"%s/ 已被 git 跟踪：拒绝写入被版本控制的 %s/（规范 §5.5）；请执行 `git rm -r --cached %s` 后重试",
			state.DirName, state.DirName, state.DirName)
	}

	// 1. 项目锁（R12：--dry-run 只读，不加锁——连 lock 文件都不创建）
	if !o.DryRun {
		lock, err := state.LockProject(projectDir)
		if err != nil {
			return nil, err
		}
		defer lock.Unlock()
	}

	cacheRoot, err := template.DefaultCacheRoot()
	if err != nil {
		return nil, err
	}

	// 2. 打开模板 + 选定目标版本（§11.3 / R8）
	src, err := openTarget(st, o.Revision, cacheRoot, o.Fetch)
	if err != nil {
		return nil, err
	}
	rep.NewRevision = src.Revision
	rep.NewCommit = src.Commit

	// 3. 清单 + vars（R15）
	m, compiled, vars, err := loadManifestVars(src, st, o.ConfigPath)
	if err != nil {
		return nil, err
	}
	// dir 源的「版本」= 树 hash（与 init 同一算法：清单编译后、排除 exclude 命中）
	if src.Kind == template.KindDir {
		if src.Commit, err = template.TreeHashOf(src.DirRoot, compiled.Exclude); err != nil {
			return nil, err
		}
		rep.NewCommit = src.Commit
	}

	// 4. 渲染 theirs（模板侧；告警与 init 同格式）
	entries, theirsRendered, newSrc, err := renderTheirs(src, compiled, vars)
	if err != nil {
		return nil, err
	}
	theirsSnap := snapFromRendered(entries, theirsRendered)

	// 5. base（§5.2 逐路径恢复；R9 重渲染用 st.Vars、R10 全量校验）
	// dry-run/diff 只读：恢复照常进行但只进内存快照，不写回 .xinit/base/
	// （零写契约，评审 I-1——恢复写回是 dry-run 路径上唯一的项目内写盘点）。
	baseSnap, err := loadBase(projectDir, st, cacheRoot, o.DryRun)
	if err != nil {
		return nil, err
	}
	rep.Degraded = baseSnap.Degraded

	// 6. ours 快照（R18：只查 base ∪ theirs 的并集——与 Plan 的并集一致）
	union := unionKeys(baseSnap.Metas, theirsSnap)
	oursSnap, oursBytes, err := worktree.Scan(projectDir, union)
	if err != nil {
		return nil, err
	}

	// 7. prune（R4）+ plan（R2）
	pruned, err := expandPrune(projectDir, union)
	if err != nil {
		return nil, err
	}
	// 7b. --accept 预检与 base 重写（§10.3，在算 plan 之前）：把 P 的 base
	// 快照条目换成 .xinit/conflicts/<P> 的内容——三方比较的 base 从此是
	// 「用户上次看到并认可的那份」，而不是本次运行的 theirs（第五轮 D2）。
	type acceptOld struct {
		meta  plan.FileMeta
		bytes []byte
		had   bool // base 快照里原有该条目（Metas/Bytes 同源）
	}
	acceptOlds := map[string]acceptOld{}
	acceptSet := map[string]bool{}
	for _, p := range o.Accept {
		if acceptSet[p] {
			continue
		}
		if p == "" || path.IsAbs(p) || path.Clean(p) != p || p == ".." || strings.HasPrefix(p, "../") {
			return nil, fmt.Errorf("--accept %s：必须是 .xinit/conflicts/ 下的规范相对路径（不接受空串、绝对路径、.. 段或未规范化写法，§10.3）", p)
		}
		if pruned[p] {
			return nil, fmt.Errorf("--accept %s：该路径已在 .xinit/prune.txt 中，已脱离管理、无需 accept（§10.3）", p)
		}
		content, err := os.ReadFile(filepath.Join(projectDir, state.DirName, "conflicts", filepath.FromSlash(p)))
		if err != nil {
			return nil, fmt.Errorf("--accept %s：.xinit/conflicts/%s 不存在或不可读——--accept 只能声明「已解决的冲突」（§10.3）：%v", p, p, err)
		}
		old := acceptOld{bytes: baseSnap.Bytes[p]}
		if m, ok := baseSnap.Metas[p]; ok {
			old.meta, old.had = m, true
		}
		acceptOlds[p] = old
		acceptSet[p] = true
		binary := fsutil.IsBinary(content)
		exec := false
		if old.had {
			exec = old.meta.Exec
		} else if m, ok := oursSnap[p]; ok {
			exec = m.Exec // 第五轮 F8：base✗ 取 ours.exec
		}
		baseSnap.Metas[p] = plan.FileMeta{
			Kind:      render.KindRegular,
			Exec:      exec,
			Binary:    binary,
			MixedEOL:  fsutil.DetectEOL(content) == fsutil.EOLMixed,
			Generated: old.had && old.meta.Generated,
			Hash:      fsutil.CompareHash(content, binary),
		}
		baseSnap.Bytes[p] = content
	}
	// hooksRun（§4.2）：未给 --no-hooks 且清单声明了 post_update。
	hooksRun := !o.NoHooks && m.Hooks.PostUpdate != ""
	actions := plan.Plan(baseSnap.Metas, oursSnap, theirsSnap, pruned, hooksRun)
	rep.Actions = actions

	// 8. merge 分类（§10.2 第 3 步；R3/R19）——就地改写 actions 的元素
	merged, conflicted, conflicts, err := classifyMerges(actions, baseSnap, oursSnap, oursBytes, theirsSnap, theirsRendered)
	if err != nil {
		return nil, err
	}
	rep.Conflicted = sortedStringKeys(conflicted)
	// conflicts/ 内容条目 ⊆ 冲突全集（ConflictKind 且 theirs 缺失时无条目）：
	// 报告据此决定是否打印「参考：conflicts/」行（评审 E1/m-3）。实跑写入的
	// 条目与 c.Conflicts 一致（apply.rebuildConflicts 全量重建），无需另取。
	rep.ConflictFiles = sortedStringKeys(conflicts)

	// 8b. --accept 后处理（§10.3）：紧跟 merge 分类之后。
	for _, p := range o.Accept {
		if !acceptSet[p] {
			continue
		}
		a := findAction(actions, p)
		switch {
		case a == nil:
			// base✗ 且 P 不在本轮并集：无 action 即无 NewBase，files 由
			// advanceState 自动移出；条目按「已不再冲突」清理（§10.3）。
			delete(conflicts, p)
		case a.Kind == plan.ConflictKind:
			return nil, fmt.Errorf("--accept %s：路径是符号链接/目录障碍（ConflictKind）——accept 改变不了类型；请把 %s 写进 .xinit/prune.txt 使其脱离管理，或移走障碍物（§10.3）", p, p)
		case a.Kind == plan.Noop && a.Note == plan.SkipNote:
			// 跳过优先（§8.2 L7/§10.3 定向复核 b）：跳过轮不得借 accept
			// 悄悄推进 base。
			return nil, fmt.Errorf("--accept %s：本轮 --no-hooks 跳过了该 generated 路径（跳过优先，§8.2 L7）——去掉 --no-hooks（或等清单声明 post_update）重跑，或把 %s 写进 .xinit/prune.txt（§10.3）", p, p)
		case isConflictAction(a, conflicted):
			// 重写后的 base′ 仍判冲突：NewBase 与 base 字节还原为「重写前」
			// 的 files[P]（§10.3 后处理；第五轮 F1——否则 base/<P> 会与
			// files[P].hash 分叉，且 advanceState 的「保持旧值」分支会读到
			// 被重写的字节）。
			old := acceptOlds[p]
			if old.had {
				m := old.meta
				a.NewBase = &m
				baseSnap.Bytes[p] = old.bytes
			} else {
				a.NewBase = nil
				delete(baseSnap.Bytes, p)
			}
		default:
			if _, inTheirs := theirsSnap[p]; !inTheirs {
				// P 本轮已不在 theirs 里（模板删了它）：不重写 base′、一律
				// 脱离管理。覆盖为 Noop + Note 必需：用户把 conflicts/P
				// 原样拷成 ours 时，按行 8 会删掉这份刚解决的文件（§10.3）。
				a.Kind = plan.Noop
				a.NewBase = nil
				a.Note = acceptDetachedNote
				delete(conflicts, p)
			}
			// 其余（重写后干净合并/推进）：照常走决策表的结论。
		}
	}

	// --accept 可用集：冲突全集里非 ConflictKind 的路径（打 accept 命令用）。
	for _, p := range rep.Conflicted {
		if a := findAction(actions, p); a != nil && a.Kind != plan.ConflictKind {
			rep.Acceptable = append(rep.Acceptable, p)
		}
	}

	// 9. 第 4 步：未解决冲突检查（conflicts/ 现有条目 ∩ 本轮冲突集合）
	existing, err := listConflicts(projectDir)
	if err != nil {
		return nil, err
	}
	rep.Blocked = step4Unresolved(existing, conflicted)
	// --accept 豁免第 4 步检查（§10.3）：这是这套设计里唯一「必须先豁免
	// 才能退出循环」的接口，不能省。
	rep.Blocked = except(rep.Blocked, acceptSet)
	if len(rep.Blocked) > 0 && !o.DryRun {
		rep.ExitCode = CodePending
		return rep, nil // 不执行任何写操作、不推进任何状态（§10.2 第 4 步）
	}

	// 10. 组装变更集（第 8 步的输入）
	changes, err := buildChanges(baseSnap.Metas, oursSnap, theirsSnap, actions, theirsRendered, merged, conflicts)
	if err != nil {
		return nil, err
	}

	if o.DryRun {
		// 只读预测：拒绝项检查与 Execute 前两步同判据，但不落盘、不改状态
		refusals, err := predictRefusals(projectDir, changes)
		if err != nil {
			return nil, err
		}
		refused := refusalSet(refusals)
		rep.Refused = refusals
		rep.Written = keysExcept(changes.Writes, refused)
		rep.Deleted = except(changes.Deletes, refused)
		rep.Chmodded = chmodPredictions(changes, refused)
		rep.Merged = sortedStringKeys(merged)
		rep.Resolved = except(existing, conflicted)
		// §7.5：改名配对检测只进 diff/--dry-run 的报告（update 不产出）
		rep.Renames = detectRenames(actions, baseSnap.Metas, theirsSnap)
		rep.ExitCode = dryRunExit(actions)
		return rep, nil
	}

	// 11. 脏检查（§10.2 第 5 步；--force 跳过；两条路都不阻止执行）
	warnIfDirty(projectDir, o.Force)

	// 12. 信任确认（§10.2 第 6 步、§10.5）——在备份与任何写入之前。
	if m.Hooks.PostUpdate != "" {
		if st.TrustedHooks == nil {
			st.TrustedHooks = map[string]string{}
		}
		h := hooks.Hash(m.Hooks.PostUpdate)
		if o.NoHooks {
			// 跳过执行也记录内容（§10.5）：下次不带 --no-hooks 仍需确认。
			hooks.Seen(st.TrustedHooks, h)
		} else if hooks.NeedConfirm(st.TrustedHooks, h) {
			oldVer, diff := hookDiffAgainstBase(st, cacheRoot, m.Hooks.PostUpdate)
			printHookNotice(os.Stdout, "post_update", m.Hooks.PostUpdate, oldVer, diff)
			if !o.Yes {
				printHookAsk(os.Stdout) // T4-R2：--yes 预先确认时不打指引
			}
			ok := o.Yes
			if !ok && hooks.Interactive(os.Stdin) {
				var cerr error
				if ok, cerr = hooks.Confirm(os.Stdin, os.Stdout); cerr != nil {
					return nil, cerr
				}
			}
			if !ok {
				rep.ExitCode = CodeNeedTrust
				return rep, nil // 未做任何修改；Update() 特判此码（不打印动作报告）
			}
			hooks.Consent(st.TrustedHooks, h, hookVersion(src))
		}
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

	// 16. 第 9 步：推进 state 与 .xinit/base/（先 base 后 state，含写入自检）
	if err := advanceState(projectDir, st, baseSnap, actions, theirsRendered, newSrc, vars, src, pending); err != nil {
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

	// 19. 退出码（§11.5；M3-R2 优先级 3 ＞ 2 ＞ 0）：冲突/折叠拒绝/障碍物
	// 待解决 → 2；钩子失败 → 3（本轮没有完整做完；2 的状态已持久化，
	// 下一轮照旧会报）。
	rep.ExitCode = CodeOK
	if pending {
		rep.ExitCode = CodePending
	}
	if rep.HookFailed {
		rep.ExitCode = CodeHookFail
	}
	return rep, nil
}

// openTarget 打开模板源并选定目标版本（§11.3、R8）。
func openTarget(st *state.State, rev, cacheRoot string, fetch bool) (*template.Source, error) {
	target := rev
	switch {
	case rev != "":
		// 显式指定：允许任意 ref、允许降级（下面给后代警告）
	case st.Template.Kind == template.KindDir.String():
		// dir 源没有版本概念：原样重开（Open 忽略 revision）
	default:
		sel, err := template.SelectRevision(st.Template.Source, cacheRoot, fetch, st.Template.Revision)
		if err != nil {
			return nil, err
		}
		target = sel
	}

	src, err := template.Open(st.Template.Source, target, cacheRoot, fetch)
	if err != nil {
		return nil, err
	}

	// 显式 --revision 的降级警告（§11.3）：目标不是当前 revision 的后代时
	// 警告但不阻止——两份版本之间模板的改动在磁盘上并不存在，会被当成
	// 「本地把模板改回去了」按行 9 永久保留，之后不会再被应用。
	if rev != "" && st.Template.Revision != "" && rev != st.Template.Revision {
		anc, err := template.IsAncestor(st.Template.Source, cacheRoot, fetch, st.Template.Revision, rev)
		switch {
		case err != nil:
			fmt.Fprintf(os.Stderr, "xinit: 警告: 无法判断 %s 是否为 %s 的后代: %v\n", rev, st.Template.Revision, err)
		case !anc:
			fmt.Fprintf(os.Stderr,
				"xinit: 警告: 目标版本 %s 不是当前版本 %s 的后代（降级/回退）：两版之间模板的改动不会出现在本项目里，之后可能被误判为本地改动（§11.3）\n",
				rev, st.Template.Revision)
		}
	}
	return src, nil
}

// loadManifestVars 读模板清单并解析本轮 vars（R15：--config > 冻结值 > default）。
// 同时返回 Manifest——hooks 块挂在它上面（Compiled 无 Hooks 字段）。
func loadManifestVars(src *template.Source, st *state.State, configPath string) (*manifest.Manifest, *manifest.Compiled, render.Vars, error) {
	manifestData, err := fs.ReadFile(src.FS, "xinit.yml")
	if err != nil {
		return nil, nil, nil, fmt.Errorf("模板缺少 xinit.yml: %w", err)
	}
	m, err := manifest.Parse(manifestData)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("解析模板的 xinit.yml: %w", err)
	}
	compiled, err := m.Compile()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("编译模板的 xinit.yml: %w", err)
	}
	var config map[string]string
	if configPath != "" {
		if config, err = LoadConfig(configPath); err != nil {
			return nil, nil, nil, err
		}
	}
	vars, err := ResolveUpdateVars(compiled, st.Vars, config)
	if err != nil {
		return nil, nil, nil, err
	}
	return m, compiled, vars, nil
}

// renderTheirs 渲染模板侧全部路径（§7）。告警与 init 完全同格式：
// 路径渲染警告逐条；替换规则零命中按**规则**汇总（§7.4）。
func renderTheirs(src *template.Source, compiled *manifest.Compiled, vars render.Vars,
) ([]render.Entry, map[string][]byte, map[string]string, error) {
	entries, warnings, err := render.Paths(src.FS, compiled, vars)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("渲染模板路径: %w", err)
	}
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "xinit: 警告: %s: %s\n", w.Path, w.Msg)
	}

	rendered := make(map[string][]byte, len(entries))
	srcOf := make(map[string]string, len(entries))
	hitTotals := make(map[int]int)
	for _, e := range entries {
		data, hits, warns, err := render.Content(src.FS, compiled, vars, e)
		if err != nil {
			return nil, nil, nil, err
		}
		for _, h := range hits {
			hitTotals[h.Index] += h.Count
		}
		for _, w := range warns {
			fmt.Fprintf(os.Stderr, "xinit: 警告: %s: %s\n", w.Path, w.Msg)
		}
		rendered[e.Dst] = data
		srcOf[e.Dst] = e.Src
	}
	for i, cs := range compiled.Subst {
		if hitTotals[i] == 0 {
			fmt.Fprintf(os.Stderr,
				"xinit: 警告: 替换规则 %d (%q) 在渲染结果中命中 0 次 —— 模板里可能已无此字面量\n",
				i, cs.Spec.From)
		}
	}
	return entries, rendered, srcOf, nil
}

// snapFromRendered 把模板侧的渲染结果变成 plan 快照——对派生侧用
// worktree.Scan，两边同一套探测与 hash（fsutil.ContentHash/IsBinary/DetectEOL），
// 同一份内容在两侧得到同一个 hash。
func snapFromRendered(entries []render.Entry, rendered map[string][]byte) plan.Snap {
	snap := make(plan.Snap, len(entries))
	for _, e := range entries {
		content := rendered[e.Dst]
		snap[e.Dst] = plan.FileMeta{
			Kind:      e.Kind,
			Exec:      e.Exec,
			Binary:    e.Binary,
			MixedEOL:  fsutil.DetectEOL(content) == fsutil.EOLMixed,
			Generated: e.Generated,
			Hash:      fsutil.ContentHash(content),
		}
	}
	return snap
}

// unionKeys 返回 base ∪ theirs 的路径（字节序）。与 plan 内部的并集同一
// 定义（R18）——ours 的扫描范围必须一致，否则行语义会漂。
func unionKeys(base, theirs plan.Snap) []string {
	seen := make(map[string]bool, len(base)+len(theirs))
	var out []string
	for _, s := range []plan.Snap{base, theirs} {
		for p := range s {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	sort.Strings(out)
	return out
}

// expandPrune 读 .xinit/prune.txt 并把 glob 展开到并集路径上（§8.0、R4）。
func expandPrune(projectDir string, paths []string) (plan.PruneSet, error) {
	patterns, err := state.ReadPrune(projectDir)
	if err != nil {
		return nil, err
	}
	set := plan.PruneSet{}
	if len(patterns) == 0 {
		return set, nil
	}
	matcher, err := fsutil.CompileGlob(patterns)
	if err != nil {
		return nil, fmt.Errorf("解析 .xinit/prune.txt: %w", err)
	}
	for _, p := range paths {
		if matcher.Match(p) {
			set[p] = true
		}
	}
	return set, nil
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
// 都进 conflicts/ 内容表；内容 = theirsRendered[P]，缺失（如行 6 的 binary 在
// theirs 里存在则不会缺；Kind 冲突在 theirs 里存在）时跳过。
func classifyMerges(actions []plan.Action, baseSnap *baseSnapshot, oursSnap plan.Snap,
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
	switch a.Kind {
	case plan.Noop, plan.WriteTheirs, plan.Merge:
	default:
		return false, false
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

// listConflicts 精确列举 .xinit/conflicts/ 的条目（相对项目根、/ 分隔）。
// 目录不存在 = 没有未解决冲突。
func listConflicts(projectDir string) ([]string, error) {
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

// predictRefusals 是 dry-run 的只读预测：与 Execute 的前两步同判据
// （删除的 §9.5、写前检查），只是不落盘——diff 因此能在不改任何东西的
// 前提下报告折叠拒绝与障碍物。
func predictRefusals(projectDir string, c apply.Changes) ([]apply.Refusal, error) {
	var out []apply.Refusal
	deleteSet := make(map[string]bool, len(c.Deletes))
	for _, rel := range c.Deletes {
		ok, err := worktree.WithinResolvedRoot(projectDir, rel)
		if err != nil {
			return nil, err
		}
		if !ok {
			out = append(out, apply.Refusal{
				Path:   rel,
				Reason: "删除路径解析后落在项目根之外（父路径有指向外部的符号链接）；请移走它（§9.5）",
			})
			continue
		}
		deleteSet[rel] = true
	}
	refusals, err := apply.CheckWrites(projectDir, sortedStringKeys(c.Writes), deleteSet)
	if err != nil {
		return nil, err
	}
	out = append(out, refusals...)
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
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

// dryRunExit 是 diff/dry-run 的退出码（§11.5）：有非 Noop → 5，否则 0。
// 不模拟 2——没有执行，就没有「待解决」的写盘后果要报。
func dryRunExit(actions []plan.Action) int {
	for _, a := range actions {
		if a.Kind != plan.Noop {
			return CodeChanged
		}
	}
	return CodeOK
}

// detectRenames 是 §7.5 的改名配对检测（只被 diff/--dry-run 调用；update
// 不产出——规范原文「v1 只在 diff 里做配对检测」）。动作本身仍按行 8/9/13
// 走，这里只产出「疑似」提示。
//
// 配对判据：旧候选（theirs 里已消失的路径——行 8 Delete / 行 9 KeepWarned）
// 与新增候选（行 13：base✗ 的 WriteTheirs）在**模板空间**内容相同，即
// base[P].Hash == theirs[Q].Hash（§9.1 归一化 hash，路径变量写进内容时
// hash 不等 → 不配对，属可接受的漏报）。只报告 1:1 唯一配对；空文件、
// 相同 LICENSE 造成的多对多歧义一律不报（§7.5 的歧义成本）。
func detectRenames(actions []plan.Action, base plan.Snap, theirs plan.Snap) []RenameNote {
	oldsByHash := map[string][]string{}
	kinds := map[string]plan.ActionKind{}
	for _, a := range actions {
		kinds[a.Path] = a.Kind
		switch a.Kind {
		case plan.Delete, plan.KeepWarned:
			if b, ok := base[a.Path]; ok {
				oldsByHash[b.Hash] = append(oldsByHash[b.Hash], a.Path)
			}
		}
	}
	newsByHash := map[string][]string{}
	for _, a := range actions {
		if a.Kind != plan.WriteTheirs {
			continue
		}
		if _, inBase := base[a.Path]; inBase {
			continue // 行 2（模板改了已有路径）不是改名候选
		}
		if t, ok := theirs[a.Path]; ok {
			newsByHash[t.Hash] = append(newsByHash[t.Hash], a.Path)
		}
	}
	out := make([]RenameNote, 0, len(oldsByHash))
	for h, olds := range oldsByHash {
		news := newsByHash[h]
		if len(olds) != 1 || len(news) != 1 {
			continue // 一对多 / 多对一：歧义，不报（§7.5）
		}
		out = append(out, RenameNote{
			Old:       olds[0],
			New:       news[0],
			LocalKept: kinds[olds[0]] == plan.KeepWarned,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Old < out[j].Old })
	return out
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
func advanceState(projectDir string, st *state.State, baseSnap *baseSnapshot,
	actions []plan.Action, theirsRendered map[string][]byte,
	newSrc map[string]string, vars render.Vars, src *template.Source, pending bool,
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
				return fmt.Errorf("内部错误：%q 保持旧 base 但 files 里没有它", a.Path)
			}
			content = b
			srcPath = old.Src
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
	st.EngineVersion = EngineVersion
	// Source 不变：openTarget 就是从 st.Template.Source 打开的
	//（template.Source 不携带来源字符串，报告侧用 st.Template.Source）。
	st.Template.Kind = src.Kind.String()
	if !pending {
		// 只有本轮全清（无冲突、无折叠拒绝/障碍物）才推进模板版本与
		// commit——未收敛时保持旧值，理由见函数注释。
		st.Template.Revision = src.Revision
		st.Template.Commit = src.Commit
	}
	return st.Save(projectDir)
}

// hasConfirmed 报告 trusted 里是否存在任何已确认记录（非空版本值）。
// 仅 marker（""）不算——首次确认前没有可做 diff 的基线（M3-R5）。
func hasConfirmed(trusted map[string]string) bool {
	for _, v := range trusted {
		if v != "" {
			return true
		}
	}
	return false
}

// hookDiffAgainstBase 取「已记录版本」（st.Template.Revision/Commit）的模板
// 清单里同字段钩子，与新块做行级 diff（M3-R5、§10.5「打印全文与 diff」）。
// 任何一步不可得（dir 源无历史、首次确认、旧清单读不到）→ ("", "")，调用方
// 省略 diff 段：不阻断确认流程。
func hookDiffAgainstBase(st *state.State, cacheRoot, newBlock string) (oldVer, diff string) {
	if st.Template.Kind != template.KindGit.String() {
		return "", "" // dir 源没有历史版本可比
	}
	if !hasConfirmed(st.TrustedHooks) {
		return "", "" // 首次确认：无基线
	}
	src, err := template.Open(st.Template.Source, st.Template.Commit, cacheRoot, false)
	if err != nil {
		return "", ""
	}
	mdata, err := fs.ReadFile(src.FS, "xinit.yml")
	if err != nil {
		return "", ""
	}
	m, err := manifest.Parse(mdata)
	if err != nil {
		return "", ""
	}
	if _, err := m.Compile(); err != nil {
		return "", "" // 旧清单编译不过 = 不可得
	}
	d := hooks.LineDiff(m.Hooks.PostUpdate, newBlock)
	if d == "" {
		return "", ""
	}
	return orNone(st.Template.Revision), d
}

// findAction 在按路径排序的动作表里定位路径（线性；规模 = 并集大小）。
// 返回的是切片内元素的指针——后处理的修改直接生效。
func findAction(actions []plan.Action, p string) *plan.Action {
	for i := range actions {
		if actions[i].Path == p {
			return &actions[i]
		}
	}
	return nil
}

// isConflictAction 报告动作是否仍属冲突类（Merge 冲突、Binary、BothAdded、
// ConflictKind）——--accept 后处理据此决定是否还原 NewBase（§10.3）。
func isConflictAction(a *plan.Action, conflicted map[string]bool) bool {
	switch a.Kind {
	case plan.ConflictBinary, plan.ConflictBothAdded, plan.ConflictKind:
		return true
	case plan.Merge:
		return conflicted[a.Path]
	}
	return false
}

// acceptDetachedNote 是 --accept 对「模板已删除该路径」的覆盖文案（§10.3）。
const acceptDetachedNote = "已按 --accept 声明解决：模板已删除该路径，本轮脱离管理（不再有 base 记录；磁盘不动）"
