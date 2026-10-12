package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/xbcio/xinit/internal/backflow"
	"github.com/xbcio/xinit/internal/fsutil"
	"github.com/xbcio/xinit/internal/manifest"
	"github.com/xbcio/xinit/internal/merge"
	"github.com/xbcio/xinit/internal/render"
	"github.com/xbcio/xinit/internal/state"
	"github.com/xbcio/xinit/internal/template"
)

// BackflowOptions 是 backflow 的全部输入（§20.8）。作用于 cwd；位置参数 = 一个提交。
type BackflowOptions struct {
	ProjectDir string
	Commit     string
	Paths      []string // --paths（可重复）：与清单 upstream.paths 取并集
	Map        []string // --map（可重复）：<Dst>=<Src>
	Out        string   // --out：产物目录（默认 .xinit/backflow/）
	DryRun     bool
}

func newBackflowCmd() *cobra.Command {
	var o BackflowOptions
	cmd := &cobra.Command{
		Use:   "backflow <commit>",
		Short: "把派生项目对共享面的改动回送给模板（§20）",
		Long: "把派生项目某个提交对共享面（清单 upstream.paths ∪ --paths）的改动翻译回模板坐标系，" +
			"生成可 `git am -3` 接收的补丁产物（默认 .xinit/backflow/），不推送；模板源只读——" +
			"本地镜像缺失时按需首次克隆一次建立缓存（已有镜像不刷新）。\n\n" +
			"提交消息末段 trailer 块须含 `Template-Upstream: yes`（opt-in）。\n" +
			"退出码：0 = 导出成功（含警告）；1 = 错误（资格/收敛/全部路径被过滤/全 noop/IO）。" +
			"警告（域外、prune、来源消失、render/binary/symlink、夹带扫描命中、未跟踪文件）不改变退出码。",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			o.ProjectDir = "."
			o.Commit = args[0]
			return Backflow(o)
		},
	}
	cmd.Flags().StringArrayVar(&o.Paths, "paths", nil,
		"共享面 glob（可重复；与清单 upstream.paths 取并集，§20.5）")
	cmd.Flags().StringArrayVar(&o.Map, "map", nil,
		"显式路径映射 <Dst>=<Src>（可重复；新增文件与「模板来源已不存在」的救援出口，§20.7）")
	cmd.Flags().StringVar(&o.Out, "out", "",
		"产物目录（默认 .xinit/backflow/；相对路径锚在项目根，§20.8）")
	cmd.Flags().BoolVar(&o.DryRun, "dry-run", false,
		"全部分类/翻译/扫描照跑，不写产物、不加锁（§20.8）")
	return cmd
}

// Backflow 执行一次 backflow，打印报告，并把非零退出码包进 ExitError（§20.8）。
//
// 出口分流：守卫 1–6 与模板源资格失败是「命令直接报错」（error 原样返回，
// main 落 stderr，rc=1，§20.10 行 1–5）；其余失败面（清单缺 upstream.paths、
// 收敛成因①、全部路径被过滤、全 noop）走报告（stdout）+ SilenceExitError
// （rc=1），文案逐字对齐 §20.10 的文案基准表。
func Backflow(o BackflowOptions) error {
	rep, err := runBackflow(o)
	if err != nil {
		return err
	}
	rep.Print(os.Stdout)
	if rep.ExitCode != CodeOK {
		return &ExitError{
			Code:   rep.ExitCode,
			Silent: true, // 报告（含失败段）已写全，main 不再补行
			msg:    fmt.Sprintf("backflow 以退出码 %d 结束", rep.ExitCode),
		}
	}
	return nil
}

// runBackflow 是 §20.8 的编排本体（不打印；返回的报告总是非 nil）。
//
// 顺序即数据依赖：状态 → 资格（守卫 1–6）→ 模板源与记录 commit 的清单/树
// （T/B/O 与域判定的同一坐标系）→ 枚举 → 装配 Items → 新增文件路径解析
// （§20.7，先于域判定：「解得 Src 后入域判定」）→ 域判定（§20.5）→ 收敛
// （§20.4 守卫 7）→ 边界分类（§20.7）→ 翻译（§20.3）→ 补丁（§20.6）→
// 扫描（§20.5）→ 产物（§20.6）。
//
// 项目锁（§20.8「产物写入加锁」）：非 --dry-run 时本函数取锁覆盖全链，
// 但 T5 的 WriteArtifact 真写时自身也取一次项目锁（artifact.go），而 flock
// 不可重入（同进程重取必失败，artifact_test.go 的 TestWriteArtifactFailsWhenLocked
// 钉死）——故写产物前显式 Unlock 交接，锁语义仍是「产出期间无并发 xinit」。
// --dry-run 全链只读，不加锁。
func runBackflow(o BackflowOptions) (*BackflowReport, error) {
	projectDir, err := filepath.Abs(o.ProjectDir)
	if err != nil {
		return nil, err
	}
	flagMap, err := parseMapFlags(o.Map)
	if err != nil {
		return nil, err
	}
	st, err := state.Load(projectDir)
	if err != nil {
		return nil, err
	}

	rep := &BackflowReport{
		Header:     "xinit backflow",
		Source:     st.Template.Source,
		Revision:   st.Template.Revision,
		TmplCommit: st.Template.Commit,
		DryRun:     o.DryRun,
		ExitCode:   CodeOK,
	}

	var lock *state.Lock
	if !o.DryRun {
		if lock, err = state.LockProject(projectDir); err != nil {
			return nil, err
		}
		defer lock.Unlock() // 与写产物前的显式 Unlock 双调用幂等（Unlock 置空句柄）
	}

	// ── 守卫 1–6（§20.4；任一不过即命令级错误，rc=1）──
	q, err := backflow.Qualify(projectDir, o.Commit)
	if err != nil {
		return nil, err
	}
	rep.DerivedSha = q.Commit
	rep.Untracked = q.Untracked
	meta, subject, err := commitMeta(projectDir, q.Commit)
	if err != nil {
		return nil, err
	}
	rep.Subject = subject

	// ── 模板源资格 + 记录 commit 的清单/树（T/B/O 的唯一坐标系）──
	cacheRoot, err := template.DefaultCacheRoot()
	if err != nil {
		return nil, err
	}
	src, err := backflow.OpenTemplate(st, cacheRoot)
	if err != nil {
		return nil, err
	}
	mdata, err := fs.ReadFile(src.FS, "xinit.yml")
	if err != nil {
		return nil, fmt.Errorf("记录 commit 的模板缺少 xinit.yml: %w", err)
	}
	m, err := manifest.Parse(mdata)
	if err != nil {
		return nil, fmt.Errorf("解析记录 commit 的模板 xinit.yml: %w", err)
	}
	compiled, err := m.Compile()
	if err != nil {
		return nil, fmt.Errorf("编译记录 commit 的模板 xinit.yml: %w", err)
	}
	repoDir, err := template.RepoDir(st.Template.Source, cacheRoot, false)
	if err != nil {
		return nil, err
	}
	tree, err := template.ListTree(repoDir, st.Template.Commit)
	if err != nil {
		return nil, err
	}
	byPath := make(map[string]template.TreeEntry, len(tree))
	treePaths := make(map[string]bool, len(tree))
	for _, e := range tree {
		byPath[e.Path] = e
		treePaths[e.Path] = true
	}
	// 记录 commit 的渲染表（判 render 模式用）。vars 用 state.Vars 原样，
	// 不重跑 EvalVars（同 converge.go 的重建判据）。
	entries, _, err := render.Paths(src.FS, compiled, st.Vars)
	if err != nil {
		return nil, fmt.Errorf("按记录 commit 重渲染路径表失败：%w", err)
	}
	bySrc := make(map[string]render.Entry, len(entries))
	for _, e := range entries {
		bySrc[e.Src] = e
	}

	// ── 枚举（守卫 6）→ prune 判据 → 装配候选 ──
	chs, err := backflow.EnumerateChanges(projectDir, q.Commit)
	if err != nil {
		return nil, err
	}
	prunePats, err := state.ReadPrune(projectDir)
	if err != nil {
		return nil, err
	}
	pruneMatcher, err := fsutil.CompileGlob(prunePats)
	if err != nil {
		return nil, fmt.Errorf("解析 .xinit/prune.txt: %w", err)
	}
	items := backflow.BuildItems(st, chs, pruneMatcher)
	sort.SliceStable(items, func(i, j int) bool { return items[i].Dst < items[j].Dst })

	// ── §20.7 新增文件的模板态路径解析（先于域判定：解得 Src 后入域判定）──
	resolver := &backflow.Resolver{Compiled: compiled, Vars: st.Vars, TreePaths: treePaths}
	for i := range items {
		it := &items[i]
		if it.Class != backflow.ClassNewFile || it.Exit != backflow.ExitExport {
			continue
		}
		if s, ok := flagMap[it.Dst]; ok {
			// 新增文件的 --map 走 §20.7 规则 4 的统一闸门（T3 的 ApplyMap：
			// 目标不得已存在于模板树），不直接写 Src。
			resolved, err := resolver.ApplyMap(it.Dst, s)
			if err != nil {
				return nil, err
			}
			it.Src = resolved
			continue
		}
		s, cands, err := resolver.ResolveNew(it.Dst)
		if err != nil {
			return nil, err
		}
		if s == "" {
			// 零/多命中：定出口并打印候选与 --map 出口（§20.7 规则 3）。
			it.Exit, it.ExitNote = backflow.ExitResolveAmbiguous, noteResolveAmbiguous(it.Dst, cands)
			continue
		}
		it.Src = s
	}

	// ── §20.5 域判定（受管路径的 --map 救援在包内处理；新增文件已带 Src）──
	missingKey, err := backflow.ResolveDomain(items, src.FS, compiled, o.Paths, flagMap)
	if err != nil {
		return nil, err
	}
	if missingKey {
		// §20.5 句 4：不得静默「全域外」——走报告 + rc=1（与成因①/守卫 8/
		// 全 noop 同口径：stdout 可见）。
		rep.ExitCode = CodeError
		rep.Failure = []string{
			fmt.Sprintf("backflow: 模板未声明 upstream.paths：记录 commit 的清单（%s@%s）没有该键，且未给 `--paths`（§20.5 句 4）——"+
				"请用 `--paths <glob>`（可重复）显式给出共享面，或升级模板使其声明 upstream.paths",
				st.Template.Source, short7(st.Template.Commit)),
		}
		return rep, nil
	}

	// ── §20.4 守卫 7 收敛（只读；只作用于受管路径）──
	srcs := make(map[string]string)
	for _, it := range items {
		if it.Class == backflow.ClassManaged && it.Exit == backflow.ExitExport {
			srcs[it.Dst] = it.Src
		}
	}
	conv, err := backflow.CheckConverged(projectDir, st, cacheRoot, srcs)
	if err != nil {
		return nil, err
	}
	gone := make(map[string]bool, len(conv.SourceGone))
	for _, dst := range conv.SourceGone {
		gone[dst] = true
	}
	for i := range items {
		it := &items[i]
		if it.Exit == backflow.ExitExport && it.Class == backflow.ClassManaged && gone[it.Dst] {
			// 成因②：与 §20.5 句 2 同列（均「来源不存在」出口），文案独立——
			// 判据按 Dst，`--map` 不改变本判定（M9 修复轮 1）；按 Dst 去重，
			// 同路径在 domain.go 已定出口时本分支不再进入。
			it.Exit, it.ExitNote = backflow.ExitSourceGone, noteOffTemplate
		}
	}
	if len(conv.Unconverged) > 0 {
		// 成因①：base 与记录 commit 不同源 → 整体拒绝（fail-closed），
		// 不打条目行（此时没有路径有确定出口）。
		rep.ExitCode = CodeError
		rep.Failure = []string{
			"backflow: base 与记录 commit 不同源（上一轮 pending 推进过 base）：无法确定翻译基线，整体拒绝（§20.4 守卫 7 成因①，fail-closed）",
			"路径：" + strings.Join(conv.Unconverged, "、"),
			"出口：先跑 `xinit update` 收敛后再回流",
		}
		return rep, nil
	}

	// ── §20.7 边界分类 → §20.3 翻译 ──
	var patchFiles []backflow.PatchFile
	var newFilePaths [][]string
	conflictSeen := false
	for i := range items {
		it := &items[i]
		if it.Exit != backflow.ExitExport {
			continue
		}
		ch := it.Change
		switch {
		case strings.HasPrefix(ch.Status, "D"):
			it.Exit, it.ExitNote = backflow.ExitDeleted, noteDeleted
			continue
		case strings.HasPrefix(ch.Status, "R"):
			it.Exit, it.ExitNote = backflow.ExitRenamed, noteRenamed
			continue
		case strings.HasPrefix(ch.Status, "C"):
			it.Exit, it.ExitNote = backflow.ExitCopied, noteCopied
			continue
		case strings.HasPrefix(ch.Status, "T"):
			it.Exit, it.ExitNote = backflow.ExitTypechange, noteTypechange
			continue
		}
		// 符号链接：派生侧 mode 120000（A/M 都为 120000），模板侧 symlink
		// 由 tree 的 Symlink 位判定（§20.7：blob 内容=链接文本，文本合并
		// 会「翻译」出语义错误的链接）。
		te, inTree := byPath[it.Src]
		if ch.NewMode == "120000" || (inTree && te.Symlink) {
			it.Exit, it.ExitNote = backflow.ExitSymlink, noteSymlink
			continue
		}
		// generated 位文件（state 记录，§20.7）。
		if fst, ok := st.Files[it.Dst]; ok && fst.Generated {
			it.Exit, it.ExitNote = backflow.ExitGenerated, noteGenerated
			continue
		}
		// render 模式（模板侧判定；新增文件的 Src 不在记录 commit 的渲染表里）。
		if e, ok := bySrc[it.Src]; ok && e.Mode == render.ModeTemplate {
			it.Exit, it.ExitNote = backflow.ExitRender, noteRender
			continue
		}

		isNew := it.Class == backflow.ClassNewFile
		var tb []byte
		if inTree && !isNew {
			b, err := template.CatBlob(repoDir, te.BlobHash)
			if err != nil {
				return nil, fmt.Errorf("读取模板原文（%s@%s）失败：%w", it.Src, short7(st.Template.Commit), err)
			}
			tb = b
		}
		bb, err := state.ReadBase(projectDir, it.Dst)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("读取 .xinit/base/%s 失败：%w", it.Dst, err)
		}
		ob, err := template.CatBlob(projectDir, ch.NewSha)
		if err != nil {
			return nil, fmt.Errorf("读取提交 %s 中 %s 的内容失败：%w", short7(q.Commit), it.Dst, err)
		}
		// binary/mixed-EOL（T/B/O 任一方，§20.7）：内容级合并的前提是文本。
		if binaryOrMixed(tb) || binaryOrMixed(bb) || binaryOrMixed(ob) {
			it.Exit, it.ExitNote = backflow.ExitBinary, noteBinary
			continue
		}
		tr, err := backflow.TranslateFile(backflow.TranslateInput{T: tb, B: bb, O: ob, IsNew: isNew})
		if err != nil {
			return nil, fmt.Errorf("翻译 %s 失败：%w", it.Dst, err)
		}
		if tr.Conflict {
			// 整体拒绝的判据是 tr.Conflict（TranslateFile 的判定），**不**依赖
			// 冲突块解析是否成功——块只影响报告细节，缺块时也要 fail-closed。
			it.Exit, it.ExitNote = backflow.ExitConflict, noteConflict
			conflictSeen = true
			blocks, err := conflictBlocks(backflow.TranslateInput{T: tb, B: bb, O: ob})
			if err != nil {
				return nil, err
			}
			if len(blocks) == 0 {
				rep.Conflicts = append(rep.Conflicts, ConflictBlock{Path: it.Src})
				continue
			}
			for _, blk := range blocks {
				rep.Conflicts = append(rep.Conflicts, ConflictBlock{Path: it.Src, Line: blk.line, Lines: blk.lines})
			}
			continue
		}
		patchFiles = append(patchFiles, backflow.PatchFile{
			Src:     it.Src,
			T:       tb,
			FNew:    tr.FNew,
			OldExec: inTree && te.Exec,
			NewExec: ch.NewMode == "100755",
			IsNew:   isNew,
		})
		if isNew {
			newFilePaths = append(newFilePaths, strings.Split(it.Src, "/"))
		}
	}

	// ── §20.3 冲突：整体 fail-closed 不产补丁 ──
	if conflictSeen {
		for i := range items {
			if items[i].Exit == backflow.ExitExport {
				items[i].Exit, items[i].ExitNote = backflow.ExitConflict, noteConflictHold
			}
		}
		rep.ExitCode = CodeError
		rep.fill(items, nil)
		rep.Failure = []string{
			"backflow: 本轮存在翻译冲突，整体 fail-closed 不产补丁（§20.3）——按上方逐条把冲突块内的行改回模板措辞后重导出",
		}
		return rep, nil
	}
	// ── 守卫 8：全部路径被出口过滤（域外/prune/来源消失/render/binary/…）──
	if len(patchFiles) == 0 {
		rep.ExitCode = CodeError
		rep.fill(items, nil)
		rep.Failure = []string{
			"backflow: 全部路径被出口过滤，没有可导出的改动（nothing to export）",
			"出口：检查该提交涉及的路径是否属于共享面（upstream.paths），或按上方逐条警告处理（域外用 `--paths`/重拆提交、prune、`--map`、render/binary/generated/改名等，§20.4 守卫 8）",
		}
		return rep, nil
	}

	// ── §20.6 补丁生成（全 noop 由 ErrAllNoop 分流）──
	res, err := backflow.BuildPatch(patchFiles, meta, q.Commit, st.Template.Commit)
	rep.Noops = res.Noops
	if err != nil {
		if !errors.Is(err, backflow.ErrAllNoop) {
			return nil, err
		}
		// 全 noop：哨兵原文打印（勿拼 err.Error() 的计数后缀）。
		rep.ExitCode = CodeError
		rep.fill(items, nil)
		rep.Failure = []string{
			"backflow: " + backflow.ErrAllNoop.Error(),
			"（全部候选路径的内容与 exec 位都与模板无差异，已剔除、不产补丁；出口：无需动作，§20.6）",
		}
		return rep, nil
	}
	kept := make(map[string]bool, len(res.Kept))
	for _, s := range res.Kept {
		kept[s] = true
	}
	rep.Kept = res.Kept
	rep.fill(items, kept)

	// ── §20.5 夹带扫描（警告级：rc 恒不影响导出完成）──
	// 只有走到这里（补丁已生成）才置 ScanRan：其余出口不跑扫描、报告不打扫描段。
	rep.ScanRan = true
	tokens, dropped, vacuous := backflow.BuildTokens(compiled, st.Vars)
	rep.Dropped, rep.Vacuous = dropped, vacuous
	rep.Scans = backflow.Scan(backflow.AddedLinesFromPatch(res.Patch), newFilePaths, tokens)
	rep.ScanTruncated = len(rep.Scans) == backflow.MaxScanHits

	// ── §20.6 产物：文件名含双 sha；--dry-run 只报路径、零写 ──
	outDir, err := backflow.ResolveOutDir(projectDir, o.Out)
	if err != nil {
		return nil, err
	}
	artifact := filepath.Join(outDir, backflow.ArtifactName(q.Commit, st.Template.Commit, subject))
	// flock 不可重入：WriteArtifact 真写时会自取项目锁，故先交接本函数的锁
	// （--dry-run 时 lock 为 nil，Unlock 对 nil 接收者安全）。
	lock.Unlock()
	path, err := backflow.WriteArtifact(projectDir, o.DryRun, artifact, res.Patch)
	if err != nil {
		return nil, err
	}
	rep.Artifact = displayPath(projectDir, path)
	return rep, nil
}

// parseMapFlags 解析 `--map <Dst>=<Src>`（§20.7 规则 4 的旗标面）。
//
// 两侧非空、同一 Dst 不得重复给出（fail-closed：静默取后者会让长命令行的
// 笔误变成难以察觉的错误映射）。
func parseMapFlags(specs []string) (map[string]string, error) {
	out := make(map[string]string, len(specs))
	for _, s := range specs {
		dst, src, ok := strings.Cut(s, "=")
		if !ok || dst == "" || src == "" {
			return nil, exitError(CodeError,
				"--map %q 形状非法：必须是 <Dst>=<Src> 且两侧非空（如 --map pkg/new.go=internal/new.go.tmpl，§20.7）", s)
		}
		if _, dup := out[dst]; dup {
			return nil, exitError(CodeError,
				"--map %q：Dst %s 重复给出（同一路径只能映射到一个模板路径，fail-closed）", s, dst)
		}
		out[dst] = src
	}
	return out, nil
}

// commitMeta 取 mailbox 的提交身份（§20.6 规则③：author/date 逐字取派生提交）。
//
// 一次 `git log -1` 取全（NUL 分隔，%b 里的换行不干扰切分）；--no-show-signature
// 防 `log.showSignature=true` 时 gpg 输出前置污染 stdout（同 eligibility.go）。
// Subject 取 %s（与 ArtifactName 的 slug 同源）；Body 取 %b 并去尾部换行
// （BuildPatch 会再 Trim，勿传多尾空行）。
func commitMeta(repoDir, sha string) (backflow.CommitMeta, string, error) {
	out, err := exec.Command("git", "-C", repoDir, "log", "-1", "--no-show-signature",
		"--format=%an%x00%ae%x00%aD%x00%s%x00%b", sha).Output()
	if err != nil {
		return backflow.CommitMeta{}, "", fmt.Errorf("读取提交 %s 的元信息失败：%w", short7(sha), err)
	}
	f := strings.SplitN(string(out), "\x00", 5)
	if len(f) != 5 {
		return backflow.CommitMeta{}, "", fmt.Errorf("解析提交 %s 的元信息失败：期望 5 段（作者/邮箱/日期/主题/正文），实际 %d 段", short7(sha), len(f))
	}
	meta := backflow.CommitMeta{
		Author:  f[0],
		Email:   f[1],
		Date:    f[2],
		Subject: strings.TrimRight(f[3], "\n"),
		Body:    strings.TrimRight(f[4], "\n"),
	}
	return meta, meta.Subject, nil
}

// conflictBlock 是一个冲突块的报告形态（行号 + 原文行）。
type conflictBlock struct {
	line  int
	lines []string
}

// conflictBlocks 从三方合并的输出里提取冲突块（§20.10 行 11：打印 `文件:行` +
// 三侧内容）。TranslateFile 冲突时不交出带标记的输出（防误用），故报告层以同
// 输入重跑一次 merge.ThreeWay 取块——merge 在临时目录里跑、无共享状态，
// 同输入同结果。标记行用 merge 的固定标签（-L ours/base/theirs）精确匹配。
func conflictBlocks(in backflow.TranslateInput) ([]conflictBlock, error) {
	res, err := merge.ThreeWay(in.T, in.B, in.O)
	if err != nil {
		return nil, err
	}
	if res.Outcome != merge.Conflicts {
		return nil, nil
	}
	var (
		blocks []conflictBlock
		cur    *conflictBlock
	)
	for i, ln := range strings.Split(string(res.Bytes), "\n") {
		switch {
		case cur == nil && strings.HasPrefix(ln, "<<<<<<< ours"):
			cur = &conflictBlock{line: i + 1}
			cur.lines = append(cur.lines, ln)
		case cur != nil:
			cur.lines = append(cur.lines, ln)
			if strings.HasPrefix(ln, ">>>>>>> theirs") {
				blocks = append(blocks, *cur)
				cur = nil
			}
		}
	}
	return blocks, nil
}

// binaryOrMixed 报「T/B/O 任一方是二进制或混合 EOL」（§20.7 的同一出口；
// nil 侧——新增文件的 T、无 base 的 B——不算）。
func binaryOrMixed(b []byte) bool {
	return b != nil && (fsutil.IsBinary(b) || fsutil.DetectEOL(b) == fsutil.EOLMixed)
}

// short7 是报告里的短 sha（7 位；不足则原样）。
func short7(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// displayPath 把产物路径转成展示形态：项目内用相对路径（报告可复制的
// `.xinit/backflow/<name>`），项目外用绝对路径（--out 允许项目外）。
func displayPath(base, p string) string {
	rel, err := filepath.Rel(base, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return p
	}
	return filepath.ToSlash(rel)
}
