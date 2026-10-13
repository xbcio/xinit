package update

// 来源：internal/update/update.go 的 Run 块 29-30（M2-P10 advance 抽取；
// I-2 实施批 T6/M2c）+ advanceState 整函数搬家（§2.6 advance.go 行）。
// 阶段判据（§3-M2）：函数体与源区间逐字对应，差异仅「参数接入」与「返回值
// 包装」两类——逐条见 task-6-report.md。

import (
	"fmt"
	"io"

	"github.com/xbcio/xinit/internal/fsutil"
	"github.com/xbcio/xinit/internal/plan"
	"github.com/xbcio/xinit/internal/render"
	"github.com/xbcio/xinit/internal/renderstore"
	"github.com/xbcio/xinit/internal/state"
	"github.com/xbcio/xinit/internal/template"
)

// advance 是 P10（§2.2）：第 9 步推进 state 与 .xinit/base/（块 29）+
// 第 10 步幂等写 .git/info/exclude（块 30）。读 d.Actions——P9 的
// restoreRefused 已按写者表就地改写 NewBase/Note（I12）。
func advance(s *session, t *Template, b *Base, d *Decision, w *Delivery) error {
	// 16. 第 9 步：推进 state 与 .xinit/base/（先 base 后 state，含写入自检）
	if err := advanceState(s.dir, s.st, b, d.Actions, t.Rendered, t.SrcOf, t.Vars, t.Src, w.Pending, s.cacheRoot, s.engineVersion, s.io.Err); err != nil {
		return err
	}

	// 17. 第 10 步：幂等写 .git/info/exclude
	if err := state.EnsureGitExcluded(s.dir); err != nil {
		return fmt.Errorf("写 .git/info/exclude: %w", err)
	}
	return nil
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
// errW = 快照写侧 best-effort 警告通道（R-M4-3：末位形参，不引入 session
// 依赖；调用点传 s.io.Err）。
func advanceState(projectDir string, st *state.State, baseSnap *Base,
	actions []plan.Action, theirsRendered map[string][]byte,
	newSrc map[string]string, vars render.Vars, src *template.Source, pending bool,
	cacheRoot, engineVersion string, errW io.Writer,
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
		renderstore.WriteBestEffort(st, cacheRoot, theirsRendered, errW)
	}
	return nil
}
