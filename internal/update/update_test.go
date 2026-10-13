package update

// 来源：internal/cli/update_test.go 行 642-885（§4.3 的 7 个单元级用例随迁，
// I-2 实施批 T2/M1）——管线级用例留 cli（其夹具 initProjectT 依赖 cli.Init）。

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/xbcio/xinit/internal/apply"
	"github.com/xbcio/xinit/internal/fsutil"
	"github.com/xbcio/xinit/internal/manifest"
	"github.com/xbcio/xinit/internal/plan"
	"github.com/xbcio/xinit/internal/render"
	"github.com/xbcio/xinit/internal/state"
	"github.com/xbcio/xinit/internal/template"
)

// engineVersionT 是引擎版本的测试字面量（v2-W-6）：生产值 EngineVersion 留
// cli（§2.7 #13），经 Input.EngineVersion 注入；本包用例不依赖其取值。
const engineVersionT = "0.0.0-test"

// AL（§9.5 删除逃逸分支）：dry-run 的预测文案与实跑同判据——逃逸拒绝也要
// 给出 prune 出口（与障碍物/折叠/写入逃逸分支一致）。
func TestPredictRefusalsDeleteEscapeHintsPrune(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("符号链接需要 Windows 特权")
	}
	proj := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(proj, "escape")); err != nil {
		t.Fatal(err)
	}
	refusals, err := predictRefusals(proj, apply.Changes{Deletes: []string{"escape/x.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(refusals) != 1 || refusals[0].Path != "escape/x.txt" {
		t.Fatalf("外指父链接的删除路径应被预测拒绝: %+v", refusals)
	}
	if !strings.Contains(refusals[0].Reason, "prune.txt") {
		t.Errorf("删除逃逸拒绝要给出 prune 出口（AL）：%q", refusals[0].Reason)
	}
}

// 裁定 U/Z：备份目标以 ours 快照（不穿透软链）为准——中间段是软链的写入
// 路径不得进备份（否则 Backup 的穿透 Lstat 会错位备份链后文件）。
func TestBackupTargetsOnlyRegularInOurs(t *testing.T) {
	ours := plan.Snap{
		"a.txt": {Kind: render.KindRegular, Hash: "h"},
		"link":  {Kind: render.KindSymlink},
	}
	c := apply.Changes{
		Writes:   map[string][]byte{"a.txt": nil, "link/x.txt": nil, "new.txt": nil},
		Deletes:  []string{"link"},
		ExecBits: map[string]bool{"a.txt": true, "link/x.txt": false},
	}
	got := backupTargets(c, ours)
	if !slices.Equal(got, []string{"a.txt"}) {
		t.Fatalf("got %v, want [a.txt]（ours✗ 无原内容；软链不做备份）", got)
	}
}

func TestHasConfirmed(t *testing.T) {
	if hasConfirmed(nil) || hasConfirmed(map[string]string{"h": ""}) {
		t.Fatal("空 map / 仅 marker 不算已有确认")
	}
	if !hasConfirmed(map[string]string{"h": "", "g": "v1"}) {
		t.Fatal("任一非空值即算已有确认")
	}
}

func TestHookDiffAgainstBaseDirSourceReturnsEmpty(t *testing.T) {
	st := &state.State{Template: state.TemplateRef{Kind: "dir", Source: t.TempDir(), Commit: "abc"}}
	ver, diff := hookDiffAgainstBase(st, t.TempDir(), "echo new\n")
	if ver != "" || diff != "" {
		t.Fatalf("dir 源无历史，diff 必须为空: ver=%q diff=%q", ver, diff)
	}
}

// §22.6：无 x 位语义平台（Windows）时 execConclusion 入口短路——Noop/
// WriteTheirs（行 13 模板新增）/Merge/Merge 迁移态四型一律无结论；dry-run 的
// chmodPredictions 与真跑 res.Chmodded 同源本函数 ⇒ 两径同口径（均 0）。
func TestExecConclusionUnsupportedShortCircuit(t *testing.T) {
	meta := func(h string, x bool) plan.FileMeta {
		return plan.FileMeta{Kind: render.KindRegular, Hash: h, Exec: x}
	}
	base := plan.Snap{
		"a.txt":     meta("h1", false),
		"m.txt":     meta("h1", false),
		"old/a.txt": meta("ho", false),
	}
	ours := plan.Snap{
		"a.txt":     meta("h2", false),
		"m.txt":     meta("h2", false),
		"old/a.txt": meta("ho", true), // 本地改过权限：迁移态判据
	}
	theirs := plan.Snap{
		"a.txt":   meta("h3", true),
		"new.txt": meta("h3", true),
		"m.txt":   meta("h3", true),
		"q.txt":   meta("h3", true),
	}
	nb := meta("h3", true)
	cases := []struct {
		name string
		a    plan.Action
	}{
		{"Noop", plan.Action{Path: "a.txt", Kind: plan.Noop, NewBase: &nb}},
		{"WriteTheirs 行 13（模板新增）", plan.Action{Path: "new.txt", Kind: plan.WriteTheirs, NewBase: &nb}},
		{"Merge（干净合并）", plan.Action{Path: "m.txt", Kind: plan.Merge, NewBase: &nb}},
		{"Merge 迁移态（MigratedFrom ≠ \"\"）", plan.Action{Path: "q.txt", Kind: plan.Merge, NewBase: &nb, MigratedFrom: "old/a.txt"}},
	}

	// 对照前置：ExecSupported=true 时四型皆有结论——夹具失效时立即暴露，
	// 防「短路测试」因夹具恒 false 而假阳性。
	for _, tc := range cases {
		if _, ok := execConclusion(tc.a, base, ours, theirs); !ok {
			t.Fatalf("%s：夹具应有 exec 结论（对照前置）", tc.name)
		}
	}

	old := fsutil.ExecSupported
	defer func() { fsutil.ExecSupported = old }()
	fsutil.ExecSupported = false

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := execConclusion(tc.a, base, ours, theirs)
			if got || ok {
				t.Errorf("execConclusion = %v,%v，want false,false（平台无 x 位语义）", got, ok)
			}
		})
	}

	// 同源链：buildChanges 产出的 ExecBits 必空 ⇒ dry-run 预测（chmodPredictions）
	// 与真跑 Chmodded 两径同口径（均 0）。
	actions := make([]plan.Action, 0, len(cases))
	for _, tc := range cases {
		actions = append(actions, tc.a)
	}
	c, err := buildChanges(base, ours, theirs, actions,
		map[string][]byte{"new.txt": []byte("n\n")},
		map[string][]byte{"m.txt": []byte("m\n"), "q.txt": []byte("q\n")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.ExecBits) != 0 {
		t.Errorf("ExecBits 必空；got %v", c.ExecBits)
	}
	if got := chmodPredictions(c, nil); len(got) != 0 {
		t.Errorf("chmodPredictions 必空；got %v", got)
	}
}

// overwrittenPaths 是行 5 另存清单的唯一出处（13b 落盘与 dry-run 预测共用）：
// 只有「行 5 ∧ 未被写前检查拒绝」的路径在列——被拒绝的路径没有被覆盖，
// §8.2 的另存义务不触发（T8 fix round 1，评审 C1）。
func TestOverwrittenPaths(t *testing.T) {
	tests := []struct {
		name    string
		actions []plan.Action
		refused map[string]bool
		want    []string
	}{
		{name: "空输入 → 空", want: nil},
		{
			name: "行 5 且未被拒 → 列入（按路径排序）",
			actions: []plan.Action{
				{Path: "sub/gen.txt", Kind: plan.WriteTheirs, OverwriteGenerated: true},
				{Path: "bin.sh", Kind: plan.WriteTheirs, OverwriteGenerated: true},
			},
			refused: map[string]bool{},
			want:    []string{"bin.sh", "sub/gen.txt"},
		},
		{
			name:    "行 5 被拒 → 不列（折叠拒绝下没发生覆盖）",
			actions: []plan.Action{{Path: "gen.txt", Kind: plan.WriteTheirs, OverwriteGenerated: true}},
			refused: map[string]bool{"gen.txt": true},
			want:    nil,
		},
		{
			name: "非行 5 → 不列（即使未被拒）",
			actions: []plan.Action{
				{Path: "plain.txt", Kind: plan.WriteTheirs},
				{Path: "skip.txt", Kind: plan.Noop, Note: plan.SkipNote},
				{Path: "conflict.txt", Kind: plan.ConflictBinary},
			},
			refused: map[string]bool{},
			want:    nil,
		},
		{
			name: "混合：只列行 5 且未被拒的",
			actions: []plan.Action{
				{Path: "a.txt", Kind: plan.WriteTheirs},
				{Path: "gen.txt", Kind: plan.WriteTheirs, OverwriteGenerated: true},
				{Path: "gen2.txt", Kind: plan.WriteTheirs, OverwriteGenerated: true},
			},
			refused: map[string]bool{"gen2.txt": true},
			want:    []string{"gen.txt"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := overwrittenPaths(tc.actions, tc.refused); !slices.Equal(got, tc.want) {
				t.Errorf("overwrittenPaths = %v, want %v", got, tc.want)
			}
		})
	}
}

// §10.2 第 9 步的 M9 C-1 分支（update.go:1132-1141）：base✗ 的 --accept 干净
// 路径下 files[P] 条目缺失（hadOld=false），保持旧 base 时 Src 必须取本轮模板
// 映射 newSrc[P]。夹具特意让「模板原路径 ≠ 目标路径」——误把 Dst 当 Src
// （srcPath = a.Path）在此必红；源名与 dst 同名的 txtar 夹具区分不出这一变异
// （M9 T4-M1 park 项）。
func TestAdvanceStateSrcFromNewSrcWithoutFilesEntry(t *testing.T) {
	const (
		dst     = "p.txt"
		srcPath = "src/p.txt.tmpl" // 与 dst 必须不同：同名夹具对本变异无区分力
	)
	baseBytes := []byte("old base\n")
	bin := fsutil.IsBinary(baseBytes)
	meta := plan.FileMeta{
		Kind:     render.KindRegular,
		Binary:   bin,
		MixedEOL: fsutil.DetectEOL(baseBytes) == fsutil.EOLMixed,
		Hash:     fsutil.CompareHash(baseBytes, bin),
	}
	// st.Files 为空 = files 条目缺失；NewBase == Metas[P] ⇒ advanced=false
	// （走「保持旧 base」分支，而非推进分支）。
	// v2-W-6：EngineVersion 常量留 cli（§2.7 #13）——本包用例用测试字面量
	// 走同一条注入链（advanceState 的 engineVersion 参数），断言不依赖其取值。
	st := state.New(engineVersionT)
	baseSnap := &Base{
		Metas: map[string]plan.FileMeta{dst: meta},
		Bytes: map[string][]byte{dst: baseBytes},
	}
	actions := []plan.Action{{Path: dst, Kind: plan.WriteTheirs, NewBase: &meta}}

	proj := t.TempDir()
	err := advanceState(proj, st, baseSnap, actions,
		map[string][]byte{dst: []byte("theirs\n")},
		map[string]string{dst: srcPath},
		render.Vars{}, &template.Source{Kind: template.KindDir},
		false, t.TempDir(), engineVersionT, os.Stderr)
	if err != nil {
		t.Fatalf("advanceState: %v", err)
	}

	fs, ok := st.Files[dst]
	if !ok {
		t.Fatalf("state.files[%s] 缺失（保持旧 base 的路径仍须入 files）", dst)
	}
	if fs.Src != srcPath {
		t.Errorf("state.files[%s].Src = %q，want %q（必须取 newSrc[path]，不得用 Dst 顶替）",
			dst, fs.Src, srcPath)
	}
	if fs.Hash != meta.Hash {
		t.Errorf("state.files[%s].Hash = %q，want %q", dst, fs.Hash, meta.Hash)
	}
	got, err := state.ReadBase(proj, dst)
	if err != nil {
		t.Fatalf("ReadBase: %v", err)
	}
	if !bytes.Equal(got, baseBytes) {
		t.Errorf("base/%s = %q，want %q（保持旧 base 字节，不写 theirs）", dst, got, baseBytes)
	}
}

// ── T7b/M3b C4：渲染钩子杂项（I20/I21/I26/I6a/I6b，含 T6 O-3）─────────────

// TestEmptyRenderIsPresentEmptySlice 钉 I20/I21（§1.3 templates / §4.1；
// 含 T6 O-3）：
//
//	①空渲染归一——两条 nil 途径（Go template 渲染为空 / 源文件 0 字节）在
//	  Rendered 里都必须是「键存在 ∧ 非 nil ∧ 0 字节」（renderTheirs 归一，
//	  template.go:177-179：空渲染是空内容文件，不是「模板没有它」）；
//	②conflicts/ 内容条目的缺失集 ⊆ {ConflictKind ∧ theirs 缺失}——按条件句
//	  显式断言差集（classifyMerges 只在该组合下不产条目）。
func TestEmptyRenderIsPresentEmptySlice(t *testing.T) {
	t.Run("空渲染两条途径归一到非 nil 空切片", func(t *testing.T) {
		repo, commit := mkTemplateRepoT(t, map[string]string{
			"xinit.yml": "manifest_version: 1\nrender:\n  - paths: [\"t.txt\"]\n",
			"t.txt":     "{{- \"\" -}}\n", // 途径①：Go template 渲染为空（ModeTemplate）
			"z.txt":     "",               // 途径②：源文件 0 字节（ModeSubst）
		})
		dir := projectWithState(t, state.TemplateRef{Source: repo, Kind: "git", Revision: "main", Commit: commit},
			map[string]state.FileState{})
		s := sessionT(dir, mustLoadState(t, dir), t.TempDir(), false)

		tpl, err := loadTemplate(s, commit, "", false)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{"t.txt", "z.txt"} {
			data, ok := tpl.Rendered[p]
			if !ok {
				t.Errorf("Rendered[%s] 键缺失（空渲染 = 空内容文件，必须存在）", p)
				continue
			}
			if data == nil || len(data) != 0 {
				t.Errorf("Rendered[%s] = %#v（len=%d），want 非 nil 空切片（I20）", p, data, len(data))
			}
		}
	})

	t.Run("conflicts 条目缺失集 ⊆ {ConflictKind ∧ theirs 缺失}（T6 O-3）", func(t *testing.T) {
		actions := []plan.Action{
			{Path: "k1.txt", Kind: plan.ConflictKind}, // theirs 有 → 有条目
			{Path: "k2.txt", Kind: plan.ConflictKind}, // theirs 缺 → 无条目（唯一合法缺失）
			{Path: "m.txt", Kind: plan.Merge},         // 真三方冲突 → 有条目
		}
		theirsRendered := map[string][]byte{"k1.txt": []byte("K1\n"), "m.txt": []byte("T\n")}
		msnap := plan.Snap{"m.txt": {Kind: render.KindRegular, Hash: "hm"}}
		merged, conflicted, conflicts, err := classifyMerges(actions,
			&Base{Metas: msnap, Bytes: map[string][]byte{"m.txt": []byte("B\n")}},
			msnap, map[string][]byte{"m.txt": []byte("O\n")},
			msnap, theirsRendered)
		if err != nil {
			t.Fatal(err)
		}
		if len(merged) != 0 {
			t.Fatalf("三方皆异应判冲突：merged = %v", merged)
		}
		if !slices.Equal(sortedStringKeys(conflicted), []string{"k1.txt", "k2.txt", "m.txt"}) {
			t.Fatalf("conflicted = %v", sortedStringKeys(conflicted))
		}
		if !slices.Equal(sortedStringKeys(conflicts), []string{"k1.txt", "m.txt"}) {
			t.Fatalf("conflicts = %v", sortedStringKeys(conflicts))
		}
		// 显式缺失集断言：差集必须恰为 k2，且 k2 满足「ConflictKind ∧ theirs 缺失」。
		var missing []string
		for _, p := range sortedStringKeys(conflicted) {
			if _, ok := conflicts[p]; !ok {
				missing = append(missing, p)
			}
		}
		if !slices.Equal(missing, []string{"k2.txt"}) {
			t.Fatalf("conflicts 缺失集 = %v，want [k2.txt]", missing)
		}
		act := findAction(actions, "k2.txt")
		if act == nil || act.Kind != plan.ConflictKind {
			t.Errorf("缺失项必须来自 ConflictKind：%+v", act)
		}
		if _, ok := theirsRendered["k2.txt"]; ok {
			t.Error("缺失项的 theirs 必须缺失")
		}
		// 子集关系一般形（T6 O-3 的主命题）：conflicts 键 ⊆ conflicted 键。
		for p := range conflicts {
			if !conflicted[p] {
				t.Errorf("conflicts 含非冲突路径 %s（⊆ 违反）", p)
			}
		}
	})
}

// TestHooksClassifyInputs 钉 I26（§1.3 :136 的 planned/generated 口径）：planned
// = 本轮 Actions 全部路径（含 Noop——runHook 冻结为无 Kind 过滤）；generated
// 取本轮模板侧位（t.Snap 的 Generated）。钩子一次触碰三文件，覆盖三分类：
// planned 被触碰 → Modified；generated ∧ ¬planned 被触碰 → 静默；其余 → Unplanned。
func TestHooksClassifyInputs(t *testing.T) {
	dir := t.TempDir()
	writeFilesT(t, dir, map[string]string{"n.txt": "noop\n", "p.txt": "gen0\n"})
	s := sessionT(dir, &state.State{}, t.TempDir(), false)
	tpl := &Template{
		Src: &template.Source{Kind: template.KindDir},
		Manifest: &manifest.Manifest{Hooks: manifest.HooksSpec{
			PostUpdate: "echo touched >> n.txt; echo rebuilt > p.txt; echo new > extra.txt",
		}},
		Snap: plan.Snap{
			"n.txt": {Kind: render.KindRegular},                  // ¬generated
			"p.txt": {Kind: render.KindRegular, Generated: true}, // 模板侧位
		},
	}
	// Noop 也在 planned 里（冻结口径：Actions 全部路径，无 Kind 过滤）。
	actions := []plan.Action{{Path: "n.txt", Kind: plan.Noop}}

	h, err := runHook(s, tpl, actions, true)
	if err != nil {
		t.Fatal(err)
	}
	if h.Failed || h.ExitCode != 0 {
		t.Fatalf("钩子应成功：Failed=%v ExitCode=%d", h.Failed, h.ExitCode)
	}
	if !slices.Equal(h.Modified, []string{"n.txt"}) {
		t.Errorf("Modified = %v，want [n.txt]（planned 含 Noop 路径：计划内改写必须报告）", h.Modified)
	}
	if !slices.Equal(h.Unplanned, []string{"extra.txt"}) {
		t.Errorf("Unplanned = %v，want [extra.txt]（p.txt = generated∧¬planned 被触碰 → 静默）", h.Unplanned)
	}
}

// TestDryRunDoesNotLock 钉 I6a（session.go 取锁块的反面 / R12）：--dry-run 只读，
// 连 .xinit/lock 文件都不创建。对照段同夹具跑一次真路径——flock 锁文件常驻，
// 证明本用例的「不存在」不可能恒真（锁文件确会被真跑创建）。
func TestDryRunDoesNotLock(t *testing.T) {
	isolateCacheT(t)
	repo, commit := mkTemplateRepoT(t, map[string]string{"xinit.yml": tmplYAML, "a.txt": "new\n"})
	envT := func(t *testing.T) string {
		t.Helper()
		dir := projectWithState(t, state.TemplateRef{Source: repo, Kind: "git", Revision: "main", Commit: commit},
			map[string]state.FileState{"a.txt": textState("a.txt", "old\n")})
		writeFilesT(t, dir, map[string]string{"a.txt": "old\n"})
		writeFilesT(t, filepath.Join(dir, state.DirName, "base"), map[string]string{"a.txt": "old\n"})
		return dir
	}

	dir := envT(t)
	rep, err := Run(Input{ProjectDir: dir, Revision: commit, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.DryRun {
		t.Fatal("夹具前提：dry-run 报告")
	}
	if _, err := os.Stat(filepath.Join(dir, state.DirName, "lock")); !os.IsNotExist(err) {
		t.Errorf("--dry-run 不得创建锁文件（R12）：stat err = %v", err)
	}

	// 对照：真跑路径必留 lock 文件。
	dir2 := envT(t)
	if _, err := Run(Input{ProjectDir: dir2, Revision: commit}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir2, state.DirName, "lock")); err != nil {
		t.Errorf("对照：真跑应创建锁文件（flock 常驻）：%v", err)
	}
}

// conflictHooksEnvT 造 I6b 三场景共用的「有提问机会」夹具：a.txt 三方冲突
// （Merge 判冲突，可问 §19.4.1）+ 清单声明 post_update（未信任）——dry-run 若
// 误读 stdin 或 --yes 语义错位，计数装置必捕获。
func conflictHooksEnvT(t *testing.T) (dir, commit string) {
	t.Helper()
	isolateCacheT(t)
	repo, commit := mkTemplateRepoT(t, map[string]string{
		"xinit.yml": "manifest_version: 1\nhooks:\n  post_update: |\n    echo hi\n",
		"a.txt":     "T\n",
	})
	dir = projectWithState(t, state.TemplateRef{Source: repo, Kind: "git", Revision: "main", Commit: commit},
		map[string]state.FileState{"a.txt": textState("a.txt", "B\n")})
	writeFilesT(t, dir, map[string]string{"a.txt": "O\n"}) // ours 本地改过 → 三方皆异
	writeFilesT(t, filepath.Join(dir, state.DirName, "base"), map[string]string{"a.txt": "B\n"})
	return dir, commit
}

// TestDryRunNeverReadsStdin 钉 I6b（R-M3b-4）：dry-run 全程零读 stdin——计数
// 证据（stdin 读取只发生在 ConfirmTrust/AskConflict 两方法）。三场景：
//
//	①dry-run：两计数均为 0；
//	②--yes：信任门短路（trustCalls==0）但冲突提问不受影响（conflictCalls==1）；
//	③对照（非 dry、非 yes）：两计数各 1——证明计数装置在本夹具上非恒零
//	  （防「零提问」因环境不具提问条件而假阳性）。
func TestDryRunNeverReadsStdin(t *testing.T) {
	t.Run("dry-run 全程零提问", func(t *testing.T) {
		dir, commit := conflictHooksEnvT(t)
		ci := newCountingInteractive("")
		rep, err := Run(Input{ProjectDir: dir, Revision: commit, DryRun: true, Interactive: ci})
		if err != nil {
			t.Fatal(err)
		}
		if ci.trustCalls != 0 || ci.conflictCalls != 0 {
			t.Errorf("dry-run 不得读 stdin：ConfirmTrust=%d AskConflict=%d，want 0/0",
				ci.trustCalls, ci.conflictCalls)
		}
		if !rep.DryRun {
			t.Error("夹具前提：dry-run 报告")
		}
	})

	t.Run("--yes 短路信任门但不短路冲突提问", func(t *testing.T) {
		dir, commit := conflictHooksEnvT(t)
		ci := newCountingInteractive("")
		if _, err := Run(Input{ProjectDir: dir, Revision: commit, Yes: true, Interactive: ci}); err != nil {
			t.Fatal(err)
		}
		if ci.trustCalls != 0 {
			t.Errorf("--yes 预先确认：ConfirmTrust 应零调用，got %d", ci.trustCalls)
		}
		if ci.conflictCalls != 1 {
			t.Errorf("--yes 不影响冲突提问（a.txt 一条可问）：AskConflict = %d，want 1", ci.conflictCalls)
		}
	})

	t.Run("对照：非 dry/非 yes 两问各一次（装置有效性）", func(t *testing.T) {
		dir, commit := conflictHooksEnvT(t)
		ci := newCountingInteractive("")
		rep, err := Run(Input{ProjectDir: dir, Revision: commit, Interactive: ci})
		if err != nil {
			t.Fatal(err)
		}
		if ci.conflictCalls != 1 || ci.trustCalls != 1 {
			t.Errorf("对照场景应各问一次（计数装置检查）：AskConflict=%d ConfirmTrust=%d",
				ci.conflictCalls, ci.trustCalls)
		}
		if rep.Verdict != VerdictNeedTrust {
			t.Errorf("信任拒绝（EOF 锁存）应 rc4：Verdict = %v", rep.Verdict)
		}
	})
}
