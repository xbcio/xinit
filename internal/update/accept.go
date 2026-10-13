package update

// 来源：internal/update/update.go 的 Run 块 13（M2-P4 rewriteAccept 抽取）
// + 块 16-18（M2-P6 refineDecision 追加）+ findAction/isConflictAction 整函数
// 搬家（I-2 实施批 T5/M2b）。阶段判据（§3-M2）：函数体与源区间逐字对应，
// 差异仅「参数接入」「返回值包装」与附加裁定类（R-M2-2/3/9/10）——逐条见
// task-5-report.md。

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/xbcio/xinit/internal/fsutil"
	"github.com/xbcio/xinit/internal/plan"
	"github.com/xbcio/xinit/internal/render"
	"github.com/xbcio/xinit/internal/state"
)

// AcceptRewrite 是 §10.3 的 base′ 重写账（P4 写，P6 消费还原；§2.3）。
type AcceptRewrite struct {
	Set              map[string]bool
	Olds             map[string]oldEntry // P → 重写前的 Metas/Bytes/had（I1、I16）
	PrunedWithAccept plan.PruneSet       // 候选集口径（I3/I32），绝不喂 plan
}

// oldEntry 是 P 重写前的 base 条目（原名 acceptOld，M2-P4 机械改名——R-M2-9；
// 字段逐字）。
type oldEntry struct {
	meta  plan.FileMeta
	bytes []byte
	had   bool // base 快照里原有该条目（Metas/Bytes 同源）
}

// rewriteAccept 是 P4（§2.2）：accept 预检与 base 重写（块 13）——5 个拒绝
// 站点文案逐字；返回值包装进 AcceptRewrite。重写段收敛到单点方法
// a.rewrite（R-M2-9）。
func rewriteAccept(s *session, b *Base, o *Ours, accept []string) (*AcceptRewrite, error) {
	// 7b. --accept 预检与 base 重写（§10.3，在算 plan 之前）：把 P 的 base
	// 快照条目换成 .xinit/conflicts/<P> 的内容——三方比较的 base 从此是
	// 「用户上次看到并认可的那份」，而不是本次运行的 theirs（第五轮 D2）。
	a := &AcceptRewrite{Set: map[string]bool{}, Olds: map[string]oldEntry{}}
	// prune 判定的候选集另算（T7 评审 Nit-3，§10.3 :906「P 已在 prune.txt
	// 中」是字面承诺）：并集 ∪ in.Accept——陈旧 conflicts 项（P 已不在并集）
	// 的字面 prune 行同样拒。**独立变量**：绝不可并入喂给 plan.Plan 的
	// pruned（那会让 accept 路径凭空移出并集）。
	acceptCand := append(append([]string{}, o.Union...), accept...)
	prunedWithAccept, err := expandPrune(s.dir, acceptCand)
	if err != nil {
		return nil, err
	}
	for _, p := range accept {
		if a.Set[p] {
			continue
		}
		if !fsutil.ValidRelPath(p) {
			return nil, fmt.Errorf("--accept %s：必须是 .xinit/conflicts/ 下的规范相对路径（不接受空串、绝对路径、.. 段、含 `\\` 或 `:` 或未规范化写法，§10.3）", p)
		}
		if prunedWithAccept[p] {
			return nil, fmt.Errorf("--accept %s：该路径已在 .xinit/prune.txt 中，已脱离管理、无需 accept（§10.3）", p)
		}
		content, err := os.ReadFile(filepath.Join(s.dir, state.DirName, "conflicts", filepath.FromSlash(p)))
		if err != nil {
			return nil, fmt.Errorf("--accept %s：.xinit/conflicts/%s 不存在或不可读——--accept 只能声明「已解决的冲突」（§10.3）：%v", p, p, err)
		}
		old := oldEntry{bytes: b.Bytes[p]}
		if m, ok := b.Metas[p]; ok {
			old.meta, old.had = m, true
		}
		a.Olds[p] = old
		a.Set[p] = true
		a.rewrite(b, o, p, content)
	}
	a.PrunedWithAccept = prunedWithAccept
	return a, nil
}

// rewrite 是 P4 的单点方法（R-M2-9）：把 P 的 base′ 条目重写为 conflicts/<P>
// 的内容——binary/exec/元数据构造与 Metas/Bytes 同步写（原块 13 重写段）。
func (a *AcceptRewrite) rewrite(b *Base, o *Ours, p string, content []byte) {
	old := a.Olds[p]
	binary := fsutil.IsBinary(content)
	exec := false
	if old.had {
		exec = old.meta.Exec
	} else if m, ok := o.Snap[p]; ok {
		exec = m.Exec // 第五轮 F8：base✗ 取 ours.exec
	}
	b.Metas[p] = plan.FileMeta{
		Kind:      render.KindRegular,
		Exec:      exec,
		Binary:    binary,
		MixedEOL:  fsutil.DetectEOL(content) == fsutil.EOLMixed,
		Generated: old.had && old.meta.Generated,
		Hash:      fsutil.CompareHash(content, binary),
	}
	b.Bytes[p] = content
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

// refineDecision 是 P6（§2.2）：迁移分类（块 16）+ accept 后处理（块 17）
// ——就地补全/改写 d 与 b（b 经单点方法 a.restore）。
// rep.Migrations 依 R-M2-3 留守 Run（值经 d）；rep.Acceptable 由 report()
// 经 derivedFields 单点派生（批末回写）。§2.6 行内旧名
// refineAccept 与本名不一致（批末回写；task-5-report.md 备案）。
func refineDecision(s *session, t *Template, b *Base, o *Ours, d *Decision, a *AcceptRewrite) error {
	// 8a. 路径迁移分类（§17）：动作表后处理——可迁移对改写为 Delete+Merge 并注入 merged。
	mignotes, err := classifyMigrations(s.dir, d.Actions, b, o.Snap, o.Bytes, t.Snap, t.Rendered, t.SrcOf, s.st, d.Merged)
	if err != nil {
		return err
	}
	d.Migrations = mignotes

	// 8b. --accept 后处理（§10.3）：紧跟 merge 分类之后。
	for p := range a.Set {
		if !a.Set[p] {
			continue
		}
		act := findAction(d.Actions, p)
		switch {
		case act == nil:
			// P 不在本轮并集（行 12：ours✗∧theirs✗——7b 已无条件把 base′
			// 重写为 conflicts/P，动作表里仍没有它）：无 action 即无 NewBase，
			// files 由 advanceState 自动移出；条目按「已不再冲突」清理（§10.3）。
			delete(d.Conflicts, p)
		case act.Kind == plan.ConflictKind:
			return fmt.Errorf("--accept %s：路径是符号链接/目录障碍（ConflictKind）——accept 改变不了类型；请把 %s 写进 .xinit/prune.txt 使其脱离管理，或移走障碍物（§10.3）", p, p)
		case act.Kind == plan.Noop && act.Note == plan.SkipNote:
			// 跳过优先（§8.2 L7/§10.3 定向复核 b）：跳过轮不得借 accept
			// 悄悄推进 base。
			return fmt.Errorf("--accept %s：本轮 --no-hooks 跳过了该 generated 路径（跳过优先，§8.2 L7）——去掉 --no-hooks（或等清单声明 post_update）重跑，或把 %s 写进 .xinit/prune.txt（§10.3）", p, p)
		case isConflictAction(act, d.Conflicted):
			a.restore(b, act, p)
		default:
			if _, inTheirs := t.Snap[p]; !inTheirs {
				// P 本轮已不在 theirs 里（模板删了它）：不重写 base′、一律
				// 脱离管理。覆盖为 Noop + Note 必需：用户把 conflicts/P
				// 原样拷成 ours 时，按行 8 会删掉这份刚解决的文件（§10.3）。
				act.Kind = plan.Noop
				act.NewBase = nil
				act.Note = acceptDetachedNote
				delete(d.Conflicts, p)
			}
			// 其余（重写后干净合并/推进）：照常走决策表的结论。
			// base✗ 时 files 条目由 advanceState 以 newSrc 补齐（M9 C-1）。
		}
	}

	// --accept 可用集：冲突全集里非 ConflictKind 的路径（打 accept 命令用）。
	// 由 report() 经 derivedFields 单点实时派生（I17；批末回写删除缓存字段）。
	return nil
}

// restore 是 P6 的单点方法（R-M2-10b）：还原段 = 原块 17 的 isConflictAction
// 分支体（NewBase 与 base 的 Metas/Bytes 同步还原）。
func (a *AcceptRewrite) restore(b *Base, act *plan.Action, p string) {
	// 重写后的 base′ 仍判冲突：NewBase、base 字节与 Metas 一律还原为
	// 「重写前」的 files[P]（§10.3 后处理；第五轮 F1）。Metas 必须与
	// Bytes 同步——advanceState 用 baseSnap.Metas[p] 判定 advanced
	// （:1055），只还原 Bytes 会误判为推进 → 写 theirs 而记旧 hash →
	// 不变量 5 自检中止（spec :902「还原后第 9 步无事发生」）。
	old := a.Olds[p]
	if old.had {
		m := old.meta
		act.NewBase = &m
		b.Metas[p] = old.meta
		b.Bytes[p] = old.bytes
	} else {
		act.NewBase = nil
		delete(b.Metas, p)
		delete(b.Bytes, p)
	}
}

// acceptDetachedNote 是 --accept 对「模板已删除该路径」的覆盖文案（§10.3）。
const acceptDetachedNote = "已按 --accept 声明解决：模板已删除该路径，本轮脱离管理（不再有 base 记录；磁盘不动）"
