package cli

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/xbcio/xinit/internal/state"
	"github.com/xbcio/xinit/internal/update"
)

// ── 局部 helper（只服务快照写侧测试；夹具自证用） ─────────────────────────

// snapshotManifestJSON 是快照清单里测试用到的字段：renderstore 的清单结构
// 未导出，这里按 §21.4 冻结的 JSON 字段名独立解码（读侧校验链另由 T4 验）。
type snapshotManifestJSON struct {
	Version    int    `json:"version"`
	Engine     string `json:"engine"`
	Source     string `json:"source"`
	Kind       string `json:"kind"`
	Anchor     string `json:"anchor"`
	VarsSHA256 string `json:"vars_sha256"`
	Files      []struct {
		Dst       string `json:"dst"`
		RawSHA256 string `json:"raw_sha256"`
		Size      int    `json:"size"`
	} `json:"files"`
}

// readSnapManifestT 读并解析条目里的 manifest.json。
func readSnapManifestT(t *testing.T, entry string) snapshotManifestJSON {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(entry, "manifest.json"))
	if err != nil {
		t.Fatalf("读快照清单: %v", err)
	}
	var m snapshotManifestJSON
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("解析快照清单: %v", err)
	}
	return m
}

// renderEntryT 返回唯一快照条目目录 <cacheRoot>/render/<familyKey>
// （0 个或多于 1 个条目都视为夹具/实现异常）。
func renderEntryT(t *testing.T, cacheRoot string) string {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join(cacheRoot, "render"))
	if err != nil {
		t.Fatalf("读快照 render/ 目录: %v", err)
	}
	if len(ents) != 1 {
		t.Fatalf("render/ 下条目 = %d 个，want 1（%v）", len(ents), ents)
	}
	return filepath.Join(cacheRoot, "render", ents[0].Name())
}

// captureStderrT 捕获 f 执行期间写到 os.Stderr 的全部输出（模式同
// captureStdoutT；快照失败警告走 stderr）。
func captureStderrT(t *testing.T, f func()) string {
	t.Helper()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStderr := os.Stderr
	os.Stderr = pw
	f()
	os.Stderr = oldStderr
	pw.Close()
	out, err := io.ReadAll(pr)
	pr.Close()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// ── 挂点测试（§21.5 写侧三挂点） ─────────────────────────────────────────

// ① init 挂点：git 局部仓模板 init 后，条目存在、tree/ 字节 == 渲染原文。
func TestInitWritesRenderSnapshot(t *testing.T) {
	cacheRoot := isolateCacheT(t)
	repo, _ := initProjectT(t, map[string]string{
		"xinit.yml": tmplYAML,
		"a.txt":     "hello\n",
		"sub/b.txt": "bee\n",
	})

	entry := renderEntryT(t, cacheRoot)
	if _, err := os.Stat(filepath.Join(entry, "manifest.json")); err != nil {
		t.Fatalf("render/*/manifest.json 应存在: %v", err)
	}
	if got := mustContent(t, entry, "tree/a.txt"); got != "hello\n" {
		t.Errorf("tree/a.txt = %q，want 渲染原文 %q", got, "hello\n")
	}
	if got := mustContent(t, entry, "tree/sub/b.txt"); got != "bee\n" {
		t.Errorf("tree/sub/b.txt = %q，want 渲染原文 %q", got, "bee\n")
	}
	m := readSnapManifestT(t, entry)
	if want := runGitT(t, repo, "rev-parse", "v1.0.0"); m.Anchor != want {
		t.Errorf("清单 anchor = %q，want init 轮模板 commit %q", m.Anchor, want)
	}
}

// ② update 挂点：字节源必须是本轮 theirsRendered 全量渲染原文——既不是写回
// 工作树的合并产物，也不是 blobs 混合体（保持旧值路径装的是旧 base 字节）。
//
// 夹具构造（多行分部位）：x.txt 本地改第 1 行、模板改第 3 行 → 干净合并轮
// （NewBase 落定到 theirs，SettleMergeClean）；gen.txt 是 generated 路径、
// 本轮钩子不会运行（清单未声明 post_update）→ L7 跳过（base 保持旧值）。
// 两者同轮、均非 pending——快照必须整条重写。
func TestUpdateSnapshotUsesTheirsRendered(t *testing.T) {
	cacheRoot := isolateCacheT(t)
	repo, proj := initProjectT(t, map[string]string{
		"xinit.yml": genYAML,
		"x.txt":     "base1\nbase2\nbase3\n",
		"gen.txt":   "g1\n",
	})
	writeFilesT(t, proj, map[string]string{
		"x.txt":   "mine\nbase2\nbase3\n",
		"gen.txt": "g_ours\n", // 模拟钩子产出
	})
	commitT(t, repo, map[string]string{
		"x.txt":   "base1\nbase2\ntheirs3\n",
		"gen.txt": "g2\n",
	}, "v1.1.0")

	rep, err := runReport(UpdateOptions{ProjectDir: proj})
	if err != nil {
		t.Fatal(err)
	}
	if codeOf(rep.Verdict) != CodeOK {
		t.Fatalf("干净合并 + 跳过轮应无待解决项: %+v", rep)
	}
	if !slices.Contains(rep.Merged, "x.txt") {
		t.Fatalf("夹具前提不成立：x.txt 应走干净合并轮: %+v", rep)
	}
	if got := mustContent(t, proj, "x.txt"); got != "mine\nbase2\ntheirs3\n" {
		t.Fatalf("夹具前提不成立：工作树应为合并产物（含本地行）: %q", got)
	}

	entry := renderEntryT(t, cacheRoot)
	// 判别器 A（vs 合并产物/工作树字节）：快照取全量渲染原文，不是合并结果。
	if got := mustContent(t, entry, "tree/x.txt"); got != "base1\nbase2\ntheirs3\n" {
		t.Errorf("tree/x.txt = %q，want 本轮 theirsRendered %q（不得取合并产物）",
			got, "base1\nbase2\ntheirs3\n")
	}
	// 判别器 B（vs blobs 混合体）：L7 跳过路径的 blobs 装的是旧 base 字节
	//（g1），快照必须仍是本轮渲染原文（g2）——误取 blobs 时此处为 g1。
	if got := mustContent(t, entry, "tree/gen.txt"); got != "g2\n" {
		t.Errorf("tree/gen.txt = %q，want 本轮 theirsRendered %q（不得取 blobs）",
			got, "g2\n")
	}
	// 对照：干净合并轮的 base 落定到 theirs；跳过路径的 base 保持旧值。
	if got := mustContent(t, state.BaseDir(proj), "x.txt"); got != "base1\nbase2\ntheirs3\n" {
		t.Errorf("干净合并轮的 base 应落定到 theirs（SettleMergeClean）: %q", got)
	}
	if got := mustContent(t, state.BaseDir(proj), "gen.txt"); got != "g1\n" {
		t.Errorf("跳过路径的 base 应保持旧值: %q", got)
	}
	m := readSnapManifestT(t, entry)
	if want := runGitT(t, repo, "rev-parse", "v1.1.0"); m.Anchor != want {
		t.Errorf("清单 anchor = %q，want 本轮模板 commit %q", m.Anchor, want)
	}
}

// ③ adopt 挂点：dir 源 adopt 后条目存在，tree/ 字节 == 渲染结果（不是模板原字节）。
func TestAdoptWritesRenderSnapshot(t *testing.T) {
	cacheRoot := isolateCacheT(t)
	tpl := t.TempDir()
	writeFilesT(t, tpl, map[string]string{
		"xinit.yml": "manifest_version: 1\n" +
			"vars:\n  - name: app\n    default: \"demo\"\n" +
			"substitute:\n  - from: \"PLACEHOLDER\"\n    to: \"{{ .app }}\"\n    paths: [\"a.txt\"]\n",
		"a.txt":     "PLACEHOLDER\n",
		"sub/b.txt": "bee\n",
	})
	cfg := filepath.Join(t.TempDir(), "vars.yml")
	if err := os.WriteFile(cfg, []byte("app: demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	proj := t.TempDir()
	if err := Adopt(AdoptOptions{ProjectDir: proj, Template: tpl, ConfigPath: cfg}); err != nil {
		t.Fatalf("Adopt: %v", err)
	}

	entry := renderEntryT(t, cacheRoot)
	if got := mustContent(t, entry, "tree/a.txt"); got != "demo\n" {
		t.Errorf("tree/a.txt = %q，want 渲染结果 %q（substitute 已应用，不是模板原字节）",
			got, "demo\n")
	}
	if got := mustContent(t, entry, "tree/sub/b.txt"); got != "bee\n" {
		t.Errorf("tree/sub/b.txt = %q，want 渲染原文 %q", got, "bee\n")
	}
	m := readSnapManifestT(t, entry)
	if st := mustLoadState(t, proj); m.Anchor != st.Template.Commit {
		t.Errorf("清单 anchor = %q，want dir 源树 hash %q", m.Anchor, st.Template.Commit)
	}
}

// ④ update 跳过：pending 轮（冲突）commit 不推进，快照一个字节都不许动
// （写了会覆写掉当下唯一 anchor 相符的有效条目）；收敛轮再重写、anchor 更新。
//
// 夹具：本轮除 a.txt 同点冲突（pending）外，模板还新增 b.txt（干净
// WriteTheirs）。dst 集因此在 pending 轮发生变化（{a.txt} → {a.txt, b.txt}）——
// 这正是「漏挂 pending 门」唯一可观测的路径：dst 集不变时误写恰好命中写侧
// 跳过判据（条目与旧 anchor 全匹配）而不可见；变了则整条重写，把 tree/a.txt
// 覆写成新一轮渲染原文、anchor 却停在旧 commit（条目从此撒谎）。
func TestUpdatePendingRoundSkipsSnapshot(t *testing.T) {
	cacheRoot := isolateCacheT(t)
	repo, proj := initProjectT(t, map[string]string{
		"xinit.yml": tmplYAML,
		"a.txt":     "a\nb\nc\n",
	})
	initAnchor := runGitT(t, repo, "rev-parse", "v1.0.0")
	entry := renderEntryT(t, cacheRoot)
	if got := readSnapManifestT(t, entry).Anchor; got != initAnchor {
		t.Fatalf("init 轮清单 anchor = %q，want %q", got, initAnchor)
	}

	// 同点修改 → 冲突轮（pending）；b.txt 为模板新增。
	writeFilesT(t, proj, map[string]string{"a.txt": "a\nB_ours\nc\n"})
	commitT(t, repo, map[string]string{
		"a.txt": "a\nB_theirs\nc\n",
		"b.txt": "new-b\n",
	}, "v1.1.0")
	renderDir := filepath.Join(cacheRoot, "render")
	before := readTreeT(t, renderDir)

	rep, err := runReport(UpdateOptions{ProjectDir: proj})
	if err != nil {
		t.Fatal(err)
	}
	if codeOf(rep.Verdict) != CodePending {
		t.Fatalf("同点修改应入冲突轮（退出码 2）: %+v", rep)
	}
	if got := mustContent(t, proj, "b.txt"); got != "new-b\n" {
		t.Fatalf("夹具前提不成立：pending 轮的非冲突路径应照常落盘: %q", got)
	}
	if after := readTreeT(t, renderDir); !reflect.DeepEqual(before, after) {
		t.Error("pending 轮不得触碰 render/ 快照（dst 集已变，写了会把旧 anchor 的条目覆写成本轮渲染）")
	}

	// 收敛轮：本地对齐到 theirs → 非 pending → 条目重写、anchor 更新。
	writeFilesT(t, proj, map[string]string{"a.txt": "a\nB_theirs\nc\n"})
	rep2, err := runReport(UpdateOptions{ProjectDir: proj})
	if err != nil {
		t.Fatal(err)
	}
	if codeOf(rep2.Verdict) != CodeOK {
		t.Fatalf("对齐内容后应变干净轮: %+v", rep2)
	}
	m := readSnapManifestT(t, entry)
	if want := runGitT(t, repo, "rev-parse", "v1.1.0"); m.Anchor != want {
		t.Errorf("收敛轮应重写条目并推进 anchor: %q，want %q", m.Anchor, want)
	}
	if got := mustContent(t, entry, "tree/a.txt"); got != "a\nB_theirs\nc\n" {
		t.Errorf("tree/a.txt = %q，want 本轮渲染原文", got)
	}
	if got := mustContent(t, entry, "tree/b.txt"); got != "new-b\n" {
		t.Errorf("tree/b.txt = %q，want 本轮渲染原文", got)
	}
}

// ⑤ 失败语义：快照写失败 → stderr 恰一行警告、命令照常成功、项目状态零影响。
func TestSnapshotWriteFailureDoesNotFailCommand(t *testing.T) {
	cacheRoot := isolateCacheT(t)
	repo, _ := mkTemplateRepoT(t, map[string]string{
		"xinit.yml": tmplYAML,
		"a.txt":     "one\n",
	})
	runGitT(t, repo, "tag", "v1.0.0")
	projCtl := t.TempDir()
	projFault := t.TempDir()
	for _, proj := range []string{projCtl, projFault} {
		if err := Init(proj, repo, "", "v1.0.0", true, false, false, false, false); err != nil {
			t.Fatalf("Init(%s): %v", proj, err)
		}
	}
	commitT(t, repo, map[string]string{"a.txt": "two\n"}, "v1.1.0")

	// 对照轮（无故障）：拿到正常轮的退出码。
	repCtl, err := runReport(UpdateOptions{ProjectDir: projCtl})
	if err != nil {
		t.Fatal(err)
	}
	if codeOf(repCtl.Verdict) != CodeOK {
		t.Fatalf("对照轮应成功: %+v", repCtl)
	}

	// 故障注入：<cacheRoot>/render 换成同名普通文件。
	renderPath := filepath.Join(cacheRoot, "render")
	if err := os.RemoveAll(renderPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(renderPath, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}

	var rep *update.Report
	var uerr error
	stderrOut := captureStderrT(t, func() {
		rep, uerr = runReport(UpdateOptions{ProjectDir: projFault})
	})
	if uerr != nil {
		t.Fatalf("快照写失败不得让命令报错: %v", uerr)
	}
	if codeOf(rep.Verdict) != codeOf(repCtl.Verdict) {
		t.Errorf("codeOf(rep.Verdict) = %d（无故障对照 %d）——快照写失败不得影响命令退出码",
			codeOf(rep.Verdict), codeOf(repCtl.Verdict))
	}
	if got := mustContent(t, projFault, "a.txt"); got != "two\n" {
		t.Errorf("快照写失败不影响命令正常落盘: a.txt = %q", got)
	}
	if n := strings.Count(stderrOut, "渲染快照写入失败（不影响本轮）"); n != 1 {
		t.Errorf("快照写失败警告 = %d 行，want 恰 1 行\nstderr:\n%s", n, stderrOut)
	}
}

// ⑥ init 挂点位置（§21.5）：钩子失败（rc=3，项目保留、不回删）时快照必须
// 已写出且与落盘 base 一致——挂点在钩子段之前，钩子失败不影响快照。
func TestInitHookFailureStillWritesSnapshot(t *testing.T) {
	cacheRoot := isolateCacheT(t)
	repo, _ := mkTemplateRepoT(t, map[string]string{
		"xinit.yml": "manifest_version: 1\nhooks:\n  post_init: |\n    exit 9\n",
		"a.txt":     "one\n",
	})
	runGitT(t, repo, "tag", "v1.0.0")
	proj := t.TempDir()

	err := Init(proj, repo, "", "v1.0.0", true, false, false, true, false)
	var ee *ExitError
	if !errors.As(err, &ee) || ee.Code != CodeHookFail {
		t.Fatalf("post_init 失败应 ExitError{3}: %v", err)
	}

	entry := renderEntryT(t, cacheRoot)
	if got := mustContent(t, entry, "tree/a.txt"); got != "one\n" {
		t.Errorf("钩子失败也须已写快照: tree/a.txt = %q", got)
	}
	if got := mustContent(t, state.BaseDir(proj), "a.txt"); got != "one\n" {
		t.Errorf("base/a.txt = %q，want 渲染原文（快照应与已落盘 base 一致）", got)
	}
}
