// plan.go：§8 决策表的纯函数实现（第零步 prune、第一步类型、第二步内容）。
package plan

import (
	"fmt"
	"sort"
	"strings"

	"github.com/xbcio/xinit/internal/fsutil"
	"github.com/xbcio/xinit/internal/render"
)

// Plan 实现规范 §8 的决策表（纯函数，契约见 §4.2）。
//
// 输入：base（`.xinit/base/` 与 state.files 的快照，Generated 位来自 state）、
// ours（worktree.Scan 的精确读盘）、theirs（新模板渲染结果）、pruned
// （prune.txt 展开后的精确 Dst 集合，R4）、hooksRun（§4.2；M3 起 = 未给
// --no-hooks ∧ 清单声明 post_update）。
//
// 输出：对并集（base ∪ theirs，R18）里除 prune 命中（§8.0）与行 12 之外的
// 每条路径产出一个 Action，按路径字节序排序（确定性）。「不在列表 = 移出
// files」由调用方落地（R5）。不碰磁盘、不看未跟踪文件、不读配置。
func Plan(base, ours, theirs Snap, pruned PruneSet, hooksRun bool) []Action {
	paths := Union(base, theirs)
	actions := make([]Action, 0, len(paths))
	for _, p := range paths {
		if pruned[p] {
			continue // §8.0 第零步：静默移出并集
		}
		if a, ok := decide(p, base, ours, theirs, hooksRun); ok {
			actions = append(actions, a)
		}
	}
	for i := range actions {
		if actions[i].Kind == Merge {
			continue // 占位；分类后由 SettleMergeClean 落定（R19）
		}
		settle(&actions[i], base, ours, theirs)
	}
	return actions
}

// advances 报告该动作本轮是否把 base 推进到 theirs[P]（§8.5 的「推进」行：
// 1–5、7 干净、10、11、13、14）。判据是**值相等 + 非 ConflictKind**：保持
// 旧值的行不可能与之相等——行 6/7 冲突有 theirs != base，行 9 的 theirs 不在
// 快照里，L7 跳过同样以 theirs != base 为前提；ConflictKind 从不推进，显式排除。
//
// advances 在 settle 里只求值一次、且必须在改 Generated 之前——位一旦按规则
// 改写，二次求值的 advances 会变 false，把 mode 步的 exec 分歧警告吞掉（裁定 R）。
func advances(a *Action, t FileMeta) bool {
	return a.NewBase != nil && a.Kind != ConflictKind && *a.NewBase == t
}

// settle 对单个动作做 §8.3 mode 步与 §8.5 的 generated 位修整。两条规则都
// 只对「推进到 theirs[P]」的动作生效——否则 L7 跳过（Noop + 旧值）会被 mode
// 步改写旧值的 Exec，违反 §8.5 的「保持旧值（含位）」。
func settle(a *Action, base, ours, theirs Snap) {
	t, tok := theirs[a.Path]
	if !tok || a.NewBase == nil {
		return
	}
	b, bok := base[a.Path]
	o, ook := ours[a.Path]

	// advances 求值一次、两处共用（裁定 R：位改写后二次求值会吞警告，理由见上）。
	if !advances(a, t) {
		return
	}
	a.NewBase.Generated = nextGenerated(bok && b.Generated, t.Generated,
		promoteAllowed(base, ours, theirs, a.Path))
	// §22.6：!ExecSupported（无 x 位语义平台，如 Windows）时整块不生效——
	// 不比较、不产 Note；NewBase.Exec 已由 advance 的整表拷贝取 theirs 值，
	// 记账无需动作（消除每轮假 Note）。
	if fsutil.ExecSupported && bok && ook && (a.Kind == Noop || a.Kind == WriteTheirs || a.Kind == Merge) {
		if o.Exec == b.Exec {
			a.NewBase.Exec = t.Exec // R7 在 apply 侧据此 chmod
		} else {
			a.Note = noteJoin(a.Note, "执行位与 base 不一致（本地改过权限）：本轮不调整 exec")
		}
	}
}

// nextGenerated 按 §8.5 的位推进规则算新位。
func nextGenerated(old, cur, promote bool) bool {
	if old == cur {
		return cur
	}
	if !cur {
		return false // true → false 不受限
	}
	return promote // false → true 只允许在行 1/2/13/14
}

// promoteAllowed 报告该路径本轮是否落在「false→true 允许集合」（§8.5）：
// 行 1/2（ours==base）、行 13（base✗ ours✗ theirs✓）、行 14（base✗ 且
// ours==theirs）。行 10/11（ours✗）与行 15 明确不放行（A5：用户删除是明确的
// 意图；行 15 有用户独有内容）。缺了这条限制，两步发布即可绕过行 5 的保护。
func promoteAllowed(base, ours, theirs Snap, p string) bool {
	b, bok := base[p]
	o, ook := ours[p]
	t, tok := theirs[p]
	if bok && ook && tok {
		return sameContent(o, b) // 行 1/2
	}
	if bok || !tok {
		return false
	}
	if !ook {
		return true // 行 13
	}
	return sameContent(o, t) // 行 14
}

// noteJoin 把追加文案并入 Note（已有文案保持在前）。
func noteJoin(note, extra string) string {
	if note == "" {
		return extra
	}
	return note + "；" + extra
}

// SettleMergeClean 落定一个被判为「干净合并」的 Merge 动作（§10.2 第 3 步的
// 分类结果之一，R19）：NewBase = theirs[P]，再走与 Plan 内部同一套修整
// （§8.3 mode + §8.5 位规则）。冲突（rc 1–127）与改判 ConflictBinary 的分支
// **不要**调用它——那条路的 NewBase 保持旧值（§8.5 行 7 两行）。
func SettleMergeClean(a *Action, base, ours, theirs Snap) {
	if t, ok := theirs[a.Path]; ok {
		m := t
		a.NewBase = &m
	}
	settle(a, base, ours, theirs)
}

// Union 返回 base ∪ theirs 的路径，按字节序排序。
func Union(base, theirs Snap) []string {
	seen := make(map[string]bool, len(base)+len(theirs))
	out := make([]string, 0, len(base)+len(theirs))
	for _, s := range []Snap{base, theirs} {
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

// decide 是单条路径的完整决策。ok=false 表示不产出 Action（行 12）。
//
// 三方的 switch **按规范表的行序落条款**（首个命中即停），每一条的隐含前提
// 由它前面的条款保证——改顺序等于改语义：
//
//	行1 → 行2（⇒ theirs!=base）→ 行3（⇒ ours!=base）→ 行4（⇒ ours!=theirs）
//	→ 行5/跳过 → 行6 → 行7
func decide(p string, base, ours, theirs Snap, hooksRun bool) (Action, bool) {
	b, bok := base[p]
	o, ook := ours[p]
	t, tok := theirs[p]

	// ── 第一步：类型（§8.1——短路，不看内容）─────────────────────────
	if note, bad := kindConflict(p, base, ours, theirs); bad {
		return Action{Path: p, Kind: ConflictKind, NewBase: oldBase(b, bok), Note: note}, true
	}

	// ── 第二步：内容（§8.2）───────────────────────────────────────
	if bok && ook && tok {
		switch {
		case sameContent(o, b) && sameContent(t, b): // 行 1
			return Action{Path: p, Kind: Noop, NewBase: advance(t)}, true
		case sameContent(o, b): // 行 2
			return Action{Path: p, Kind: WriteTheirs, NewBase: advance(t)}, true
		case sameContent(t, b): // 行 3
			return Action{Path: p, Kind: Noop, NewBase: advance(t)}, true
		case sameContent(o, t): // 行 4
			return Action{Path: p, Kind: Noop, NewBase: advance(t)}, true
		}
		// 行 5 的三个前提：theirs 声明 ∧ 上次也是 ∧ 钩子会运行
		if t.Generated && b.Generated {
			if !hooksRun {
				// L7 跳过：不判冲突、不写 conflicts/、NewBase 与位保持旧值
				return Action{Path: p, Kind: Noop, NewBase: oldBase(b, bok), Note: SkipNote}, true
			}
			return Action{Path: p, Kind: WriteTheirs, NewBase: advance(t),
				OverwriteGenerated: true, Note: generatedNote(p)}, true
		}
		// 行 6：任一方二进制或混合 EOL（含 base 侧——§8.2 原文「任一方」）
		if b.Binary || o.Binary || t.Binary || b.MixedEOL || o.MixedEOL || t.MixedEOL {
			return Action{Path: p, Kind: ConflictBinary, NewBase: oldBase(b, bok), Note: conflictNote(p)}, true
		}
		// 行 7：占位，真实值由 §10.2 第 3 步的分类落定（R3/R19）
		return Action{Path: p, Kind: Merge, NewBase: oldBase(b, bok)}, true
	}

	switch {
	case bok && ook && !tok: // 行 8 / 9
		if sameContent(o, b) {
			return Action{Path: p, Kind: Delete, NewBase: nil}, true
		}
		return Action{Path: p, Kind: KeepWarned, NewBase: oldBase(b, bok), Note: keepWarnedNote(p)}, true
	case bok && !ook && tok: // 行 10 / 11
		if !sameContent(t, b) {
			return Action{Path: p, Kind: KeepLocalDelete, NewBase: advance(t), Note: keepLocalDeleteNote(p)}, true
		}
		return Action{Path: p, Kind: Noop, NewBase: advance(t)}, true // 行 11（无声）
	case bok && !ook && !tok: // 行 12：移出并集，从 files 移除（静默，R5）
		return Action{}, false
	case !bok && !ook && tok: // 行 13
		return Action{Path: p, Kind: WriteTheirs, NewBase: advance(t)}, true
	case !bok && ook && tok: // 行 14 / 15
		if sameContent(o, t) {
			return Action{Path: p, Kind: Noop, NewBase: advance(t)}, true
		}
		return Action{Path: p, Kind: ConflictBothAdded, NewBase: nil, Note: bothAddedNote(p)}, true
	}
	// 到此只剩 ✗✗✗——不在并集内，不可达；静默返回会把 bug 藏起来。
	panic(fmt.Sprintf("plan: %q 的存在性组合不可达（base=%v ours=%v theirs=%v）", p, bok, ook, tok))
}

// sameContent 是决策表里 `==` 的含义：内容等价（Hash 相等）。
// Exec 不参与（R17）——模板只改 exec 位时走行 1/3 的 Noop + §8.3 的 mode 步。
func sameContent(a, b FileMeta) bool { return a.Hash == b.Hash }

// oldBase 拷贝 base 快照的条目（「保持旧值」）；base✗ → nil。
func oldBase(b FileMeta, ok bool) *FileMeta {
	if !ok {
		return nil
	}
	m := b
	return &m
}

// advance 拷贝 theirs[P]（「推进到 theirs」）。一定与输入快照脱钩——调用方
// 会就地改 NewBase.Exec/Generated（Task 6 的 settle 步）。
func advance(t FileMeta) *FileMeta {
	m := t
	return &m
}

// kindConflict 实现 §8.1（第一步）。
//
// ① 对存在的各方两两比较 Kind（快照只有 Regular/Symlink；Exec 不是 Kind）；
// ② 文件↔目录转换：P 的某个父前缀在某一侧是常规文件——文件顶掉了目录，
// 或目录顶掉了文件（§8.1「或反向」）。快照里只有文件与符号链接，出现即
// 「占住了这一格」。**未跟踪**的挡路文件/目录看不到，由 apply 期的
// ENOTDIR/EISDIR/符号链接检查兜住（§9.3，R6/R16）。
//
// ConflictKind 没有 --accept 出口：正当出口是 prune 或移走障碍物（§8.1）。
func kindConflict(p string, base, ours, theirs Snap) (string, bool) {
	var ref render.Kind
	refSide := ""
	first := true
	for _, s := range []struct {
		name string
		snap Snap
	}{{"base", base}, {"ours", ours}, {"theirs", theirs}} {
		m, ok := s.snap[p]
		if !ok {
			continue
		}
		if first {
			ref, refSide, first = m.Kind, s.name, false
			continue
		}
		if m.Kind != ref {
			return fmt.Sprintf("类型冲突：%s 侧是 %s、%s 侧是 %s，两者互斥。"+
				"出口：把 %s 写进 .xinit/prune.txt（脱离管理），或移走障碍物后重跑（§8.1）",
				refSide, ref, s.name, m.Kind, p), true
		}
	}
	for _, s := range []struct {
		name string
		snap Snap
	}{{"ours", ours}, {"theirs", theirs}} {
		if q, bad := ancestorIsFile(p, s.snap); bad {
			return fmt.Sprintf("类型冲突：%s 的父路径 %s 在 %s 侧是常规文件——文件与目录不能互相顶替。"+
				"出口：把 %s 写进 .xinit/prune.txt，或先移走 %s 后重跑（§8.1）",
				p, q, s.name, q, q), true
		}
	}
	return "", false
}

// ancestorIsFile 报告 p 的某个父前缀在 snap 里是常规文件，并返回该前缀。
func ancestorIsFile(p string, snap Snap) (string, bool) {
	for {
		i := strings.LastIndex(p, "/")
		if i < 0 {
			return "", false
		}
		p = p[:i]
		if m, ok := snap[p]; ok && m.Kind == render.KindRegular {
			return p, true
		}
	}
}

// ── 各行的 Note 文案（警告出口按 §8.2 要求写进文案）─────────────────

// SkipNote 是 §8.2 L7 跳过的备注。导出供 cli 的 --accept 后处理识别
// 「本轮被跳过」（§10.3 的拒绝项）——**文案不得改动**：脚本按它断言。
const SkipNote = "跳过（generated 且钩子不会运行）：不判冲突、不改动磁盘、base 与 generated 位保持旧值；带钩子重跑时按「模板生成物」处理（§8.2 L7）"

func generatedNote(p string) string {
	return fmt.Sprintf("模板生成物：本地改动被模板覆盖（钩子会在更新后重建它）；本地版本另存 .xinit/overwritten/%s", p)
}

func conflictNote(p string) string {
	return fmt.Sprintf("二进制或混合 EOL，不能三方合并；theirs 的内容写入 .xinit/conflicts/%s，请手工处理（改到与模板一致）后重跑", p)
}

// bothAddedNote 是行 15（ConflictBothAdded）的专属说明（M9 观察②）：两侧都是
// 新增、没有 base，与行 6 的二进制/混合 EOL 无关——沿用 conflictNote 会误报
// 成因。出口要素与 conflictNote 同款：theirs 进 conflicts/，手工处理后重跑。
func bothAddedNote(p string) string {
	return fmt.Sprintf("模板新增了该路径，本地已有同名内容且两者不同，无法自动合并；theirs 的内容写入 .xinit/conflicts/%s，请手工处理（改到与模板一致）后重跑", p)
}

func keepWarnedNote(p string) string {
	return fmt.Sprintf("模板删除了该路径，但你本地改过它——保留你的版本。"+
		"找回模板已记录的版本：cp .xinit/base/%s %s；不再看到这条警告：把 %s 写进 .xinit/prune.txt（§8.0）",
		p, p, p)
}

func keepLocalDeleteNote(p string) string {
	return fmt.Sprintf("你本地删除了该路径，模板有更新——保留删除、本轮不回写。"+
		"找回模板已记录的版本：cp .xinit/base/%s %s", p, p)
}
