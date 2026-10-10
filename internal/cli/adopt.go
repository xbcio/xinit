package cli

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	"github.com/xbcio/xinit/internal/fsutil"
	"github.com/xbcio/xinit/internal/manifest"
	"github.com/xbcio/xinit/internal/plan"
	"github.com/xbcio/xinit/internal/render"
	"github.com/xbcio/xinit/internal/state"
	"github.com/xbcio/xinit/internal/template"
	"github.com/xbcio/xinit/internal/worktree"
)

// AdoptOptions 是 adopt 的全部输入（§5.3）。
type AdoptOptions struct {
	ProjectDir string
	Template   string
	Revision   string
	ConfigPath string
}

func newAdoptCmd() *cobra.Command {
	var o AdoptOptions
	cmd := &cobra.Command{
		Use:   "adopt",
		Short: "把已有项目按模板渲染结果重建为受管状态（重建基线）",
		Long: "把模板在指定版本的渲染结果记为 base（不是磁盘内容），并重建 .xinit/state.json。\n" +
			"磁盘与渲染结果的全部差异都视为「本地改动」：update 起普通路径按「本地改过」保留，\n" +
			"模板之后也改到它时走合并/冲突（行 6/7）；generated 路径的模板改动按行 5 覆盖\n" +
			"（本地版本另存 .xinit/overwritten/）。\n" +
			"注意：--revision 选错版本时，两版之间模板的改动在磁盘上并不存在，会被当成\n" +
			"「本地把模板改回去了」而永久保留——那些模板更新从此不会再被应用（§5.3）。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			o.ProjectDir = "."
			return Adopt(o)
		},
	}
	cmd.Flags().StringVar(&o.Template, "template", "", "模板来源（本地路径或 git URL，必填）")
	cmd.Flags().StringVar(&o.Revision, "revision", "", "目标版本（默认取模板最新的 semver tag）")
	cmd.Flags().StringVar(&o.ConfigPath, "config", "", "变量文件（扁平 YAML，必填——vars 是重建三要素之一）")
	_ = cmd.MarkFlagRequired("template")
	_ = cmd.MarkFlagRequired("config")
	return cmd
}

// Adopt 按 §5.3 重建基线。不跑任何钩子；TrustedHooks 留空（下次 update 会
// 按 §10.5 正常要求确认）。
func Adopt(o AdoptOptions) error {
	if o.Template == "" {
		return fmt.Errorf("必须指定 --template")
	}
	if o.ConfigPath == "" {
		return fmt.Errorf("必须指定 --config（vars 是重建三要素之一，§5.3）")
	}
	dest, err := filepath.Abs(o.ProjectDir)
	if err != nil {
		return err
	}

	// 写 .xinit/ 前的两个既有检查（与 init/update 同判据）。
	if tracked, err := state.IsGitTracked(dest, state.DirName); err != nil {
		return fmt.Errorf("无法确认 %s/ 是否被 git 跟踪: %w", state.DirName, err)
	} else if tracked {
		return fmt.Errorf(
			"%s/ 已被 git 跟踪：拒绝写入被版本控制的 %s/（规范 §5.5）；请执行 `git rm -r --cached %s` 后重试",
			state.DirName, state.DirName, state.DirName)
	}
	// 已受管理的项目拒绝重建：state 可读即说明无需 adopt。
	if _, err := state.Load(dest); err == nil {
		return fmt.Errorf("已存在可用的 %s/state.json（项目已受管理，无需 adopt）；如确要重建基线，先删除该文件再运行",
			state.DirName)
	} else if !os.IsNotExist(err) {
		// 文件存在但读不动（损坏/版本更高）：给出可见警告后覆盖重建。
		fmt.Fprintf(os.Stderr, "xinit: 警告: 现有 %s/state.json 无法读取（%v），将被 adopt 重建覆盖\n", state.DirName, err)
	}

	lock, err := state.LockProject(dest)
	if err != nil {
		return err
	}
	defer lock.Unlock()

	cacheRoot, err := template.DefaultCacheRoot()
	if err != nil {
		return err
	}

	// 选版：--revision 显式优先。缺省按源类型：dir 源没有版本概念（Open
	// 忽略 revision）；git 源取最高的非预发布 semver tag——§11.3 候选取
	// 最高的退化形式（adopt 没有「当前 revision」，不需要升级比较/F6，
	// 也不能直接调 template.SelectRevision：它要求 current 可解析）。
	// git 源无 tag / 全是预发布 → 报错要求显式 --revision（不猜）。
	target := o.Revision
	if target == "" && !template.IsDirSource(o.Template) {
		tags, err := template.ListTags(o.Template, cacheRoot, false, true)
		if err != nil {
			return err // 本地 git 仓/裸仓/URL：列 tag 失败（含克隆失败）是真错误
		}
		cands := template.SemverTags(tags)
		switch {
		case len(cands) > 0:
			target = cands[len(cands)-1] // SemverTags 升序：(版本序, 名字节序) 最大在尾
		case len(tags) == 0:
			return fmt.Errorf("模板里没有任何 tag；请显式指定 --revision")
		default:
			return fmt.Errorf("未找到符合 vX.Y.Z 格式的 tag；请显式指定 --revision")
		}
	}
	src, err := template.Open(o.Template, target, cacheRoot, false)
	if err != nil {
		return err
	}

	manifestData, err := fs.ReadFile(src.FS, "xinit.yml")
	if err != nil {
		return fmt.Errorf("模板缺少 xinit.yml: %w", err)
	}
	m, err := manifest.Parse(manifestData)
	if err != nil {
		return fmt.Errorf("解析模板的 xinit.yml: %w", err)
	}
	compiled, err := m.Compile()
	if err != nil {
		return fmt.Errorf("编译模板的 xinit.yml: %w", err)
	}
	if src.Kind == template.KindDir {
		if src.Commit, err = template.TreeHashOf(src.DirRoot, compiled.Exclude); err != nil {
			return err
		}
	}
	provided, err := LoadConfig(o.ConfigPath)
	if err != nil {
		return err
	}
	vars, err := render.EvalVars(compiled, provided)
	if err != nil {
		return err
	}

	entries, warnings, err := render.Paths(src.FS, compiled, vars)
	if err != nil {
		return fmt.Errorf("渲染模板路径: %w", err)
	}
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "xinit: 警告: %s: %s\n", w.Path, w.Msg)
	}
	rendered := make(map[string][]byte, len(entries))
	for _, e := range entries {
		data, _, warns, err := render.Content(src.FS, compiled, vars, e)
		if err != nil {
			return err
		}
		for _, w := range warns {
			fmt.Fprintf(os.Stderr, "xinit: 警告: %s: %s\n", w.Path, w.Msg)
		}
		rendered[e.Dst] = data
	}

	// state：base = 渲染结果（不是磁盘内容，§5.3）。
	st := state.New(EngineVersion)
	st.Template = state.TemplateRef{
		Source:   o.Template,
		Kind:     src.Kind.String(),
		Revision: src.Revision,
		Commit:   src.Commit,
	}
	st.Vars = vars
	for _, e := range entries {
		content := rendered[e.Dst]
		st.Files[e.Dst] = state.FileState{
			Src:       e.Src,
			Hash:      fsutil.CompareHash(content, e.Binary),
			Exec:      e.Exec,
			Binary:    e.Binary,
			MixedEOL:  fsutil.DetectEOL(content) == fsutil.EOLMixed,
			Generated: e.Generated,
		}
	}
	if err := state.SyncBase(dest, entries, rendered); err != nil {
		return fmt.Errorf("写 base: %w", err)
	}
	if err := st.Save(dest); err != nil {
		return fmt.Errorf("写状态: %w", err)
	}
	if err := state.EnsureGitExcluded(dest); err != nil {
		return fmt.Errorf("写 .git/info/exclude: %w", err)
	}

	// 磁盘 vs 渲染：差异即「本地改动」（§5.3 输出的一部分）。
	all := make([]string, 0, len(entries))
	theirs := make(plan.Snap, len(entries))
	for _, e := range entries {
		all = append(all, e.Dst)
		content := rendered[e.Dst]
		theirs[e.Dst] = plan.FileMeta{
			Kind:      e.Kind,
			Exec:      e.Exec,
			Binary:    e.Binary,
			MixedEOL:  fsutil.DetectEOL(content) == fsutil.EOLMixed,
			Generated: e.Generated,
			Hash:      fsutil.CompareHash(content, e.Binary),
		}
	}
	sort.Strings(all)
	oursSnap, _, err := worktree.Scan(dest, all)
	if err != nil {
		return err
	}
	var localDiff []string
	for _, p := range all {
		o, ok := oursSnap[p]
		t := theirs[p]
		if !ok || o.Kind != render.KindRegular || o.Hash != t.Hash || o.Exec != t.Exec {
			localDiff = append(localDiff, p)
		}
	}

	fmt.Printf("xinit adopt: %s@%s\n", o.Template, orNone(src.Revision))
	if src.Revision == "" {
		fmt.Printf("（dir 源：commit 为树 hash %s）\n", src.Commit)
	}
	fmt.Printf("基线已重建：%d 个文件（.xinit/base/ 与 .xinit/state.json）\n", len(entries))
	// 本地改动的后续走向分两类（发布批次 O7/场景 6.2-6.3）：普通路径按行 3
	// 「本地改过」保留，模板之后也改到它时走三方合并/冲突（行 6/7）；generated
	// 路径不同——模板改动按行 5 覆盖本地内容（原版本另存 overwritten/）。
	// 旧文案「update 起按行 9 保留，模板对它们的更新不会应用」对两类都不准
	// （行 9 只在模板删除该路径时适用；generated 恰是被覆盖）。
	genCount := 0
	for _, p := range localDiff {
		if theirs[p].Generated {
			genCount++
		}
	}
	if genCount == 0 {
		fmt.Printf("本地改动 %d 个文件（磁盘与模板渲染结果不同）：update 起按「本地改过」保留，模板之后也改到它时走合并/冲突（行 6/7）\n", len(localDiff))
	} else {
		fmt.Printf("本地改动 %d 个文件（磁盘与模板渲染结果不同；其中 generated 路径 %d 个）：普通路径 update 起按「本地改过」保留、模板之后也改到它时走合并/冲突（行 6/7）；generated 路径的模板改动按行 5 覆盖本地内容（本地版本另存 .xinit/overwritten/）\n",
			len(localDiff), genCount)
	}
	for _, p := range localDiff {
		fmt.Printf("  %-20s %s\n", "local-diff", p)
	}
	fmt.Print("警告：--revision 选错版本时，两版之间模板的改动在磁盘上不存在，会被当成\n" +
		"「本地把模板改回去了」而永久保留——那些模板更新从此不会再被应用（§5.3）。\n")
	if m.Hooks.PostInit != "" || m.Hooks.PostUpdate != "" {
		fmt.Print("模板声明了钩子：adopt 不执行钩子、也不写信任记录——下次 update 会要求确认（§10.5）。\n")
	}
	return nil
}
