package update

// 来源：internal/update/update.go 的 Run 块 14-15 + hooksRun 行（M2-P5
// buildDecision 抽取；I-2 实施批 T5/M2b）。阶段判据（§3-M2）：函数体与源
// 区间逐字对应，差异仅「参数接入」「返回值包装」与附加裁定类（R-M2-3/12）
// ——逐条见 task-5-report.md。

import "github.com/xbcio/xinit/internal/plan"

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
