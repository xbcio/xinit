package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/xbcio/xinit/internal/fsutil"
	"github.com/xbcio/xinit/internal/manifest"
	"github.com/xbcio/xinit/internal/render"
	"github.com/xbcio/xinit/internal/state"
	"github.com/xbcio/xinit/internal/template"
)

// EngineVersion 是渲染引擎版本，写进 state.json。
//
// 改动渲染行为时**必须**同时改它：状态里记录了它，用来检测
// 「引擎漂移」—— 同一模板在新旧引擎下渲染结果不同。
const EngineVersion = "0.1.0"

func newInitCmd() *cobra.Command {
	var (
		templateSrc string
		configPath  string
		offline     bool
		noHooks     bool
		force       bool
		yes         bool
	)
	cmd := &cobra.Command{
		Use:   "init [目标目录]",
		Short: "从模板生成新项目",
		Long: "按模板项目的 xinit.yml 与配置渲染出一个新的派生项目。\n\n" +
			"目标目录不存在时会创建；已存在且非空时需 --force。\n" +
			"只含 .git 的目录视为空目录。",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dest := "."
			if len(args) > 0 {
				dest = args[0]
			}
			return Init(dest, templateSrc, configPath, "", offline, noHooks, force, yes)
		},
	}
	cmd.Flags().StringVar(&templateSrc, "template", "", "模板来源（本地路径或 git URL，必填）")
	cmd.Flags().StringVar(&configPath, "config", "", "变量文件（扁平 YAML）")
	cmd.Flags().BoolVar(&offline, "offline", false, "不联网，只用本地模板镜像缓存")
	cmd.Flags().BoolVar(&noHooks, "no-hooks", false, "跳过 post_init")
	cmd.Flags().BoolVar(&force, "force", false, "允许在非空目录执行（仍不覆盖同名文件）")
	cmd.Flags().BoolVar(&yes, "yes", false, "跳过交互确认")
	_ = cmd.MarkFlagRequired("template")
	return cmd
}

// Init 生成一个新的派生项目。
//
// 步骤（规范 §6）：
//
//  1. 取模板
//  2. 读 xinit.yml
//  3. 求值 vars
//  4. （M3）钩子信任确认 —— 在写任何文件之前
//  5. 渲染 Paths，做碰撞检查
//  6. 写文件；记录本次创建的路径
//  7. 写 .xinit/state.json
//  8. 把渲染结果逐字节复制进 .xinit/base/
//  9. 幂等写 .git/info/exclude
//  10. （M3）跑 post_init
//
// 失败语义：第 6–9 步失败时**只回删本次创建的路径**，绝不删除预先存在
// 的任何内容。第 10 步（钩子）失败不回删 —— 渲染产物是正确的。
// M1 不实现第 4、10 步（钩子属于 M3，见规范 §16 的门禁）。
func Init(
	projectDir, templateSrc, configPath, revision string,
	offlineReady, noHooks, force, yes bool,
) error {
	if templateSrc == "" {
		return errors.New("必须指定 --template")
	}

	dest, err := filepath.Abs(projectDir)
	if err != nil {
		return err
	}
	if err := checkDestUsable(dest, force); err != nil {
		return err
	}

	// 写前安全检查（规范 §5.5）：.xinit/ 被 git 跟踪时报错。
	// IsGitTracked 在非 git 仓/未跟踪时返回 (false, nil)；err 仅在
	// 「dest 是 git 仓但 git 命令无法执行」这类异常态出现 —— 本检查
	// fail-closed（旧代码在此吞错，属刻意收紧）。
	if tracked, err := state.IsGitTracked(dest, state.DirName); err != nil {
		return fmt.Errorf("无法确认 %s/ 是否被 git 跟踪: %w", state.DirName, err)
	} else if tracked {
		return fmt.Errorf(
			"%s/ 已被 git 跟踪：拒绝写入被版本控制的 %s/（规范 §5.5）；请执行 `git rm -r --cached %s` 后重试",
			state.DirName, state.DirName, state.DirName)
	}

	cacheRoot, err := template.DefaultCacheRoot()
	if err != nil {
		return err
	}

	// 1. 取模板
	src, err := template.Open(templateSrc, revision, cacheRoot, !offlineReady)
	if err != nil {
		return err
	}

	// 2. 读清单
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

	// dir 源的树 hash 是它的「commit」，必须等清单编译出来才能算：
	// §5.4 要求排除 exclude 命中的文件（git 源的 commit 在 Open 时已定）。
	if src.Kind == template.KindDir {
		if src.Commit, err = template.TreeHashOf(src.DirRoot, compiled.Exclude); err != nil {
			return err
		}
	}

	// 3. 求值 vars
	provided := map[string]string{}
	if configPath != "" {
		if provided, err = LoadConfig(configPath); err != nil {
			return err
		}
	}
	vars, err := render.EvalVars(compiled, provided)
	if err != nil {
		return err
	}

	// 5. 渲染路径（含碰撞检查）
	entries, warnings, err := render.Paths(src.FS, compiled, vars)
	if err != nil {
		return fmt.Errorf("渲染模板路径: %w", err)
	}
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "xinit: 警告: %s: %s\n", w.Path, w.Msg)
	}

	// 渲染全部内容
	rendered := make(map[string][]byte, len(entries))
	// 零命中告警按**规则**汇总（规范 §7.4）：逐文件告警会在
	// paths: ["**"] 的规则上刷出与文件数同量的噪音
	hitTotals := make(map[int]int)
	for _, e := range entries {
		data, hits, warns, err := render.Content(src.FS, compiled, vars, e)
		if err != nil {
			return err
		}
		for _, h := range hits {
			hitTotals[h.Index] += h.Count
		}
		for _, w := range warns {
			fmt.Fprintf(os.Stderr, "xinit: 警告: %s: %s\n", w.Path, w.Msg)
		}
		rendered[e.Dst] = data
	}
	for i, cs := range compiled.Subst {
		if hitTotals[i] == 0 {
			fmt.Fprintf(os.Stderr,
				"xinit: 警告: 替换规则 %d (%q) 在渲染结果中命中 0 次 —— 模板里可能已无此字面量\n",
				i, cs.Spec.From)
		}
	}

	// 6. 写文件，记录本次创建的路径
	created, skipped, writeErr := writeTree(dest, entries, rendered, force)

	// 回删语义（规范 §6.1）：只回删**本次创建**的路径。
	//   - created 里的工作树文件；
	//   - 只清理 created 路径的空父目录链 —— 绝不扫描整个 dest 删「看起来
	//     空」的目录（--force 场景下那可能是用户自己建的）；
	//   - .xinit/（state.json 与 base/）同样是本次创建的产物：写状态或
	//     写 base 中途失败时，残留的 state.json 会宣称一棵已被回删的树
	//     —— 下一轮 init 会因此要求 --force，M2 的 update 更会被误导。
	//     但若 .xinit/ 在本次运行前就存在，它属于用户，不得删除。
	xinitExisted := pathExists(filepath.Join(dest, state.DirName))
	cleanup := func() {
		rollback(dest, created, xinitExisted)
	}

	if writeErr != nil {
		cleanup()
		return writeErr
	}

	// 7. 写 state.json
	st := state.New(EngineVersion)
	st.Template = state.TemplateRef{
		Source:   templateSrc,
		Kind:     src.Kind.String(),
		Revision: src.Revision,
		Commit:   src.Commit,
	}
	st.Vars = vars

	// managed 是「磁盘内容确实由我们写下的」那一部分。
	//
	// 被 --force 跳过的路径必须同时排除在 state.files **和** base/ 之外。
	// 只排除前者会留下一个孤儿 base 文件，而它描述的内容根本不在工作树里
	// —— 下一轮 update 会把它当成 legitimate 的 base 去比较。
	var managed []render.Entry
	for _, e := range entries {
		content, ok := rendered[e.Dst]
		if !ok {
			cleanup()
			return fmt.Errorf("内部错误：%q 有 Entry 但无渲染内容", e.Dst)
		}
		if skipped[e.Dst] {
			continue
		}
		managed = append(managed, e)
		st.Files[e.Dst] = state.FileState{
			Src:       e.Src,
			Hash:      renderContentHash(content, e.Binary),
			Exec:      e.Exec,
			Binary:    e.Binary,
			MixedEOL:  fsutil.DetectEOL(content) == fsutil.EOLMixed,
			Generated: e.Generated,
		}
	}
	if err := st.Save(dest); err != nil {
		cleanup()
		return fmt.Errorf("写状态: %w", err)
	}

	// 8. 写 base 树
	if err := state.SyncBase(dest, managed, rendered); err != nil {
		cleanup()
		return fmt.Errorf("写 base: %w", err)
	}

	// 9. 写 .git/info/exclude
	if err := state.EnsureGitExcluded(dest); err != nil {
		cleanup()
		return fmt.Errorf("写 .git/info/exclude: %w", err)
	}

	// 10. （M3）post_init

	fmt.Printf("已生成 %d 个文件到 %s\n", len(created), dest)
	if len(skipped) > 0 {
		names := make([]string, 0, len(skipped))
		for k := range skipped {
			names = append(names, k)
		}
		sort.Strings(names)
		fmt.Printf("跳过 %d 个已存在的文件（未覆盖）：\n", len(names))
		for _, n := range names {
			fmt.Printf("  %s\n", n)
		}
	}
	return nil
}

func renderContentHash(content []byte, binary bool) string {
	return fsutil.CompareHash(content, binary)
}

// checkDestUsable 校验目标目录。
//
// 只含 .git 的目录视为**空目录**：`git clone <空仓> && cd && xinit init .`
// 是常见用法，按字面算非空会强迫用户加 --force。
func checkDestUsable(dest string, force bool) error {
	info, err := os.Stat(dest)
	if os.IsNotExist(err) {
		return os.MkdirAll(dest, 0o755)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("目标 %s 不是目录", dest)
	}

	// checkDestUsable 只查 .xinit/state.json 不够：手删 state.json 而
	// .xinit/base/ 尚存时，--force 仍会把 writeTree 的全部文件判定为
	// 「已存在」而跳过，state.files 写成 {}，SyncBase.removeStale 据此
	// 把 base/ 整棵清空（I1 复验残余）。两种残留命中同一个「拒绝」结论，
	// 用统一措辞即可——用户要做的事（手动删 .xinit/ 后重试）相同。
	stateExists := pathExists(filepath.Join(dest, state.DirName, "state.json"))
	baseExists := pathExists(state.BaseDir(dest))
	if stateExists || baseExists {
		return fmt.Errorf(
			"目标目录 %s 已是 xinit 项目（存在 %s/state.json 或 %s/base）：init 不会重新初始化已初始化的项目"+
				"（会破坏 %s/base 基线），--force 也不生效；如确需从头再来请手动删除 %s/ 后重试"+
				"（同步模板更新请运行 xinit update，不必删掉重来）",
			dest, state.DirName, state.DirName, state.DirName, state.DirName)
	}

	ents, err := os.ReadDir(dest)
	if err != nil {
		return err
	}
	var meaningful []string
	for _, e := range ents {
		if e.Name() == ".git" {
			continue
		}
		meaningful = append(meaningful, e.Name())
	}
	if len(meaningful) > 0 && !force {
		return fmt.Errorf(
			"目标目录 %s 非空（含 %s）；加 --force 继续，或换一个空目录",
			dest, strings.Join(meaningful, ", "))
	}
	return nil
}

// writeTree 写派生树，返回 (本次创建的路径, 被跳过的路径, 错误)。
//
// 同名文件已存在时**逐个报冲突并跳过**，不静默覆盖 —— 失败即回删的
// 语义配上静默覆盖会删掉用户数据。
func writeTree(
	dest string,
	entries []render.Entry,
	rendered map[string][]byte,
	force bool,
) (created []string, skipped map[string]bool, err error) {
	skipped = map[string]bool{}

	for _, e := range entries {
		content, ok := rendered[e.Dst]
		if !ok {
			return created, skipped, fmt.Errorf("内部错误：%q 有 Entry 但无渲染内容", e.Dst)
		}

		full := filepath.Join(dest, filepath.FromSlash(e.Dst))

		// 输出路径安全：Clean 后必须仍在 dest 之内
		if !withinDir(dest, full) {
			return created, skipped, fmt.Errorf("输出路径 %q 越出目标目录", e.Dst)
		}

		// 大小写折叠：在 APFS/Windows 上，折叠后同名的已存在文件
		// **就是同一个文件**，原子写会把它替换掉
		if existing, ok := foldCollision(full); ok {
			if err := checkNoCollision(full, existing, force); err != nil {
				return created, skipped, err
			}
		}

		mode := fs.FileMode(0o644)
		if e.Exec {
			mode = 0o755
		}

		if _, statErr := os.Lstat(full); statErr == nil {
			skipped[e.Dst] = true
			fmt.Fprintf(os.Stderr, "xinit: 跳过已存在的文件 %s\n", e.Dst)
			continue
		} else if !os.IsNotExist(statErr) {
			return created, skipped, statErr
		}

		if err := state.WriteFileAtomic(full, content, mode); err != nil {
			return created, skipped, fmt.Errorf("写 %s: %w", e.Dst, err)
		}
		created = append(created, e.Dst)
	}

	return created, skipped, nil
}

// foldCollision 检查父目录里是否存在折叠后同名但拼写不同的文件。
func foldCollision(full string) (string, bool) {
	dir := filepath.Dir(full)
	base := filepath.Base(full)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	for _, e := range ents {
		if e.Name() == base {
			continue // 同名是正常的覆盖情形，由调用方处理
		}
		if fsutil.FoldEqual(e.Name(), base) {
			return filepath.Join(dir, e.Name()), true
		}
	}
	return "", false
}

func checkNoCollision(want, existing string, force bool) error {
	if force {
		fmt.Fprintf(os.Stderr,
			"xinit: 警告: %s 与已存在的 %s 在大小写不敏感的文件系统上是同一个文件\n",
			path.Base(want), path.Base(existing))
		return nil
	}
	return fmt.Errorf(
		"路径冲突：要写 %s，但已存在 %s —— 二者在大小写不敏感的文件系统上是同一个文件。"+
			"请先移走其中一个，或加 --force",
		path.Base(want), path.Base(existing))
}

func withinDir(dir, target string) bool {
	rel, err := filepath.Rel(dir, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// pathExists 报告路径是否存在（symlink 本身也算存在）。
func pathExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// rollback 回删本次创建的路径（失败路径用）。
//
// 只删 created 列出的文件、它们的空父目录链、以及**本次创建**的 .xinit/。
// 绝不扫描 dest 去删「看起来空」的目录 —— --force 场景下那可能是用户
// 自己建的。
func rollback(dest string, created []string, xinitExisted bool) {
	for i := len(created) - 1; i >= 0; i-- {
		_ = os.Remove(filepath.Join(dest, filepath.FromSlash(created[i])))
	}
	removeEmptyParents(dest, created)
	if !xinitExisted {
		_ = os.RemoveAll(filepath.Join(dest, state.DirName))
	}
}

// removeEmptyParents 自底向上删掉 created 路径的空父目录链。
func removeEmptyParents(dest string, created []string) {
	seen := map[string]bool{}
	for _, rel := range created {
		for dir := path.Dir(rel); dir != "." && dir != "/" && !seen[dir]; dir = path.Dir(dir) {
			seen[dir] = true
		}
	}
	dirs := make([]string, 0, len(seen))
	for d := range seen {
		dirs = append(dirs, d)
	}
	// 深的先删，浅的后删（父目录只有在子目录都空掉之后才删得掉）
	sort.Slice(dirs, func(i, j int) bool {
		if len(dirs[i]) != len(dirs[j]) {
			return len(dirs[i]) > len(dirs[j])
		}
		return dirs[i] > dirs[j]
	})
	for _, d := range dirs {
		_ = os.Remove(filepath.Join(dest, filepath.FromSlash(d)))
	}
}
