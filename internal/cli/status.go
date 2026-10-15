package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/mod/semver"

	"github.com/xbcio/xinit/internal/state"
	"github.com/xbcio/xinit/internal/template"
	"github.com/xbcio/xinit/internal/worktree"
)

// StatusOptions 是 status 的输入（R13：作用于 cwd）。
// 只读：不加锁、不写任何文件（§11.4）。
type StatusOptions struct {
	ProjectDir string
	Fetch      bool
}

func newStatusCmd() *cobra.Command {
	var o StatusOptions
	cmd := &cobra.Command{
		Use:   "status",
		Short: "只读报告：模板版本、本地改动、状态健康",
		Long: "四段报告（§11.4）：模板侧、本地改动（generated 单列）、状态健康、状态缺失。\n\n" +
			"默认不联网（只读本地镜像缓存），--fetch 才更新缓存；\n" +
			"--fetch 发现有可用更新时退出码为 5。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			o.ProjectDir = "."
			return Status(o)
		},
	}
	cmd.Flags().BoolVar(&o.Fetch, "fetch", false, "联网更新模板镜像缓存，并检查可用更新")
	return cmd
}

// Status 打印报告；--fetch 且有可用更新 → 退出码 5（§11.5）。
func Status(o StatusOptions) error {
	rep, err := runStatus(o)
	if err != nil {
		return err
	}
	rep.Print(os.Stdout)
	if o.Fetch && len(rep.NewerTags) > 0 {
		return &ExitError{
			Code:   CodeChanged,
			Silent: true,
			msg:    fmt.Sprintf("status --fetch：有 %d 个可用更新", len(rep.NewerTags)),
		}
	}
	return nil
}

// statusReport 是 §11.4 四段的数据；Print 只管排版。
type statusReport struct {
	Source   string
	Revision string
	Commit   string

	NewerTags    []string // 高于记录版本的 semver tag（升序）
	TemplateNote string   // 模板侧无法比较时的说明（dir 源、镜像未命中、revision 非 semver）

	Modified  []string // 相对 base 改动、且不在 generated
	Generated []string // generated 路径的改动（钩子产物，单列）
	Missing   []string // 孤儿：files 里有、磁盘上没有

	PruneCount     int
	MixedEOL       []string
	EngineRecorded string
	EngineCurrent  string
}

func runStatus(o StatusOptions) (*statusReport, error) {
	projectDir, err := filepath.Abs(o.ProjectDir)
	if err != nil {
		return nil, err
	}
	st, err := state.Load(projectDir)
	if err != nil {
		// 第 4 段：状态缺失（adopt 见 §5.3）；损坏 → .prev 人工恢复指引
		// （评审 I-1：指引单一来源，六调用点不得分叉）。
		return nil, fmt.Errorf("%w\n%s", err, state.LoadHint(err))
	}
	rep := &statusReport{
		Source:         st.Template.Source,
		Revision:       st.Template.Revision,
		Commit:         st.Template.Commit,
		EngineRecorded: st.EngineVersion,
		EngineCurrent:  EngineVersion,
	}

	// 第 1 段：模板侧。dir 源没有版本概念；git 源读本地镜像（--fetch 才联网）。
	side, err := inspectTemplateSide(st.Template, o.Fetch, "status")
	if err != nil {
		return nil, err
	}
	rep.NewerTags, rep.TemplateNote = side.NewerTags, side.Note

	// 第 2 段：本地改动。精确列举 + 逐路径 hash（与 update 的 ours 快照同法）。
	paths := sortedStringKeys(st.Files)
	snap, _, err := worktree.Scan(projectDir, paths)
	if err != nil {
		return nil, err
	}
	for _, p := range paths {
		fst := st.Files[p]
		meta, ok := snap[p]
		if !ok {
			rep.Missing = append(rep.Missing, p) // 孤儿
			continue
		}
		if meta.Hash == fst.Hash {
			continue
		}
		if fst.Generated {
			rep.Generated = append(rep.Generated, p)
		} else {
			rep.Modified = append(rep.Modified, p)
		}
	}

	// 第 3 段：状态健康。
	patterns, err := state.ReadPrune(projectDir)
	if err != nil {
		return nil, err
	}
	rep.PruneCount = len(patterns)
	for _, p := range paths {
		if st.Files[p].MixedEOL {
			rep.MixedEOL = append(rep.MixedEOL, p)
		}
	}
	return rep, nil
}

// templateSide 是报告里「模板侧」一段的数据，status 与 template 命令共用。
type templateSide struct {
	NewerTags []string // 高于记录版本的稳定 semver tag（升序）
	Note      string   // 无法比较时的说明（dir 源、镜像未命中、revision 非 semver）
}

// inspectTemplateSide 只读地查看模板侧：dir 源没有版本概念；git 源读本地镜像，
// fetch=true 才联网刷新已有镜像。
//
// cmdName 是调用方的命令名（"status" / "template"），只用于镜像缺失时把降级
// 说明里的命令名说对——该文案为两个命令共用（措辞单一来源）。
func inspectTemplateSide(ref state.TemplateRef, fetch bool, cmdName string) (templateSide, error) {
	var side templateSide
	if ref.Kind == template.KindDir.String() {
		side.Note = "dir 源：无版本概念（commit 是树 hash）"
		return side, nil
	}
	cacheRoot, err := template.DefaultCacheRoot()
	if err != nil {
		return side, err
	}
	// allowClone=false：默认不联网、不创建缓存根（§11.4；评审 M-1/F2）——
	// 镜像缺失走降级说明，不静默 clone。
	tags, err := template.ListTags(ref.Source, cacheRoot, fetch, false)
	switch {
	case err != nil:
		side.Note = fmt.Sprintf(
			"无法读取模板 tag（本地没有模板镜像缓存——%s 不创建缓存、--fetch 只刷新已有镜像；"+
				"先运行 xinit update 建立缓存后重试）：%v", cmdName, err)
	case !semver.IsValid(ref.Revision):
		side.Note = fmt.Sprintf(
			"记录的 revision %q 不是 vX.Y.Z 格式，无法做版本比较（update 需要显式 --revision）",
			ref.Revision)
	default:
		for _, t := range template.SemverTags(tags) {
			if semver.Compare(t, ref.Revision) > 0 {
				side.NewerTags = append(side.NewerTags, t)
			}
		}
	}
	return side, nil
}

// printTemplateSide 输出「模板侧」一段（status 与 template 共用，措辞一处维护）。
func printTemplateSide(w io.Writer, revision, commit string, side templateSide) {
	fmt.Fprintf(w, "模板侧:\n")
	fmt.Fprintf(w, "  记录版本: %s（commit %s）\n", orNone(revision), orNone(commit))
	switch {
	case side.Note != "":
		fmt.Fprintf(w, "  %s\n", side.Note)
	case len(side.NewerTags) == 0:
		fmt.Fprintf(w, "  已是最新（没有更高的 semver tag）\n")
	default:
		fmt.Fprintf(w, "  可用更新 %d 个: %s（最新 %s）\n",
			len(side.NewerTags), strings.Join(side.NewerTags, ", "), side.NewerTags[len(side.NewerTags)-1])
	}
}

// Print 输出四段报告。行前缀（modified/generated/missing）是稳定标识。
func (r *statusReport) Print(w io.Writer) {
	fmt.Fprintf(w, "xinit status: %s\n", r.Source)

	printTemplateSide(w, r.Revision, r.Commit, templateSide{NewerTags: r.NewerTags, Note: r.TemplateNote})

	fmt.Fprintf(w, "本地改动（相对 base）: %d 条\n", len(r.Modified))
	for _, p := range r.Modified {
		fmt.Fprintf(w, "  %-10s %s\n", "modified", p)
	}
	fmt.Fprintf(w, "generated 路径的改动（钩子产物，单列）: %d 条\n", len(r.Generated))
	for _, p := range r.Generated {
		fmt.Fprintf(w, "  %-10s %s\n", "generated", p)
	}

	fmt.Fprintf(w, "状态健康:\n")
	fmt.Fprintf(w, "  prune 名单: %d 条\n", r.PruneCount)
	if len(r.MixedEOL) == 0 {
		fmt.Fprintf(w, "  混合 EOL: 0 个文件\n")
	} else {
		fmt.Fprintf(w, "  混合 EOL: %d 个文件（按二进制处理，§9.1）: %s\n",
			len(r.MixedEOL), strings.Join(r.MixedEOL, ", "))
	}
	if r.EngineRecorded == r.EngineCurrent {
		fmt.Fprintf(w, "  engine_version: %s（与当前引擎一致）\n", r.EngineRecorded)
	} else {
		fmt.Fprintf(w, "  engine_version: 记录 %s，当前 %s —— 不一致：渲染结果可能有漂移，建议对照模板核对（§7.6）\n",
			r.EngineRecorded, r.EngineCurrent)
	}
	fmt.Fprintf(w, "  孤儿（files 里有、磁盘上没有）: %d 条\n", len(r.Missing))
	for _, p := range r.Missing {
		fmt.Fprintf(w, "  %-10s %s（update 会按决策表处理：行 10/11/12）\n", "missing", p)
	}
}
