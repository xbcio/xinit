package update

// 来源：internal/update/update.go 的 Run 块 11-12（M2-P3 snapshotOurs 抽取；
// I-2 实施批 T4/M2a）+ expandPrune 整函数搬家。阶段判据（§3-M2）：函数体与
// 源区间逐字对应，差异仅「参数接入」与「返回值包装」两类（逐条见
// task-4-report.md）。R-M2-4：§2.6 M2 布局表未给 Ours 家族落点，本文件为
// 新建第 15 文件（缺口登记，批末回写设计稿）。

import (
	"fmt"

	"github.com/xbcio/xinit/internal/fsutil"
	"github.com/xbcio/xinit/internal/plan"
	"github.com/xbcio/xinit/internal/state"
	"github.com/xbcio/xinit/internal/worktree"
)

// Ours 是 ours 侧快照（P3 建立；§2.3）。
type Ours struct {
	Union []string          // base ∪ theirs 的路径（字节序；R18）
	Snap  plan.Snap         // Dst → ours 元数据（Plan 的 ours 参数）
	Bytes map[string][]byte // Dst → ours 原始字节
	Prune plan.PruneSet     // .xinit/prune.txt 展开到并集上的结果（R4）
}

// snapshotOurs 是 P3（§2.2）：ours 快照（块 11）+ prune 展开（块 12）。
func snapshotOurs(s *session, t *Template, b *Base) (*Ours, error) {
	// 6. ours 快照（R18：只查 base ∪ theirs 的并集——与 Plan 的并集一致）
	union := unionKeys(b.Metas, t.Snap)
	oursSnap, oursBytes, err := worktree.Scan(s.dir, union)
	if err != nil {
		return nil, err
	}

	// 7. prune（R4）+ plan（R2）
	pruned, err := expandPrune(s.dir, union)
	if err != nil {
		return nil, err
	}
	return &Ours{Union: union, Snap: oursSnap, Bytes: oursBytes, Prune: pruned}, nil
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
