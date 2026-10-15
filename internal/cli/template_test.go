package cli

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/xbcio/xinit/internal/state"
	"github.com/xbcio/xinit/internal/template"
)

// commitMsgT 在模板仓里提交一批改动，标题自定；tag 为空则不打 tag。
func commitMsgT(t *testing.T, repo string, files map[string]string, subject, tag string) {
	t.Helper()
	writeFilesT(t, repo, files)
	runGitT(t, repo, "add", "-A")
	runGitT(t, repo, "commit", "-q", "-m", subject)
	if tag != "" {
		runGitT(t, repo, "tag", tag)
	}
}

func runTemplate(t *testing.T, o TemplateOptions) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := Template(o, &out)
	return out.String(), err
}

// 带两个新版本的模板仓：项目记录 v1.0.0；v1.1.0 含 alpha/beta 两个提交，v1.2.0 含 gamma。
func templateWithReleases(t *testing.T) (repo, proj string) {
	t.Helper()
	repo, proj = initProjectT(t, map[string]string{"xinit.yml": tmplYAML, "a.txt": "one\n"})
	commitMsgT(t, repo, map[string]string{"a.txt": "alpha\n"}, "feat: alpha", "")
	commitMsgT(t, repo, map[string]string{"a.txt": "beta\n"}, "feat: beta", "v1.1.0")
	commitMsgT(t, repo, map[string]string{"a.txt": "gamma\n"}, "fix: gamma", "v1.2.0")
	return repo, proj
}

func TestTemplateBareShowsSideSummaryOnly(t *testing.T) {
	_, proj := templateWithReleases(t)
	out, err := runTemplate(t, TemplateOptions{ProjectDir: proj})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"xinit template: ",
		"模板侧:",
		"记录版本: v1.0.0（commit ",
		"可用更新 2 个: v1.1.0, v1.2.0（最新 v1.2.0）",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺少 %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "变更") || strings.Contains(out, "feat: beta") {
		t.Errorf("不带 --changelog 不得列出变更:\n%s", out)
	}
}

// 与 status 的模板侧一段逐字相同（两者共用同一份排版）。
func TestTemplateSideMatchesStatus(t *testing.T) {
	_, proj := templateWithReleases(t)
	rep, err := runStatus(StatusOptions{ProjectDir: proj})
	if err != nil {
		t.Fatal(err)
	}
	var st bytes.Buffer
	rep.Print(&st)
	out, err := runTemplate(t, TemplateOptions{ProjectDir: proj})
	if err != nil {
		t.Fatal(err)
	}
	side := out[strings.Index(out, "模板侧:"):]
	if !strings.Contains(st.String(), side) {
		t.Errorf("template 的模板侧应与 status 一致\n--- template ---\n%s\n--- status ---\n%s", side, st.String())
	}
}

func TestTemplateWritesNothing(t *testing.T) {
	_, proj := templateWithReleases(t)
	before := readTreeT(t, proj)
	if _, err := runTemplate(t, TemplateOptions{ProjectDir: proj, Changelog: true}); err != nil {
		t.Fatal(err)
	}
	if after := readTreeT(t, proj); !reflect.DeepEqual(before, after) {
		t.Error("template 不得写任何文件（含 .xinit/lock）")
	}
}

func TestTemplateChangelogListsSectionsNewestFirst(t *testing.T) {
	_, proj := templateWithReleases(t)
	out, err := runTemplate(t, TemplateOptions{ProjectDir: proj, Changelog: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "变更（v1.0.0 → v1.2.0）") {
		t.Errorf("缺少区间标题:\n%s", out)
	}
	i120, i110 := strings.Index(out, "\nv1.2.0\n"), strings.Index(out, "\nv1.1.0\n")
	if i120 < 0 || i110 < 0 || i120 > i110 {
		t.Errorf("各版本段应存在且新版本在前（v1.2.0 先于 v1.1.0）:\n%s", out)
	}
	for _, want := range []string{"  - fix: gamma", "  - feat: beta", "  - feat: alpha"} {
		if !strings.Contains(out, want) {
			t.Errorf("缺少提交标题行 %q:\n%s", want, out)
		}
	}
	// 段内新→旧：beta 先于 alpha
	if strings.Index(out, "feat: beta") > strings.Index(out, "feat: alpha") {
		t.Errorf("段内应新→旧:\n%s", out)
	}
}

func TestTemplateChangelogExplicitRevision(t *testing.T) {
	_, proj := templateWithReleases(t)
	out, err := runTemplate(t, TemplateOptions{ProjectDir: proj, Changelog: true, Revision: "v1.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "变更（v1.0.0 → v1.1.0）") {
		t.Errorf("区间应止于显式目标:\n%s", out)
	}
	if strings.Contains(out, "fix: gamma") {
		t.Errorf("不得越过显式目标:\n%s", out)
	}
}

func TestTemplateChangelogAlreadyLatest(t *testing.T) {
	repo, proj := initProjectT(t, map[string]string{"xinit.yml": tmplYAML, "a.txt": "one\n"})
	_ = repo
	out, err := runTemplate(t, TemplateOptions{ProjectDir: proj, Changelog: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "变更: 无（记录版本已是最新稳定版本 v1.0.0）") {
		t.Errorf("记录版本即最新应明说:\n%s", out)
	}
}

// 记录版本是未打 tag 的 HEAD、且已领先最新稳定 tag：不是错误，也不是「降级」。
func TestTemplateChangelogRecordedAheadOfLatestTag(t *testing.T) {
	repo, proj := initProjectT(t, map[string]string{"xinit.yml": tmplYAML, "a.txt": "one\n"})
	commitMsgT(t, repo, map[string]string{"a.txt": "ahead\n"}, "wip: ahead", "")
	st := mustLoadState(t, proj)
	st.Template.Revision = "HEAD"
	st.Template.Commit = runGitT(t, repo, "rev-parse", "HEAD")
	if err := st.Save(proj); err != nil {
		t.Fatal(err)
	}
	out, err := runTemplate(t, TemplateOptions{ProjectDir: proj, Changelog: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "变更: 无（记录版本已不低于最新稳定版本 v1.0.0）") {
		t.Errorf("领先于最新稳定 tag 应如实说明:\n%s", out)
	}
}

func TestTemplateChangelogExplicitOlderTargetIsRollback(t *testing.T) {
	_, proj := templateWithReleases(t)
	st := mustLoadState(t, proj)
	st.Template.Revision = "v1.2.0"
	st.Template.Commit = "" // 由下面按仓库实况补
	repoDir := st.Template.Source
	st.Template.Commit = runGitT(t, repoDir, "rev-parse", "v1.2.0^{commit}")
	if err := st.Save(proj); err != nil {
		t.Fatal(err)
	}
	out, err := runTemplate(t, TemplateOptions{ProjectDir: proj, Changelog: true, Revision: "v1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "比记录版本更旧") || !strings.Contains(out, "回退") {
		t.Errorf("显式指定更旧目标应说明是回退目标:\n%s", out)
	}
}

// 记录的 revision 不是 vX.Y.Z（如 HEAD）：区间标题的起点用短 commit，不能出现 "HEAD"。
func TestTemplateChangelogLabelsNonSemverStartWithShortCommit(t *testing.T) {
	repo, _ := mkTemplateRepoT(t, map[string]string{"xinit.yml": tmplYAML, "a.txt": "one\n"})
	c0 := runGitT(t, repo, "rev-parse", "HEAD")
	commitMsgT(t, repo, map[string]string{"a.txt": "two\n"}, "feat: two", "v1.0.0")
	proj := projectWithState(t, state.TemplateRef{
		Source: repo, Kind: "git", Revision: "HEAD", Commit: c0,
	}, map[string]state.FileState{})
	out, err := runTemplate(t, TemplateOptions{ProjectDir: proj, Changelog: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "变更（"+c0[:8]+" → v1.0.0）") {
		t.Errorf("起点应是 8 位短 commit:\n%s", out)
	}
	if !strings.Contains(out, "  - feat: two") {
		t.Errorf("缺少提交标题:\n%s", out)
	}
}

// M-3(a)：cli 层措辞分支的回归锁（评审实测正确但无锁）。
func TestTemplateChangelogWordingBranches(t *testing.T) {
	// 分叉：记录 commit 与目标 commit 互不为祖先（从同一基点各分一支）
	repo, _ := mkTemplateRepoT(t, map[string]string{"xinit.yml": tmplYAML, "a.txt": "one\n"})
	runGitT(t, repo, "tag", "v1.0.0")
	runGitT(t, repo, "checkout", "-q", "-b", "side")
	commitMsgT(t, repo, map[string]string{"s.txt": "side\n"}, "side: work", "")
	side := runGitT(t, repo, "rev-parse", "HEAD")
	runGitT(t, repo, "checkout", "-q", "-")
	commitMsgT(t, repo, map[string]string{"a.txt": "two\n"}, "feat: two", "")
	recorded := runGitT(t, repo, "rev-parse", "HEAD")
	proj := projectWithState(t, state.TemplateRef{
		Source: repo, Kind: "git", Revision: "v1.0.0", Commit: recorded,
	}, map[string]state.FileState{})
	out, err := runTemplate(t, TemplateOptions{ProjectDir: proj, Changelog: true, Revision: side})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "不在同一条历史线上") {
		t.Errorf("互不为祖先应给出明确措辞:\n%s", out)
	}

	// 空段：版本序与拓扑序不一致——v2.0.0 打在祖先提交上，v1.5.0 打在其后代上；
	// 升序处理时 v2.0.0 段的范围（后代→祖先）为空，输出占位行而不是静默消失
	repo2, proj2 := initProjectT(t, map[string]string{"xinit.yml": tmplYAML, "a.txt": "one\n"})
	commitMsgT(t, repo2, map[string]string{"a.txt": "two\n"}, "feat: two", "")
	runGitT(t, repo2, "tag", "v2.0.0")
	commitMsgT(t, repo2, map[string]string{"a.txt": "three\n"}, "feat: three", "v1.5.0")
	out2, err := runTemplate(t, TemplateOptions{ProjectDir: proj2, Changelog: true, Revision: "HEAD"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out2, "（没有非 merge 提交）") {
		t.Errorf("无提交的段应有占位行:\n%s", out2)
	}

	// 显式 sha 命中已打 tag 的 commit：末段以 sha 原文命名，不替换成 tag 名
	sha := runGitT(t, repo2, "rev-parse", "v2.0.0")
	out3, err := runTemplate(t, TemplateOptions{ProjectDir: proj2, Changelog: true, Revision: sha})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out3, "变更（v1.0.0 → "+sha+"）:") {
		t.Errorf("末段应以 sha 原文命名:\n%s", out3)
	}
	if strings.Contains(out3, "→ v2.0.0）") {
		t.Errorf("点名 sha 时不得把终点名换成 tag:\n%s", out3)
	}
}

func TestTemplateRevisionRequiresChangelog(t *testing.T) {
	_, proj := templateWithReleases(t)
	_, err := runTemplate(t, TemplateOptions{ProjectDir: proj, Revision: "v1.1.0"})
	if err == nil || !strings.Contains(err.Error(), "--changelog") {
		t.Fatalf("--revision 单独使用应报错并点名 --changelog，got %v", err)
	}
}

func TestTemplateChangelogRejectsDirSource(t *testing.T) {
	proj := projectWithState(t, state.TemplateRef{
		Source: t.TempDir(), Kind: "dir", Commit: "treehash",
	}, map[string]state.FileState{})
	_, err := runTemplate(t, TemplateOptions{ProjectDir: proj, Changelog: true})
	if err == nil || !strings.Contains(err.Error(), "dir 源") {
		t.Fatalf("dir 源没有版本历史，--changelog 应报错，got %v", err)
	}
	// 不带 --changelog 的摘要仍可用，且与 status 同口径
	out, err := runTemplate(t, TemplateOptions{ProjectDir: proj})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "dir 源：无版本概念") {
		t.Errorf("dir 源摘要应说明无版本概念:\n%s", out)
	}
}

func TestTemplateMirrorMissing(t *testing.T) {
	proj := projectWithState(t, state.TemplateRef{
		Source: "https://example.invalid/tpl.git", Kind: "git",
		Revision: "v1.0.0", Commit: "0123456789abcdef0123456789abcdef01234567",
	}, map[string]state.FileState{})
	t.Setenv("HOME", t.TempDir())

	// 摘要：降级为说明（与 status 同口径），不报错
	out, err := runTemplate(t, TemplateOptions{ProjectDir: proj})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "无法读取模板 tag") {
		t.Errorf("镜像缺失的摘要应降级为说明:\n%s", out)
	}
	// 命令名必须说对：共享文案曾硬编码 "status"，template 输出点名了别的命令（评审 M-1）
	if !strings.Contains(out, "template 不创建缓存") {
		t.Errorf("降级说明应点名 template 自己:\n%s", out)
	}
	if strings.Contains(out, "status 不创建缓存") {
		t.Errorf("template 的输出不得点名 status:\n%s", out)
	}

	// --changelog：没有历史可读就是硬错误，且可用 errors.Is 识别
	_, err = runTemplate(t, TemplateOptions{ProjectDir: proj, Changelog: true})
	if !errors.Is(err, template.ErrMirrorMissing) {
		t.Fatalf("--changelog 镜像缺失应返回 ErrMirrorMissing，got %v", err)
	}
}

func TestTemplateMissingState(t *testing.T) {
	_, err := runTemplate(t, TemplateOptions{ProjectDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "xinit adopt") {
		t.Fatalf("状态缺失应给出 adopt 指引，got %v", err)
	}
}

func TestTemplateCmdFlagsAndHelp(t *testing.T) {
	var out bytes.Buffer
	root := newRootCmd()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"template", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--changelog", "--revision", "--fetch", "xinit changelog"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("template --help 缺少 %q:\n%s", want, out.String())
		}
	}
}
