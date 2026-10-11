package backflow

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/xbcio/xinit/internal/fsutil"
	"github.com/xbcio/xinit/internal/manifest"
	"github.com/xbcio/xinit/internal/state"
)

// 各出口的文案（§20.10 文案基准表的散文句；报告层在行首加稳定标识前缀）。
const (
	notePruned      = "该路径已脱离管理，无法确定模板路径（prune 判据 = `.xinit/prune.txt`）；出口：人工判断"
	noteSourceGone  = "该路径的模板来源已不存在（模板已移除或改名）；如需回送请用 `--map` 显式给出模板路径，或人工判断"
	noteOutOfDomain = "不属于共享面（upstream.paths）；出口：重拆提交 / `--paths` 显式覆盖"

	// 域内条目的来源标注（§20.5 句 4：报告逐行注明来源：清单/旗标）。
	noteFromManifest = "来源：清单 upstream.paths"
	noteFromPaths    = "来源：--paths"
	noteFromBoth     = "来源：清单 upstream.paths、--paths"
)

// noteMapTargetMissing 是「来源消失」的 `--map` 救援命中失败点子类（§20.5 句 2，M9 注）：
// 用户给了 `--map A=B` 而 B 不在记录 commit 的模板树中——不再回指 `--map`（循环指引）。
func noteMapTargetMissing(src string) string {
	return fmt.Sprintf("`--map` 给出的模板路径 %q 不在记录 commit 的模板树中；请给出正确的模板路径，或人工判断", src)
}

// BuildItems 把枚举结果 + 状态装配为候选 Items（域判定前）。
//
// 装配规则：
//   - Dst = 提交内路径（Change.Path）；state.Files 有条目 ⇒ ClassManaged 且
//     Src 取记录值（§20.5 句 1 的映射来源），否则 ClassNewFile 且 Src 留空
//     （待 §20.7 路径解析）。
//   - prune 命中（pruneMatcher 来自 `.xinit/prune.txt`，与 update 的
//     expandPrune 同源）⇒ 装配即定出口 ExitPruned。prune.txt 是「已脱离
//     管理」的权威记录（§20.5 句 2），故命中即不作为导出候选，在册与否
//     一视同仁 —— 用户已声明该路径不在回流范围。
//   - 其余条目保持候选态 ExitExport（零值），由后续阶段定出口。
func BuildItems(st *state.State, chs []Change, pruneMatcher *fsutil.Matcher) []Item {
	items := make([]Item, 0, len(chs))
	for _, ch := range chs {
		it := Item{Dst: ch.Path, Change: ch, Exit: ExitExport}
		if fst, ok := st.Files[ch.Path]; ok {
			it.Src = fst.Src
			it.Class = ClassManaged
		} else {
			it.Class = ClassNewFile
		}
		if pruneMatcher.Match(ch.Path) {
			it.Exit, it.ExitNote = ExitPruned, notePruned
		}
		items = append(items, it)
	}
	return items
}

// ResolveDomain 执行 §20.5 域判定四句，就地写回 Item.Exit/ExitNote。
//
// 判据：
//  1. 匹配域 = 映射后的 Src（与 exclude 的 Src 口径一致）；映射来源 =
//     state.Files[Dst].Src（BuildItems 已装好），受管路径可被 flagMap
//     （`--map Dst=Src`）显式覆盖 —— 那是「模板来源已不存在」文案点名的出口。
//  2. 无 Src 映射的路径各有出口：新增（ClassNewFile，Src 为空）留给 §20.7
//     路径解析（T3），本函数不动它（解得 Src 后再调本函数即完成域判定，
//     故本函数幂等）；prune 在 BuildItems 已定出口；受管却无 Src 映射
//     （state 损坏面）按来源已消失 fail-closed。
//  3. 清单来源 = 记录 commit 的清单（调用方从记录 commit 读出并 Parse +
//     Compile 后传入；vars 用 state.Vars 原样，不重跑 EvalVars）。
//  4. 清单 upstream.paths 与 flagPatterns（`--paths`）并存 = **并集**，
//     两侧独立编译（各自的 ! 取反只作用于自己那组），命中来源写进 ExitNote
//     供报告逐行注明。清单没有 upstream.paths 键且未给 --paths ⇒
//     missingKey=true，调用方以 rc=1 报「模板未声明 upstream.paths」（出口：
//     `--paths` 或升级模板）；不得静默把全部路径判成域外。
//
// tmplFS = 记录 commit 的模板树，供句 2 的另一出口使用：「映射后的 Src 在
// 记录 commit 的模板树中不存在」（KeepWarned 类）⇒ ExitSourceGone 警告不导出。
// 该判据**仅作用于受管路径** —— 新增文件恒不在模板树，不得被此闸门拦截。
//
// 只处置 Exit == ExitExport 的条目；已定出口者（prune / 收敛成因② / 边界
// 分类等）一律不改写。
func ResolveDomain(items []Item, tmplFS fs.FS, compiled *manifest.Compiled, flagPatterns []string, flagMap map[string]string) (missingKey bool, err error) {
	manifestPats := compiled.Raw.Upstream.Paths
	if len(manifestPats) == 0 && len(flagPatterns) == 0 {
		// §20.5 句 4：缺键且未给 --paths。不得静默「全域外」——那会把
		// 用户的清单缺键误导成「提交写错」。
		return true, nil
	}

	byManifest, err := fsutil.CompileGlob(manifestPats)
	if err != nil {
		return false, fmt.Errorf("记录 commit 的清单 upstream.paths: %w", err)
	}
	byFlag, err := fsutil.CompileGlob(flagPatterns)
	if err != nil {
		return false, fmt.Errorf("--paths: %w", err)
	}

	for i := range items {
		it := &items[i]
		if it.Exit != ExitExport {
			continue // 前序阶段已定出口
		}
		fromMap := false
		if s, ok := flagMap[it.Dst]; ok && it.Class == ClassManaged {
			// --map 对受管路径的救援（§20.5 句 2 文案点名它）。新增文件的
			// --map 归 §20.7 规则 4（T3 的 ApplyMap，带「P 不得已存在于
			// 模板树」统一闸门），这里不越俎代庖。命中失败点（映射目标不在
			// 模板树）的文案见 noteMapTargetMissing——不再回指 `--map`。
			it.Src = s
			fromMap = true
		}
		if it.Src == "" {
			if it.Class == ClassNewFile {
				continue // 待 §20.7 路径解析
			}
			// 受管却没有 Src 映射：state 损坏面，按来源已消失 fail-closed。
			it.Exit, it.ExitNote = ExitSourceGone, noteSourceGone
			continue
		}
		if it.Class == ClassManaged {
			_, statErr := fs.Stat(tmplFS, it.Src)
			switch {
			case statErr == nil:
				// 在树中，继续域判定。
			case errors.Is(statErr, fs.ErrNotExist):
				if fromMap {
					it.Exit, it.ExitNote = ExitSourceGone, noteMapTargetMissing(it.Src)
				} else {
					it.Exit, it.ExitNote = ExitSourceGone, noteSourceGone
				}
				continue
			default:
				return false, fmt.Errorf(
					"无法判定模板路径 %q 是否存在：%w；判据 = 映射后 Src 须在记录 commit 的模板树中（§20.5 句 2）——"+
						"路径形状非法（绝对路径/含 .. 段）也会落这里（若来自 `--map`，请给合法模板路径）；无法判定时 fail-closed",
					it.Src, statErr)
			}
		}
		inManifest, inFlags := byManifest.Match(it.Src), byFlag.Match(it.Src)
		switch {
		case inManifest && inFlags:
			it.ExitNote = noteFromBoth
		case inManifest:
			it.ExitNote = noteFromManifest
		case inFlags:
			it.ExitNote = noteFromPaths
		default:
			it.Exit, it.ExitNote = ExitOutOfDomain, noteOutOfDomain
		}
	}
	return false, nil
}
