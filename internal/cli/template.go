package cli

import (
	"fmt"
	"io"
	"path/filepath"

	"github.com/spf13/cobra"
	"golang.org/x/mod/semver"

	"github.com/xbcio/xinit/internal/state"
	"github.com/xbcio/xinit/internal/template"
)

// TemplateOptions 是 template 的输入（作用于 cwd，与 status 同）。
// 只读：不加锁、不写任何文件；退出码只用 0 / 1。
type TemplateOptions struct {
	ProjectDir string
	Changelog  bool   // 列出记录版本到目标之间的变更
	Revision   string // 变更区间的终点；空 = 最新稳定 tag（仅与 Changelog 联用）
	Fetch      bool   // 联网刷新已有的模板镜像
}

func newTemplateCmd() *cobra.Command {
	var o TemplateOptions
	cmd := &cobra.Command{
		Use:   "template",
		Short: "只读查看模板侧：来源、记录版本、可用更新；--changelog 列出版本间变更",
		Long: "不带旗标：输出模板侧摘要（来源、记录版本与 commit、更高的稳定 semver tag），\n" +
			"口径与 xinit status 的「模板侧」一段相同。\n\n" +
			"--changelog：再列出项目记录的版本到目标版本之间的变更——按稳定 semver tag 分段，\n" +
			"段内是该区间的提交标题（不含 merge 提交，新→旧）。目标默认是最新稳定 tag，\n" +
			"可用 --revision 指定（tag、分支名或 sha）。\n\n" +
			"默认不联网（只读本地镜像缓存，缺失时不会克隆）；--fetch 才刷新已有镜像。\n" +
			"dir 源没有版本历史，--changelog 不适用。\n\n" +
			"xinit 自身的变更日志见 xinit changelog。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			o.ProjectDir = "."
			return Template(o, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&o.Changelog, "changelog", false, "列出记录版本到目标版本之间的变更")
	cmd.Flags().StringVar(&o.Revision, "revision", "", "变更区间的终点（tag / 分支 / sha；默认最新稳定 tag；需与 --changelog 联用）")
	cmd.Flags().BoolVar(&o.Fetch, "fetch", false, "联网刷新已有的模板镜像缓存")
	return cmd
}

// Template 把报告写到 w。所有读取与计算先于任何输出完成，出错时不留半截报告。
func Template(o TemplateOptions, w io.Writer) error {
	if o.Revision != "" && !o.Changelog {
		return fmt.Errorf("--revision 指定变更区间的终点，需与 --changelog 一起使用")
	}
	projectDir, err := filepath.Abs(o.ProjectDir)
	if err != nil {
		return err
	}
	st, err := state.Load(projectDir)
	if err != nil {
		// 与 status 同一份指引（LoadHint 单一来源）。
		return fmt.Errorf("%w\n%s", err, state.LoadHint(err))
	}
	ref := st.Template

	if !o.Changelog {
		side, err := inspectTemplateSide(ref, o.Fetch, "template")
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "xinit template: %s\n", ref.Source)
		printTemplateSide(w, ref.Revision, ref.Commit, side)
		return nil
	}

	if ref.Kind == template.KindDir.String() {
		return fmt.Errorf("dir 源没有版本历史，无法列出变更（--changelog 仅适用于 git 模板源）")
	}
	cacheRoot, err := template.DefaultCacheRoot()
	if err != nil {
		return err
	}
	// 只读定位：镜像缺失就是 ErrMirrorMissing（%w 保留，调用方可 errors.Is）。
	// fetch 在这里一次做完，下面的摘要不再重复联网。
	res, err := template.BuildChangelog(ref.Source, cacheRoot, o.Fetch, ref.Commit, o.Revision)
	if err != nil {
		return fmt.Errorf("读取模板历史: %w", err)
	}
	side, err := inspectTemplateSide(ref, false, "template")
	if err != nil {
		return err
	}

	fmt.Fprintf(w, "xinit template: %s\n", ref.Source)
	printTemplateSide(w, ref.Revision, ref.Commit, side)
	printChangelog(w, ref, res, o.Revision != "")
	return nil
}

// printChangelog 输出变更一段：按版本分段，新版本在前，段内提交新→旧。
// explicit 表示终点是用户用 --revision 点名的（影响「无变更」时的措辞）。
func printChangelog(w io.Writer, ref state.TemplateRef, res *template.ChangelogResult, explicit bool) {
	switch res.Relation {
	case template.RelSame:
		if explicit {
			fmt.Fprintf(w, "变更: 无（记录版本与目标 %s 是同一个提交）\n", res.ToName)
		} else {
			fmt.Fprintf(w, "变更: 无（记录版本已是最新稳定版本 %s）\n", res.ToName)
		}
		return
	case template.RelBehind:
		if explicit {
			fmt.Fprintf(w, "变更: 无（目标 %s 比记录版本更旧——这是回退目标，没有「之间的变更」可列）\n", res.ToName)
		} else {
			fmt.Fprintf(w, "变更: 无（记录版本已不低于最新稳定版本 %s）\n", res.ToName)
		}
		return
	case template.RelDiverged:
		fmt.Fprintf(w, "变更: 无（记录版本与目标 %s 不在同一条历史线上，无法列出之间的变更）\n", res.ToName)
		return
	}

	fmt.Fprintf(w, "变更（%s → %s）:\n", fromLabel(ref), res.ToName)
	for i := len(res.Points) - 1; i >= 0; i-- {
		p := res.Points[i]
		fmt.Fprintf(w, "\n%s\n", p.Name)
		if len(p.Subjects) == 0 {
			fmt.Fprintf(w, "  （没有非 merge 提交）\n")
			continue
		}
		for _, s := range p.Subjects {
			fmt.Fprintf(w, "  - %s\n", s)
		}
	}
}

// fromLabel 是区间起点的显示名：可比较的 vX.Y.Z 用 tag 原文，其余（HEAD、分支、
// sha）用 8 位短 commit——「HEAD」在区间标题里没有意义。
func fromLabel(ref state.TemplateRef) string {
	if semver.IsValid(ref.Revision) {
		return ref.Revision
	}
	if len(ref.Commit) > 8 {
		return ref.Commit[:8]
	}
	return orNone(ref.Commit)
}
