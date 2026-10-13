package update

// 来源：internal/update/update.go 的 Run 块 24（M2-P8 trust 抽取；I-2 实施批
// T6/M2c）+ hasConfirmed/hookDiffAgainstBase/hookVersion 整函数搬家（§2.6
// trust.go 行；hookVersion 为该表未列项——按消费者 = trust 归此，登记）。
// 阶段判据（§3-M2）：函数体与源区间逐字对应，差异仅「参数接入」「返回值包装」
// 与附加裁定类——逐条见 task-6-report.md。
// M5：编排内环抽至 hooks.ConfirmGate（本文件仅剩 update 侧差异）。

import (
	"io/fs"

	"github.com/xbcio/xinit/internal/hooks"
	"github.com/xbcio/xinit/internal/manifest"
	"github.com/xbcio/xinit/internal/state"
	"github.com/xbcio/xinit/internal/template"
)

// trust 是 P8（§2.2）：信任确认（块 24）——bool = 终局（rc4，零写）。
// 编排单点 = hooks.ConfirmGate（I-2 M5）；本函数只负责 update 侧差异：
// trusted 非空保证、diff 懒求值、interactive 守卫、终局语义。
func trust(s *session, t *Template, noHooks, yes bool) (bool, error) {
	// 12. 信任确认（§10.2 第 6 步、§10.5）——在备份与任何写入之前。
	if t.Manifest.Hooks.PostUpdate != "" {
		if s.st.TrustedHooks == nil {
			s.st.TrustedHooks = map[string]string{}
		}
		// 守卫是 interactive != nil（而非 Interactive 接口判定）：非 --interactive
		// 时二者等价，--interactive 时信任门才第一次能在管道下被驱动
		//（§19.2.3；信任判据本身不变——仍须显式 y/yes）。
		var confirm func() (bool, error)
		if s.interactive != nil {
			confirm = s.interactive.ConfirmTrust
		}
		return hooks.ConfirmGate(s.st.TrustedHooks, hooks.GateInput{
			Name:    "post_update",
			Block:   t.Manifest.Hooks.PostUpdate,
			Version: hookVersion(t.Src),
			Out:     s.io.Out,
			Confirm: confirm,
			PrepareDiff: func() (string, string) {
				return hookDiffAgainstBase(s.st, s.cacheRoot, t.Manifest.Hooks.PostUpdate)
			},
		}, noHooks, yes)
	}
	return false, nil
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
	// allowClone=true：镜像优先，缺失回退首次 clone（取旧版本清单的
	// 功能性回退；失败本就不阻断确认流程）。
	src, err := template.Open(st.Template.Source, st.Template.Commit, cacheRoot, false, true)
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

// hookVersion 是信任记录的版本值：git 源 = revision，dir 源 = 树 hash
// （§4.3；dir 源 Revision 为空，用 Commit 保证记录非空——空串是 Seen 的
// marker，两者不可混）。
func hookVersion(src *template.Source) string {
	if src.Revision != "" {
		return src.Revision
	}
	return src.Commit
}
