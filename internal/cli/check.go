package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/mod/semver"

	"github.com/xbcio/xinit/internal/fsutil"
	"github.com/xbcio/xinit/internal/state"
	"github.com/xbcio/xinit/internal/template"
)

// CheckOptions 是 check 的输入（R13：作用于 cwd）。
// 只读：不加锁、项目内不写任何文件（§18.1）。
type CheckOptions struct {
	ProjectDir string
	Fetch      bool
}

func newCheckCmd() *cobra.Command {
	var o CheckOptions
	cmd := &cobra.Command{
		Use:   "check",
		Short: "只读门禁：校验 .xinit/ 内部自洽、git 契约与模板可继续更新（§18）",
		Long: "分组检查（§18）：A 状态与 base 树、B 冲突残留、C 配置与 git 契约、D 模板面。\n\n" +
			"只读：项目内零写、不加锁、默认不联网；--fetch 刷新已有镜像。\n" +
			"有 FAIL 项退出码 6；warn/skip 不影响退出码。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			o.ProjectDir = "."
			return Check(o)
		},
	}
	cmd.Flags().BoolVar(&o.Fetch, "fetch", false, "联网刷新模板镜像缓存后再检查模板面")
	return cmd
}

// Check 打印报告；有 FAIL 项 → 退出码 6（§18.6）。warn/skip 不影响退出码。
func Check(o CheckOptions) error {
	rep, err := runCheck(o)
	if err != nil {
		return err
	}
	rep.Print(os.Stdout)
	if rep.Fails > 0 {
		return &ExitError{
			Code:   CodeCheck,
			Silent: true,
			msg:    fmt.Sprintf("check：%d 项失败", rep.Fails),
		}
	}
	return nil
}

// runCheck 跑完四组检查（计数器模型：不在首个失败处中止）。返回的错误只来自
// 前置失败或 IO 意外——普通 error、退 1、不进计数器（§18.7）。
func runCheck(o CheckOptions) (*checkReport, error) {
	projectDir, err := filepath.Abs(o.ProjectDir)
	if err != nil {
		return nil, err
	}
	st, err := state.Load(projectDir)
	if err != nil {
		return nil, fmt.Errorf(
			"%w\n这不是 xinit 项目，或 .xinit/state.json 缺失；请从模板 init 生成，或用 xinit adopt 重建基线（§5.3）",
			err)
	}
	rep := newCheckReport(st.Template.Source)
	if err := checkGroupA(rep, projectDir, st); err != nil {
		return nil, err
	}
	if err := checkGroupB(rep, projectDir, st); err != nil {
		return nil, err
	}
	if err := checkGroupC(rep, projectDir, st); err != nil {
		return nil, err
	}
	if err := checkGroupD(rep, projectDir, st, o.Fetch); err != nil {
		return nil, err
	}
	return rep, nil
}

// checkKind 是检查行的级别。
type checkKind int

const (
	kindFail checkKind = iota
	kindWarn
	kindSkip
)

// String 返回级别词。FAIL/warn/skip 是稳定标识、供脚本 grep（§18.6），
// 拼写不可改。
func (k checkKind) String() string {
	switch k {
	case kindFail:
		return "FAIL"
	case kindWarn:
		return "warn"
	case kindSkip:
		return "skip"
	}
	return "unknown"
}

// checkLine 是报告中的一条检查结果。Detail 是预排版的附加行（不含基础缩进）。
type checkLine struct {
	Kind   checkKind
	Text   string
	Detail []string
}

// checkSection 是一个检查组。Lines 存指针：fail/warn/skip 返回的行指针
// 在同组后续追加（切片扩容）后仍有效，调用方可随时挂 Detail。
type checkSection struct {
	Title string
	Lines []*checkLine
}

// checkReport 是 §18.6 报告的数据；Print 只管排版。
type checkReport struct {
	Source   string
	Sections []checkSection

	Fails, Warns, Skips int
}

// 组索引（与 newCheckReport 的标题顺序一致）。
const (
	secA = 0
	secB = 1
	secC = 2
	secD = 3
)

// newCheckReport 构造带四组标题的空报告：组标题序列本身即检查清单（§18.6）。
func newCheckReport(source string) *checkReport {
	return &checkReport{
		Source: source,
		Sections: []checkSection{
			{Title: "A 状态与 base 树（§4.3 / §5.1 / §5.2）"},
			{Title: "B 冲突残留（§10.4）"},
			{Title: "C 配置与 git 契约（§3.2 / §5.5）"},
			{Title: "D 模板面（§5.2 / §7.6 / §11.4）"},
		},
	}
}

// add 追加一行并返回其指针（调用方可挂 Detail）。
func (r *checkReport) add(sec int, k checkKind, format string, args ...any) *checkLine {
	s := &r.Sections[sec]
	ln := &checkLine{Kind: k, Text: fmt.Sprintf(format, args...)}
	s.Lines = append(s.Lines, ln)
	return ln
}

func (r *checkReport) fail(sec int, format string, args ...any) *checkLine {
	r.Fails++
	return r.add(sec, kindFail, format, args...)
}

func (r *checkReport) warn(sec int, format string, args ...any) *checkLine {
	r.Warns++
	return r.add(sec, kindWarn, format, args...)
}

func (r *checkReport) skip(sec int, format string, args ...any) *checkLine {
	r.Skips++
	return r.add(sec, kindSkip, format, args...)
}

// Print 输出报告（§18.6）：组标题恒打印（含空组），末行为汇总。
func (r *checkReport) Print(w io.Writer) {
	fmt.Fprintf(w, "xinit check: %s\n", r.Source)
	for _, s := range r.Sections {
		fmt.Fprintf(w, "%s:\n", s.Title)
		for _, l := range s.Lines {
			fmt.Fprintf(w, "  %-5s %s\n", l.Kind, l.Text)
			for _, d := range l.Detail {
				fmt.Fprintf(w, "  %s\n", d)
			}
		}
	}
	if r.Fails > 0 {
		fmt.Fprintf(w, "检查汇总: %d 项失败、%d 项警告、%d 项跳过（退出码 %d）\n",
			r.Fails, r.Warns, r.Skips, CodeCheck)
	} else {
		fmt.Fprintf(w, "检查汇总: 通过（0 项失败、%d 项警告、%d 项跳过）\n", r.Warns, r.Skips)
	}
}

// baseFile 是 base 树中的一个普通文件。
type baseFile struct {
	content []byte
	exec    bool
}

// listBaseTree 一次列举 base 树（§18.2）：普通文件进 regular，其余非目录
// 条目（符号链接/设备/FIFO 等）进 nonRegular[rel]=类型串；目录不入表——
// 嵌套路径的目录项是正常结构。base 目录缺失 = 两张空表。
func listBaseTree(base string) (regular map[string]baseFile, nonRegular map[string]string, err error) {
	regular = map[string]baseFile{}
	nonRegular = map[string]string{}
	if _, err := os.Lstat(base); os.IsNotExist(err) {
		return regular, nonRegular, nil
	} else if err != nil {
		return nil, nil, err
	}
	err = filepath.WalkDir(base, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if p == base || d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(base, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			nonRegular[rel] = info.Mode().Type().String()
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		regular[rel] = baseFile{content: data, exec: info.Mode().Perm()&0o111 != 0}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("读取 base 树: %w", err)
	}
	return regular, nonRegular, nil
}

// checkEscapePath 渲染 base/%s 形态消息里的路径：含控制字符（< 0x20 或
// 0x7F）时返回 strconv.Quote 形式，避免换行把一条检查行拆成两行；否则原样。
func checkEscapePath(p string) string {
	for i := 0; i < len(p); i++ {
		if p[i] < 0x20 || p[i] == 0x7f {
			return strconv.Quote(p)
		}
	}
	return p
}

// validCheckKey 判定 files 键是否为规范相对路径（§4.3、§22.4）：口径 = fsutil.CheckRelPath。
func validCheckKey(p string) bool {
	return fsutil.ValidRelPath(p)
}

// checkGroupA 执行 A1–A10（§18.2）。返回的错误只来自 IO 意外；
// 判定结果一律进报告。
func checkGroupA(r *checkReport, projectDir string, st *state.State) error {
	keys := sortedStringKeys(st.Files)

	// A1：非法键仅报 A1，不参与 A2–A9。
	var legal []string
	legalSet := map[string]bool{}
	for _, p := range keys {
		if !validCheckKey(p) {
			r.fail(secA, "files 键 %q 不是规范的相对路径（§4.3）", p)
			continue
		}
		legal = append(legal, p)
		legalSet[p] = true
	}

	// A2：按折叠键分组，组内首键与其余每个键各报一条。
	firstByFold := map[string]string{}
	for _, p := range legal {
		f := fsutil.Fold(p)
		if first, ok := firstByFold[f]; ok {
			r.fail(secA, "%q 与 %q 折叠等价冲突（§9.3）", first, p)
			continue
		}
		firstByFold[f] = p
	}

	// A3
	for _, p := range legal {
		if st.Files[p].Src == "" {
			r.fail(secA, "files[%q].src 为空（§4.3）", p)
		}
	}

	// base 根异常（不存在 / 非目录）：报一条根因，跳过 A4–A9 的表驱动判定
	// ——逐键报缺失只会淹掉真因（§18.2 A4 注）。
	base := state.BaseDir(projectDir)
	info, err := os.Lstat(base)
	switch {
	case os.IsNotExist(err):
		r.fail(secA, "base 树不可用：.xinit/base %s（全部记账路径按缺失处理，§5.1）", "不存在")
	case err != nil:
		return err
	case !info.IsDir():
		r.fail(secA, "base 树不可用：.xinit/base %s（全部记账路径按缺失处理，§5.1）",
			fmt.Sprintf("不是目录（symlink 等；类型 %s）", info.Mode().Type()))
	default:
		if err := checkBaseTables(r, base, st, legal, legalSet); err != nil {
			return err
		}
	}

	// A10
	if k := st.Template.Kind; k != template.KindGit.String() && k != template.KindDir.String() {
		r.fail(secA, "template.kind %q 非法（§4.2）", k)
	}
	if st.Template.Source == "" {
		r.fail(secA, "template.source 为空（§4.2）")
	}
	if st.Template.Commit == "" {
		r.fail(secA, "template.commit 为空（§4.2）")
	}
	return nil
}

// checkBaseTables 执行 A4–A9：只基于一次 listBaseTree 的两张表查，
// 不按键拼路径读盘（§18.2）。legal 是通过 A1 的键（已排序）。
func checkBaseTables(r *checkReport, base string, st *state.State, legal []string, legalSet map[string]bool) error {
	regular, nonRegular, err := listBaseTree(base)
	if err != nil {
		return err
	}

	// A4：只查 regular 表；nonRegular 中的键由 A6 独占报告。
	for _, p := range legal {
		f, ok := regular[p]
		if !ok {
			if _, nr := nonRegular[p]; !nr {
				r.fail(secA, "base/%s 缺失（§5.1——update 将按 §5.2 处理）", checkEscapePath(p))
			}
			continue
		}
		fst := st.Files[p]
		if got := fsutil.CompareHash(f.content, fst.Binary); got != fst.Hash {
			r.fail(secA, "base/%s 与记账 hash 不符（got %s, want %s；§5.1——update 将按 §5.2 处理）",
				checkEscapePath(p), got, fst.Hash)
		}
	}

	// A5
	for _, p := range sortedStringKeys(regular) {
		if !legalSet[p] {
			r.fail(secA, "base/%s 不在记账中（残片；update 将清除）", checkEscapePath(p))
		}
	}

	// A6
	for _, p := range sortedStringKeys(nonRegular) {
		r.fail(secA, "base/%s 不是普通文件（%s）", checkEscapePath(p), nonRegular[p])
	}

	// A7–A9：记录位与 base 内容/mode 对照，仅对 regular 中的键。
	// §22.6：无 x 位语义平台（Windows）——A9（exec 对照）输出 skip，A7/A8 照常。
	if !fsutil.ExecSupported {
		r.skip(secA, "exec 位检查按平台跳过（本平台无 x 位语义，§22.6）")
	}
	for _, p := range legal {
		f, ok := regular[p]
		if !ok {
			continue
		}
		fst := st.Files[p]
		if bin := fsutil.IsBinary(f.content); fst.Binary != bin {
			r.fail(secA, "files[%q].binary=%v 但 base 内容是 %v", p, fst.Binary, bin)
		}
		if mixed := fsutil.DetectEOL(f.content) == fsutil.EOLMixed; fst.MixedEOL != mixed {
			r.fail(secA, "files[%q].mixed_eol=%v 但 base 内容是 %v", p, fst.MixedEOL, mixed)
		}
		if fsutil.ExecSupported && fst.Exec != f.exec {
			has := "无"
			if f.exec {
				has = "有"
			}
			r.fail(secA, "files[%q].exec=%v 但 base 文件%s可执行位", p, fst.Exec, has)
		}
	}

	return nil
}

// checkGroupB 执行 B1（§18.3）：只读列举 conflicts/，不做「本轮仍冲突」
// 判定（那是 diff 的预测）。出路两行是 check 专属文案，不复用 conflictExits。
func checkGroupB(r *checkReport, projectDir string, st *state.State) error {
	entries, err := listConflicts(projectDir)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}
	l := r.fail(secB, "未解决的冲突（%d 条，§10.4）：", len(entries))
	for _, p := range entries {
		if _, ok := st.Files[p]; ok {
			l.Detail = append(l.Detail, fmt.Sprintf("%-20s %s", "conflict", checkEscapePath(p)))
		} else {
			l.Detail = append(l.Detail, fmt.Sprintf("%-20s %s（孤立条目，下轮 update 将清理）", "stale", checkEscapePath(p)))
		}
	}
	l.Detail = append(l.Detail,
		"出路一：编辑 .xinit/conflicts/<路径> 手工解决后，运行 xinit update --accept <路径> 声明已解决（§10.3）。",
		"出路二：本轮已无事可做时直接运行 xinit update——陈旧条目随本轮自动清理（§10.2 第 8 步）。")
	return nil
}

// checkGroupC 执行 C1–C3（§18.4）。C1 不依赖 git；非 git 判定先于 C2——
// IsGitTracked 在非 git 仓返回假「未跟踪」。只读：不调 EnsureGitExcluded。
func checkGroupC(r *checkReport, projectDir string, st *state.State) error {
	// C1：逐条编译，以定位非法条目（与 expandPrune 同语义）。
	pats, err := state.ReadPrune(projectDir)
	if err != nil {
		return err
	}
	for _, pat := range pats {
		if _, err := fsutil.CompileGlob([]string{pat}); err != nil {
			r.fail(secC, "prune.txt 条目 %q 不是合法 glob：%v（§3.2）", pat, err)
		}
	}

	excludePath, ok := state.GitExcludePath(projectDir)
	if !ok {
		r.skip(secC, "无法定位 git 仓库：跳过 .xinit/ 跟踪与 exclude 检查（§5.5）")
		return nil
	}

	// C2
	tracked, err := state.IsGitTracked(projectDir, state.DirName)
	if err != nil {
		return fmt.Errorf("无法确认 .xinit/ 是否被 git 跟踪: %w", err)
	}
	if tracked {
		r.fail(secC, "%s/ 已被 git 跟踪：状态目录不应进入版本历史（§5.5）；请 git rm -r --cached %s/ 后提交",
			state.DirName, state.DirName)
	}

	// C3：与 EnsureGitExcluded 同口径，只读。
	data, err := os.ReadFile(excludePath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("读取 %s: %w", excludePath, err)
	}
	entry := state.DirName + "/"
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == entry {
			return nil
		}
	}
	r.warn(secC, ".git/info/exclude 缺少 .xinit/ 行（init/update 会自动补写；补上可避免 git add -A 误纳状态目录，§5.5）")
	return nil
}

// checkGroupD 执行 D1–D3（§18.5）：全部无 FAIL——模板面只提示不阻塞。
// 输出顺序 D1→D2→D3；D2 只依赖 state，恒执行（§18.7）。
func checkGroupD(r *checkReport, projectDir string, st *state.State, fetch bool) error {
	d3, err := checkTemplateSide(r, st, fetch)
	if err != nil {
		return err
	}
	// D2
	if st.EngineVersion != EngineVersion {
		r.warn(secD, "engine_version 记录 %s，当前 %s——渲染结果可能有漂移（§7.6）", st.EngineVersion, EngineVersion)
	}
	if d3 != nil {
		d3()
	}
	return nil
}

// checkTemplateSide 判 D1 并就地出行，D3 的结果以闭包返回（由调用方排在 D2
// 之后输出；nil = 无 D3 行）。D1/D3 共同降级时只出一条合并 skip。git 源先经
// RepoDir 一次定位——fetch 只在这里发生，不 clone、不建缓存根（§18.5）。
func checkTemplateSide(r *checkReport, st *state.State, fetch bool) (func(), error) {
	tpl := st.Template
	// 分支一：A10 判据（与 checkGroupA 同口径）命中——模板面判定无意义。
	if (tpl.Kind != template.KindGit.String() && tpl.Kind != template.KindDir.String()) ||
		tpl.Source == "" || tpl.Commit == "" {
		r.skip(secD, "template 字段非法（见 A10）：跳过模板面检查")
		return nil, nil
	}
	// 分支二：dir 源。
	if tpl.Kind == template.KindDir.String() {
		r.skip(secD, "dir 源：无版本概念（§11.4）")
		return func() {
			r.skip(secD, "dir 源无历史版本：base 丢失时仅当渲染快照可用时可自动恢复，否则只能 adopt（§5.2、§21）")
		}, nil
	}

	// 分支三：git 源。
	cacheRoot, err := template.DefaultCacheRoot()
	if err != nil {
		return nil, err
	}
	repoDir, err := template.RepoDir(tpl.Source, cacheRoot, fetch)
	if errors.Is(err, template.ErrMirrorMissing) {
		r.skip(secD, "模板面检查需要本地镜像缓存（先运行 xinit update 建立缓存，§11.4）")
		return nil, nil
	} else if err != nil {
		r.skip(secD, "无法读取模板仓（%s）", checkEscapePath(err.Error()))
		return nil, nil
	}

	// D1：镜像已定位（含可选 fetch），ListTags 传 fetch=false 避免双 fetch。
	if !semver.IsValid(tpl.Revision) {
		r.skip(secD, "记录的 revision %q 不是 vX.Y.Z 格式，无法做版本比较（§11.4）", tpl.Revision)
	} else if tags, err := template.ListTags(tpl.Source, cacheRoot, false, false); err != nil {
		r.skip(secD, "无法读取模板 tag（%s；§11.4）", checkEscapePath(err.Error()))
	} else {
		var newer []string
		for _, t := range template.SemverTags(tags) {
			if semver.Compare(t, tpl.Revision) > 0 {
				newer = append(newer, t)
			}
		}
		if n := len(newer); n > 0 {
			latest := newer[n-1]
			r.warn(secD, "本地镜像中有 %d 个更高的 semver tag：%s（最新 %s；xinit update --revision %s 升级）",
				n, strings.Join(newer, ", "), latest, latest)
		}
	}

	// D3：只读 ls-tree 判可取（不用 NewGitFS——其 loadBlobs 全量读内容）。
	if _, err := template.ListTree(repoDir, tpl.Commit); err != nil {
		return func() {
			r.warn(secD, "记录的 commit %s 在模板源中取不到：base 损坏时仅当渲染快照可用时可自动重建，否则无法自动重建（§5.2、§21）",
				checkEscapePath(tpl.Commit))
		}, nil
	}
	return nil, nil
}
