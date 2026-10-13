package update

// 来源：internal/update/update.go 的 Run 块 13（M2-P4 rewriteAccept 抽取；
// I-2 实施批 T5/M2b）。阶段判据（§3-M2）：函数体与源区间逐字对应，差异仅
// 「参数接入」「返回值包装」与附加裁定类（R-M2-2/9）——逐条见
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
