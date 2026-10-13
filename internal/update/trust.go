package update

// 来源：internal/update/update.go 的 Run 块 24（M2-P8 trust 抽取；I-2 实施批
// T6/M2c）+ hasConfirmed/hookDiffAgainstBase 整函数搬家（§2.6 trust.go 行）。
// 阶段判据（§3-M2）：函数体与源区间逐字对应，差异仅「参数接入」「返回值包装」
// 与附加裁定类——逐条见 task-6-report.md。

import (
	"io/fs"
	"os"

	"github.com/xbcio/xinit/internal/hooks"
	"github.com/xbcio/xinit/internal/manifest"
	"github.com/xbcio/xinit/internal/state"
	"github.com/xbcio/xinit/internal/template"
)

// trust 是 P8（§2.2）：信任确认（块 24）——bool = 终局（rc4，零写）。
func trust(s *session, t *Template, noHooks, yes bool) (bool, error) {
	// 12. 信任确认（§10.2 第 6 步、§10.5）——在备份与任何写入之前。
	if t.Manifest.Hooks.PostUpdate != "" {
		if s.st.TrustedHooks == nil {
			s.st.TrustedHooks = map[string]string{}
		}
		h := hooks.Hash(t.Manifest.Hooks.PostUpdate)
		if noHooks {
			// 跳过执行也记录内容（§10.5）：下次不带 --no-hooks 仍需确认。
			hooks.Seen(s.st.TrustedHooks, h)
		} else if hooks.NeedConfirm(s.st.TrustedHooks, h) {
			oldVer, diff := hookDiffAgainstBase(s.st, s.cacheRoot, t.Manifest.Hooks.PostUpdate)
			hooks.PrintNotice(os.Stdout, "post_update", t.Manifest.Hooks.PostUpdate, oldVer, diff)
			if !yes {
				hooks.PrintAsk(os.Stdout) // T4-R2：--yes 预先确认时不打指引
			}
			// 守卫是 ps != nil（而非 hooks.Interactive）：非 --interactive
			// 时二者等价，--interactive 时信任门才第一次能在管道下被驱动
			//（§19.2.3；信任判据本身不变——仍须显式 y/yes）。走 ps.confirm：
			// 会话已 EOF 锁存时省掉提问行、输出走 ps.out（M3/M4，M7）。
			ok := yes
			if !ok && s.interactive != nil {
				var cerr error
				if ok, cerr = s.interactive.ConfirmTrust(); cerr != nil {
					return false, cerr
				}
			}
			if !ok {
				return true, nil // 未做任何修改；Update() 特判此码（不打印动作报告）
			}
			hooks.Consent(s.st.TrustedHooks, h, hookVersion(t.Src))
		}
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
