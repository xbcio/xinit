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
	Kind     string
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
		// 第 4 段：状态缺失（adopt 在 M3 提供，这里不指向不存在的命令）
		return nil, fmt.Errorf(
			"%w\n这不是 xinit 项目，或 .xinit/state.json 缺失；请从模板 init 生成，或在 M3 的 adopt 落地后用它重建状态",
			err)
	}
	rep := &statusReport{
		Source:         st.Template.Source,
		Kind:           st.Template.Kind,
		Revision:       st.Template.Revision,
		Commit:         st.Template.Commit,
		EngineRecorded: st.EngineVersion,
		EngineCurrent:  EngineVersion,
	}

	// 第 1 段：模板侧。dir 源没有版本概念；git 源读本地镜像（--fetch 才联网）。
	if st.Template.Kind == template.KindDir.String() {
		rep.TemplateNote = "dir 源：无版本概念（commit 是树 hash）"
	} else {
		cacheRoot, err := template.DefaultCacheRoot()
		if err != nil {
			return nil, err
		}
		tags, err := template.ListTags(st.Template.Source, cacheRoot, o.Fetch)
		switch {
		case err != nil:
			rep.TemplateNote = fmt.Sprintf("无法读取模板 tag（本地镜像未命中？加 --fetch 重试）：%v", err)
		case !semver.IsValid(st.Template.Revision):
			rep.TemplateNote = fmt.Sprintf(
				"记录的 revision %q 不是 vX.Y.Z 格式，无法做版本比较（update 需要显式 --revision）",
				st.Template.Revision)
		default:
			for _, t := range template.SemverTags(tags) {
				if semver.Compare(t, st.Template.Revision) > 0 {
					rep.NewerTags = append(rep.NewerTags, t)
				}
			}
		}
	}

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

// Print 输出四段报告。行前缀（modified/generated/missing）是稳定标识。
func (r *statusReport) Print(w io.Writer) {
	fmt.Fprintf(w, "xinit status: %s\n", r.Source)

	fmt.Fprintf(w, "模板侧:\n")
	fmt.Fprintf(w, "  记录版本: %s（commit %s）\n", orNone(r.Revision), orNone(r.Commit))
	switch {
	case r.TemplateNote != "":
		fmt.Fprintf(w, "  %s\n", r.TemplateNote)
	case len(r.NewerTags) == 0:
		fmt.Fprintf(w, "  已是最新（没有更高的 semver tag）\n")
	default:
		fmt.Fprintf(w, "  可用更新 %d 个: %s（最新 %s）\n",
			len(r.NewerTags), strings.Join(r.NewerTags, ", "), r.NewerTags[len(r.NewerTags)-1])
	}

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
