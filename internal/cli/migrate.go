package cli

import (
	"path"
	"sort"

	"github.com/xbcio/xinit/internal/plan"
	"github.com/xbcio/xinit/internal/render"
	"github.com/xbcio/xinit/internal/state"
)

// 本文件是 §17（v2 路径迁移）的纯逻辑层：配对检测（§17.1）与静态决策
// （§17.2 第 0–2 步的候选分流）。不触盘——拒绝预检（§17.4）与内容迁移
// （二进制/混合行尾过滤、三方合并、字节改写）由 cli 层的迁移分类
// （classifyMigrations，§17.3）负责。

// MigrationNote 是 §17.6 的一行路径迁移报告。
//
// Status 取值：migrated（Clean 迁移落地）/ fallback（内容层面无法完成）/
// rename（本地无内容改动，按行 8/13 完成改名）/ skip（前置跳过）。
// Reason 仅 fallback 与 skip 非空。
type MigrationNote struct {
	Old, New, Status, Reason string
}

// migrationPair 是 §17.1 配对出的一对候选：Old = P（base 有、theirs 无，
// 动作行 8 `Delete` / 行 9 `KeepWarned`），New = Q（行 13 `WriteTheirs`）。
// PartA 标记配对通道：true = 同模板源（`files[P].Src == newSrc[Q]`），
// false = 同内容 hash（Part B）。
type migrationPair struct {
	Old, New string
	PartA    bool
}

// detectMigrationPairs 是 §17.1 的配对检测：在动作表上找「旧路径消失 +
// 新路径出现」的 1:1 唯一配对。两通道都要求判据值下旧、新两侧各恰一个候选，
// 多对多一律不配（歧义成本与 §7.5 一致）；Part A 先配，已占用的 P/Q 从
// Part B 候选剔除。配对表按 Old 字节序排序（确定性）。
//
// ours 不参与判据（§17.7：配对只用模板空间信息 → 重跑结果不振荡）。
//
// 天然排除（§17.1，由动作表与快照结构保证，无需专门分支）：prune 路径无
// 动作；行 12（ours[P] 与 theirs[P] 同缺）不产出动作——本地删除的 P 不进
// 候选；行 15 both-added 与类型冲突路径的动作不是 8/9/13；ours[Q] 已存在
// 不产出 WriteTheirs。
func detectMigrationPairs(actions []plan.Action, base, ours, theirs plan.Snap,
	files map[string]state.FileState, newSrc map[string]string) []migrationPair {
	// 候选集：旧 = 行 8/9 动作 ∧ P ∈ base ∧ state 有记录；
	// 新 = 行 13 动作 ∧ Q ∉ base ∧ state 无记录（Q ∈ files 的防御同理）。
	var olds, news []string
	for _, a := range actions {
		switch a.Kind {
		case plan.Delete, plan.KeepWarned:
			if _, inBase := base[a.Path]; !inBase {
				continue
			}
			if _, tracked := files[a.Path]; tracked {
				olds = append(olds, a.Path)
			}
		case plan.WriteTheirs:
			if _, inBase := base[a.Path]; inBase {
				continue
			}
			if _, tracked := files[a.Path]; !tracked {
				news = append(news, a.Path)
			}
		}
	}

	// Part A：按模板原路径分组；两侧同 Src 各恰一个才配对。
	oldBySrc := map[string][]string{}
	for _, p := range olds {
		s := files[p].Src
		oldBySrc[s] = append(oldBySrc[s], p)
	}
	newBySrc := map[string][]string{}
	for _, q := range news {
		if s, ok := newSrc[q]; ok && s != "" {
			newBySrc[s] = append(newBySrc[s], q)
		}
	}
	usedOld := map[string]bool{}
	usedNew := map[string]bool{}
	var out []migrationPair
	for _, s := range sortedStringKeys(oldBySrc) {
		os, ns := oldBySrc[s], newBySrc[s]
		if len(os) != 1 || len(ns) != 1 {
			continue // 一对多 / 多对一：歧义，不配（§17.1）
		}
		out = append(out, migrationPair{Old: os[0], New: ns[0], PartA: true})
		usedOld[os[0]], usedNew[ns[0]] = true, true
	}

	// Part B：剩余候选按 base[P].Hash == theirs[Q].Hash 分组，同样 1:1 才配。
	oldByHash := map[string][]string{}
	for _, p := range olds {
		if usedOld[p] {
			continue
		}
		oldByHash[base[p].Hash] = append(oldByHash[base[p].Hash], p)
	}
	newByHash := map[string][]string{}
	for _, q := range news {
		if usedNew[q] {
			continue
		}
		t, ok := theirs[q]
		if !ok {
			continue // 不该发生（行 13 前件含 theirs✓）；防御：无模板元数据不配
		}
		newByHash[t.Hash] = append(newByHash[t.Hash], q)
	}
	for _, h := range sortedStringKeys(oldByHash) {
		os, ns := oldByHash[h], newByHash[h]
		if len(os) != 1 || len(ns) != 1 {
			continue
		}
		out = append(out, migrationPair{Old: os[0], New: ns[0]})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Old < out[j].Old })
	return out
}

// migrationDecision 是 §17.2 静态决策的分流结果。`fallback` 不在此枚举——
// 它由迁移分类在内容迁移阶段（§17.2 第 2 步）产生。
type migrationDecision int

const (
	migRename migrationDecision = iota // 第 0 步：ours[P] 未改（仅记信息行，不改写动作）
	migSkip                            // 第 1 步：前置跳过（按 v1 行为）
	migDirect                          // 第 2 步：hash 相等，逐字节直搬
	migMerge                           // 第 2 步：hash 不等，走三方合并
)

// decideMigration 是 §17.2 的静态决策：第 0 步 rename → 第 1 步前置跳过
// （generated → 空内容[Part B] → 类型 → 置信度[Part B]）→ 第 2 步
// 直搬/合并分流。返回的 reason 仅 migSkip 非空（§17.6 的 MigrationNote 口径）。
//
// 不在本函数的两件事（需 projectDir 或盘上字节，由迁移分类负责）：§17.4 的
// 拒绝预检，与 §17.2 第 2 步的 Binary/MixedEOL 前置过滤（b/o/t 任一命中 →
// fallback，不进入三方合并）。本函数只按快照元数据分流。
func decideMigration(p migrationPair, base, ours, theirs plan.Snap,
	baseBytes map[string][]byte) (migrationDecision, string) {
	// 第 0 步：ours[P] 内容未改 → 不做改写（行 8+13 本与改名等价）。仅 exec
	// 位差异也归此档（§8.3 的位保护只在同一路径上成立，R6 保留 v1 语义）。
	if ours[p.Old].Hash == base[p.Old].Hash {
		return migRename, ""
	}

	// 第 1 步：前置跳过——任一命中即不迁移，保持 v1 行为（行 8/9/13 + 报告）。
	if base[p.Old].Generated || theirs[p.New].Generated {
		return migSkip, "生成物路径不参与迁移"
	}
	// 空内容的 1:1 配对不可信（.gitkeep 类）。只限 Part B：Part A 靠同 Src，
	// 空占位被本地填写后位移正是其旗舰场景（N2）。base 字节缺失（降级 base）
	// 同样按不迁移处理——保守优先。
	if !p.PartA && len(baseBytes[p.Old]) == 0 {
		return migSkip, "空内容配对不自动迁移"
	}
	if ours[p.Old].Kind != render.KindRegular || base[p.Old].Kind != render.KindRegular {
		return migSkip, "类型不符" // 类型面归 §8.1
	}
	// 名字与目录都变 → 名称维度无佐证，配对置信度不足（Part A 不受影响）。
	// Dst 全项目为 / 分隔的规范相对路径，故用 path 而非 filepath。
	if !p.PartA && path.Dir(p.Old) != path.Dir(p.New) && path.Base(p.Old) != path.Base(p.New) {
		return migSkip, "名字与目录都变，配对置信度不足"
	}

	// 第 2 步：内容迁移分流——hash 相等逐字节直搬（二进制同样适用，不调
	// merge）；不等走三方合并（仅 Part A 可达——模板同时在改内容）。
	if theirs[p.New].Hash == base[p.Old].Hash {
		return migDirect, ""
	}
	return migMerge, ""
}
