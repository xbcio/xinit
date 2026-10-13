package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/xbcio/xinit/internal/fsutil"
	"github.com/xbcio/xinit/internal/plan"
	"github.com/xbcio/xinit/internal/state"
)

// genYAML 是带 generated 声明的清单（生成物跳过的夹具用）。
const genYAML = "manifest_version: 1\ngenerated:\n  paths: [\"gen.txt\"]\n"

// commitT 在模板仓里提交一批改动并打 tag。
func commitT(t *testing.T, repo string, files map[string]string, tag string) {
	t.Helper()
	writeFilesT(t, repo, files)
	runGitT(t, repo, "add", "-A")
	runGitT(t, repo, "commit", "-q", "-m", tag)
	runGitT(t, repo, "tag", tag)
}

// initProjectT 建模板仓（打 v1.0.0）、init 出派生项目，返回 (repo, proj)。
func initProjectT(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	repo, _ := mkTemplateRepoT(t, files)
	runGitT(t, repo, "tag", "v1.0.0")
	proj := t.TempDir()
	if err := Init(proj, repo, "", "v1.0.0", true, false, false, false, false); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return repo, proj
}

// mustContent 读文件内容。
func mustContent(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// captureStdoutT 捕获 f 执行期间写到 os.Stdout 的全部输出（报告断言用；
// 报告走 stdout、错误走返回值的同一次运行内对照）。
func captureStdoutT(t *testing.T, f func()) string {
	t.Helper()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdout := os.Stdout
	os.Stdout = pw
	f()
	os.Stdout = oldStdout
	pw.Close()
	out, err := io.ReadAll(pr)
	pr.Close()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// readTreeT 是整棵树的快照（相对路径 → "perm:content"），dry-run 的
// 「一个字节都不写」断言用它。
func readTreeT(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = fmt.Sprintf("%04o:%s", fi.Mode().Perm(), b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestUpdateAppliesTemplateChanges(t *testing.T) {
	repo, proj := initProjectT(t, map[string]string{
		"xinit.yml": tmplYAML,
		"a.txt":     "one\n",
		"b.txt":     "bee\n",
	})
	commitT(t, repo, map[string]string{
		"b.txt": "bee v2\n",
		"c.txt": "see\n",
	}, "v1.1.0")

	rep, err := runReport(UpdateOptions{ProjectDir: proj})
	if err != nil {
		t.Fatal(err)
	}
	if codeOf(rep.Verdict) != CodeOK {
		t.Fatalf("codeOf(rep.Verdict) = %d，报告 = %+v", codeOf(rep.Verdict), rep)
	}
	if !slices.Equal(rep.Written, []string{"b.txt", "c.txt"}) {
		t.Errorf("Written = %v，want [b.txt c.txt]", rep.Written)
	}
	if got := mustContent(t, proj, "b.txt"); got != "bee v2\n" {
		t.Errorf("b.txt = %q", got)
	}
	if got := mustContent(t, proj, "c.txt"); got != "see\n" {
		t.Errorf("c.txt = %q", got)
	}
	if got := mustContent(t, proj, "a.txt"); got != "one\n" {
		t.Errorf("a.txt 不应变: %q", got)
	}
	if rep.Merged != nil && len(rep.Merged) != 0 {
		t.Errorf("Merged = %v", rep.Merged)
	}

	st := mustLoadState(t, proj)
	if st.Template.Revision != "v1.1.0" {
		t.Errorf("Template.Revision = %q，want v1.1.0", st.Template.Revision)
	}
	if len(st.Files) != 3 {
		t.Errorf("files 条目 = %d，want 3", len(st.Files))
	}
	if got := mustContent(t, state.BaseDir(proj), "b.txt"); got != "bee v2\n" {
		t.Errorf("base/b.txt = %q", got)
	}
	if got := mustContent(t, state.BaseDir(proj), "c.txt"); got != "see\n" {
		t.Errorf("base/c.txt = %q", got)
	}
}

func TestUpdateIsIdempotent(t *testing.T) {
	repo, proj := initProjectT(t, map[string]string{
		"xinit.yml": tmplYAML,
		"a.txt":     "one\n",
	})
	commitT(t, repo, map[string]string{"a.txt": "two\n"}, "v1.1.0")

	if _, err := runReport(UpdateOptions{ProjectDir: proj}); err != nil {
		t.Fatal(err)
	}
	rep, err := runReport(UpdateOptions{ProjectDir: proj})
	if err != nil {
		t.Fatal(err)
	}
	if codeOf(rep.Verdict) != CodeOK {
		t.Fatalf("第二次 update 应成功: %+v", rep)
	}
	for _, a := range rep.Actions {
		if a.Kind != plan.Noop {
			t.Errorf("第二次 update 出现非 Noop：%s %s", a.Kind, a.Path)
		}
	}
	if len(rep.Written)+len(rep.Deleted)+len(rep.Chmodded)+len(rep.Conflicted) != 0 {
		t.Errorf("第二次 update 有动作: %+v", rep)
	}
}

func TestUpdateConflictLifecycle(t *testing.T) {
	repo, proj := initProjectT(t, map[string]string{
		"xinit.yml": tmplYAML,
		"a.txt":     "a\nb\nc\n",
	})
	writeFilesT(t, proj, map[string]string{"a.txt": "a\nB_ours\nc\n"})
	commitT(t, repo, map[string]string{"a.txt": "a\nB_theirs\nc\n"}, "v1.1.0")

	// 第一跑：冲突 → 退出 2（Update 把它包进 ExitError），ours 不动、
	// theirs 进 conflicts/、base 与模板版本都不推进。
	var ee *ExitError
	err := Update(UpdateOptions{ProjectDir: proj})
	if !errors.As(err, &ee) || ee.Code != CodePending {
		t.Fatalf("首次冲突应 ExitError{2}: %v", err)
	}
	if got := mustContent(t, proj, "a.txt"); got != "a\nB_ours\nc\n" {
		t.Errorf("冲突时不得改 ours: %q", got)
	}
	if got := mustContent(t, proj, ".xinit/conflicts/a.txt"); got != "a\nB_theirs\nc\n" {
		t.Errorf("conflicts/a.txt = %q", got)
	}
	if st := mustLoadState(t, proj); st.Template.Revision != "v1.0.0" {
		t.Errorf("冲突不得推进模板版本: %q", st.Template.Revision)
	}
	if got := mustContent(t, state.BaseDir(proj), "a.txt"); got != "a\nb\nc\n" {
		t.Errorf("冲突不得推进 base: %q", got)
	}

	// 第二跑：未解决 → 第 4 步拒绝，磁盘一个字节都不变。
	before := readTreeT(t, proj)
	err = Update(UpdateOptions{ProjectDir: proj})
	if !errors.As(err, &ee) || ee.Code != CodePending {
		t.Fatalf("未解决重跑应 ExitError{2}: %v", err)
	}
	if after := readTreeT(t, proj); !reflect.DeepEqual(before, after) {
		t.Error("第 4 步拒绝不得写任何文件")
	}

	// 用户收敛（ours 改成与 theirs 一致）→ 第三跑成功、conflicts/ 清理、base 推进。
	writeFilesT(t, proj, map[string]string{"a.txt": "a\nB_theirs\nc\n"})
	rep, err := runReport(UpdateOptions{ProjectDir: proj})
	if err != nil {
		t.Fatal(err)
	}
	if codeOf(rep.Verdict) != CodeOK {
		t.Fatalf("收敛后应成功: %+v", rep)
	}
	if !slices.Equal(rep.Resolved, []string{"a.txt"}) {
		t.Errorf("Resolved = %v，want [a.txt]", rep.Resolved)
	}
	if _, err := os.Lstat(filepath.Join(proj, ".xinit", "conflicts")); !os.IsNotExist(err) {
		t.Error("conflicts/ 清空后应移除目录")
	}
	if st := mustLoadState(t, proj); st.Template.Revision != "v1.1.0" {
		t.Errorf("收敛后才推进模板版本: %q", st.Template.Revision)
	}
	if got := mustContent(t, state.BaseDir(proj), "a.txt"); got != "a\nB_theirs\nc\n" {
		t.Errorf("base/a.txt = %q", got)
	}

	// 第四跑：全 Noop。
	rep4, err := runReport(UpdateOptions{ProjectDir: proj})
	if err != nil {
		t.Fatal(err)
	}
	if codeOf(rep4.Verdict) != CodeOK || len(rep4.Written)+len(rep4.Conflicted) != 0 {
		t.Errorf("第四跑应无动作: %+v", rep4)
	}
}

func TestUpdateSkipsGeneratedWhenHooksCannotRun(t *testing.T) {
	repo, proj := initProjectT(t, map[string]string{
		"xinit.yml": genYAML,
		"gen.txt":   "g1\n",
	})
	writeFilesT(t, proj, map[string]string{"gen.txt": "g_ours\n"}) // 模拟钩子产出
	commitT(t, repo, map[string]string{"gen.txt": "g2\n"}, "v1.1.0")

	rep, err := runReport(UpdateOptions{ProjectDir: proj})
	if err != nil {
		t.Fatal(err)
	}
	if codeOf(rep.Verdict) != CodeOK {
		t.Fatalf("跳过不产生待解决项、不影响退出码（§11.5）: %+v", rep)
	}
	if len(rep.Conflicted) != 0 {
		t.Errorf("跳过不得判冲突: %v", rep.Conflicted)
	}
	if got := mustContent(t, proj, "gen.txt"); got != "g_ours\n" {
		t.Errorf("生成物不得被覆盖: %q", got)
	}
	if _, err := os.Lstat(filepath.Join(proj, ".xinit", "conflicts")); !os.IsNotExist(err) {
		t.Error("跳过不写 conflicts/")
	}
	st := mustLoadState(t, proj)
	if st.Template.Revision != "v1.1.0" {
		t.Errorf("非生成物路径照常推进版本: %q", st.Template.Revision)
	}
	if got := mustContent(t, state.BaseDir(proj), "gen.txt"); got != "g1\n" {
		t.Errorf("跳过路径的 base 保持旧值: %q", got)
	}
	if !st.Files["gen.txt"].Generated {
		t.Error("generated 位应保持旧值（true）")
	}
}

// AR 的另一半：待解决注解只属于非 dry-run——dry-run 的退出码不模拟 2
// （§11.5），「state 里的版本记录未推进」在只读预测里是失实信息。
func TestUpdateDryRunReportHasNoPendingNote(t *testing.T) {
	repo, proj := initProjectT(t, map[string]string{
		"xinit.yml": tmplYAML,
		"a.txt":     "one\n",
	})
	writeFilesT(t, proj, map[string]string{"a.txt": "one local\n"})
	commitT(t, repo, map[string]string{"a.txt": "two\n"}, "v1.1.0")

	rep, err := runReport(UpdateOptions{ProjectDir: proj, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Conflicted) == 0 {
		t.Fatalf("夹具应产出冲突（否则负断言无判别力）: %+v", rep)
	}
	var buf strings.Builder
	rep.Print(&buf)
	if strings.Contains(buf.String(), "本轮仍待解决") {
		t.Errorf("dry-run 报告不得带待解决注解:\n%s", buf.String())
	}
}

func TestUpdateDryRunChangesNothing(t *testing.T) {
	repo, proj := initProjectT(t, map[string]string{
		"xinit.yml": tmplYAML,
		"a.txt":     "one\n",
	})
	commitT(t, repo, map[string]string{"a.txt": "two\n"}, "v1.1.0")

	before := readTreeT(t, proj)
	var ee *ExitError
	err := Update(UpdateOptions{ProjectDir: proj, DryRun: true})
	if !errors.As(err, &ee) || ee.Code != CodeChanged {
		t.Fatalf("dry-run 有非 Noop 应 ExitError{5}: %v", err)
	}
	if after := readTreeT(t, proj); !reflect.DeepEqual(before, after) {
		t.Error("dry-run 不得写任何文件（含 .xinit/lock、state、base）")
	}
}

// 评审 I-1：零写契约覆盖 base 的 §5.2 恢复——git 源下 .xinit/base/ 丢失时，
// 管线的 base 装载重建只进内存快照（供 plan 计算、Degraded 照常报告），不得
// 写回磁盘。a.txt 可重建（进内存参与 plan）、b.txt 记录 hash 与旧 commit
// 重渲染必然不一致（降级）。
func TestUpdateDryRunBaseRecoveryDoesNotWrite(t *testing.T) {
	repo, proj := initProjectT(t, map[string]string{
		"xinit.yml": tmplYAML,
		"a.txt":     "one\n",
		"b.txt":     "bee\n",
	})
	// b.txt 的 recorded hash 篡改成重渲染不可能匹配的值 → §5.2 降级
	st := mustLoadState(t, proj)
	fst := st.Files["b.txt"]
	fst.Hash = fsutil.CompareHash([]byte("旧 base\n"), false)
	st.Files["b.txt"] = fst
	if err := st.Save(proj); err != nil {
		t.Fatal(err)
	}
	// base 整树丢失（git 源：记录的旧 commit 可重建）
	if err := os.RemoveAll(state.BaseDir(proj)); err != nil {
		t.Fatal(err)
	}
	commitT(t, repo, map[string]string{"a.txt": "two\n"}, "v1.1.0")

	before := readTreeT(t, proj)
	rep, err := runReport(UpdateOptions{ProjectDir: proj, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if after := readTreeT(t, proj); !reflect.DeepEqual(before, after) {
		t.Error("dry-run 不得写任何文件（含 .xinit/base/ 的恢复写回）")
	}
	// 精确条目判断（APFS 折叠教训）：.xinit/ 下不得出现 base 这一格。
	ents, err := os.ReadDir(filepath.Join(proj, ".xinit"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if e.Name() == "base" {
			t.Error("dry-run 不得重建 .xinit/base/（恢复只进内存快照）")
		}
	}
	// 重建结果照常进内存：a.txt 的 base 参与 plan（base=one、ours=one、
	// theirs=two → 写）；若恢复未进内存（base✗）会判冲突而不是写入。
	if codeOf(rep.Verdict) != CodeChanged {
		t.Errorf("codeOf(rep.Verdict) = %d，want %d", codeOf(rep.Verdict), CodeChanged)
	}
	if !slices.Contains(rep.Written, "a.txt") {
		t.Errorf("Written = %v，应含 a.txt（内存重建须照常参与 plan）", rep.Written)
	}
	// 降级照常报告。
	if !slices.Equal(rep.Degraded, []string{"b.txt"}) {
		t.Errorf("Degraded = %v，want [b.txt]", rep.Degraded)
	}
}

func TestUpdateDeletesPathAndBacksUp(t *testing.T) {
	repo, proj := initProjectT(t, map[string]string{
		"xinit.yml": tmplYAML,
		"a.txt":     "one\n",
		"gone.txt":  "bye\n",
	})
	if err := os.Remove(filepath.Join(repo, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	runGitT(t, repo, "add", "-A")
	runGitT(t, repo, "commit", "-q", "-m", "v1.1.0")
	runGitT(t, repo, "tag", "v1.1.0")

	rep, err := runReport(UpdateOptions{ProjectDir: proj})
	if err != nil {
		t.Fatal(err)
	}
	if codeOf(rep.Verdict) != CodeOK {
		t.Fatalf("codeOf(rep.Verdict) = %d: %+v", codeOf(rep.Verdict), rep)
	}
	if !slices.Equal(rep.Deleted, []string{"gone.txt"}) {
		t.Errorf("Deleted = %v", rep.Deleted)
	}
	if _, err := os.Lstat(filepath.Join(proj, "gone.txt")); !os.IsNotExist(err) {
		t.Error("模板删除的路径应从磁盘移除")
	}
	if rep.BackupDir == "" {
		t.Fatal("删除也记入备份（§10.1）")
	}
	if got := mustContent(t, proj, rep.BackupDir+"/gone.txt"); got != "bye\n" {
		t.Errorf("备份内容 = %q", got)
	}
	if _, err := os.Lstat(filepath.Join(state.BaseDir(proj), "gone.txt")); !os.IsNotExist(err) {
		t.Error("base 残片应删除（§10.2 第 9 步）")
	}
	st := mustLoadState(t, proj)
	if _, ok := st.Files["gone.txt"]; ok {
		t.Error("files 里应移除（NewBase = nil）")
	}
}

// §8.5 折叠拒绝行 + restoreRefused：被拒路径的 NewBase 必须还原为
// 「本轮开始时的 base 条目」（base✗ → nil）——restoreRefused 必须在第 9 步
// （advanceState）之前执行，否则被拒路径的 base/state 会被错误推进。
func TestUpdateRefusedPathKeepsOldBase(t *testing.T) {
	repo, proj := initProjectT(t, map[string]string{
		"xinit.yml": tmplYAML,
		"a.txt":     "one\n",
	})
	// 用户放了一个未跟踪的常规文件占住 new/ 这一格（§9.3 障碍物：
	// 模板要 new 是目录、磁盘上它是文件）。
	writeFilesT(t, proj, map[string]string{"new": "obstacle\n"})
	commitT(t, repo, map[string]string{
		"a.txt":     "two\n",
		"new/x.txt": "x\n",
	}, "v1.1.0")

	rep, err := runReport(UpdateOptions{ProjectDir: proj})
	if err != nil {
		t.Fatal(err)
	}
	if codeOf(rep.Verdict) != CodePending {
		t.Fatalf("被拒路径应退出 2（§11.5）: %+v", rep)
	}
	if len(rep.Refused) != 1 || rep.Refused[0].Path != "new/x.txt" {
		t.Fatalf("Refused = %+v，want [new/x.txt]", rep.Refused)
	}
	// 被拒不中止其余路径（§9.3）：a.txt 照常写入。
	if !slices.Equal(rep.Written, []string{"a.txt"}) {
		t.Errorf("Written = %v，want [a.txt]", rep.Written)
	}
	if got := mustContent(t, proj, "a.txt"); got != "two\n" {
		t.Errorf("a.txt = %q", got)
	}
	if got := mustContent(t, proj, "new"); got != "obstacle\n" {
		t.Errorf("障碍物不得被改写: %q", got)
	}
	st := mustLoadState(t, proj)
	if _, ok := st.Files["new/x.txt"]; ok {
		t.Error("被拒路径不得进 state.files（NewBase 还原为 base✗ → nil）")
	}
	if st.Template.Revision != "v1.0.0" {
		t.Errorf("有待解决项时不得推进模板版本（与冲突同语义）: %q", st.Template.Revision)
	}
	// 精确条目名判断（APFS 折叠教训）：base/ 里不得出现 new 这一格。
	ents, err := os.ReadDir(state.BaseDir(proj))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if e.Name() == "new" {
			t.Error("被拒路径不得写进 .xinit/base/（base 未被推进）")
		}
	}
}

// §10.2 第 4 步的「不写任何文件」在**模板又动了**时同样可观察：被拒的
// 运行不得刷新 conflicts/——同目标重跑时刷新是「同字节重写」，内容级断言
// 会失明；模板再前进一版就让该写盘现形。
func TestUpdateStep4BlockedDoesNotRefreshConflicts(t *testing.T) {
	repo, proj := initProjectT(t, map[string]string{
		"xinit.yml": tmplYAML,
		"a.txt":     "a\nb\nc\n",
	})
	writeFilesT(t, proj, map[string]string{"a.txt": "a\nB_ours\nc\n"})
	commitT(t, repo, map[string]string{"a.txt": "a\nB_theirs\nc\n"}, "v1.1.0")
	var ee *ExitError
	if err := Update(UpdateOptions{ProjectDir: proj}); !errors.As(err, &ee) || ee.Code != CodePending {
		t.Fatalf("首次冲突应 ExitError{2}: %v", err)
	}
	first := mustContent(t, proj, ".xinit/conflicts/a.txt")

	// 模板再次前进（v1.1.1）：冲突仍在 → 第 4 步拒绝；磁盘（含 conflicts/）
	// 一个字节都不写。
	commitT(t, repo, map[string]string{"a.txt": "a\nB_theirs2\nc\n"}, "v1.1.1")
	before := readTreeT(t, proj)
	err := Update(UpdateOptions{ProjectDir: proj})
	if !errors.As(err, &ee) || ee.Code != CodePending {
		t.Fatalf("未解决时模板再前进应仍 ExitError{2}: %v", err)
	}
	if after := readTreeT(t, proj); !reflect.DeepEqual(before, after) {
		t.Error("第 4 步拒绝不得写任何文件")
	}
	if got := mustContent(t, proj, ".xinit/conflicts/a.txt"); got != first {
		t.Errorf("被拒的运行不得刷新 conflicts/：%q → %q", first, got)
	}
}

// 评审 E1：ConflictKind 且 theirs 缺失（conflicts/ 无内容条目）也必须计入
// 待解决——退出 2、报告有冲突段且不打印「参考：」行（无条目时不实）、
// revision/commit 不推进。旧实现 pending 只算已写盘条目 → 退 0、版本照推，
// 与同一计划的 diff（冲突 1 / 退 5）自相矛盾。
func TestUpdateConflictKindWithoutTheirsFileStillPending(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("符号链接需要 Windows 特权；与 apply 包 TestDanglingSymlinkRefusedNotAborted 同款题材")
	}
	repo, proj := initProjectT(t, map[string]string{
		"xinit.yml": tmplYAML,
		"a.txt":     "one\n",
	})
	oldCommit := mustLoadState(t, proj).Template.Commit

	// §8.1 K1 形状：用户把受管文件换成悬空符号链接，模板同轮删除该路径。
	if err := os.Remove(filepath.Join(proj, "a.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("nowhere", filepath.Join(proj, "a.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(repo, "a.txt")); err != nil {
		t.Fatal(err)
	}
	runGitT(t, repo, "add", "-A")
	runGitT(t, repo, "commit", "-q", "-m", "v1.1.0")
	runGitT(t, repo, "tag", "v1.1.0")

	// 捕获报告输出：ExitError 语义与打印内容同一次运行内断言。
	var uerr error
	report := captureStdoutT(t, func() { uerr = Update(UpdateOptions{ProjectDir: proj}) })

	var ee *ExitError
	if !errors.As(uerr, &ee) || ee.Code != CodePending {
		t.Fatalf("ConflictKind（无 conflicts/ 条目）应待解决退 2: %v\n%s", uerr, report)
	}
	if !strings.Contains(report, "冲突（1 条）需要你处理：") {
		t.Errorf("报告应有冲突段:\n%s", report)
	}
	// AR（待解决轮的版本行诚实表述）：版本行照打（预测的目标版本），
	// 但必须紧跟一行说明 state 里的版本记录本轮没有推进。
	if !strings.Contains(report, "版本: v1.0.0 → v1.1.0") {
		t.Errorf("报告应有版本行:\n%s", report)
	}
	if !strings.Contains(report, "（本轮仍待解决：state 里的版本记录保持 v1.0.0，收敛后重跑再推进；§10.2 第 9 步）") {
		t.Errorf("待解决轮的版本行须说明记录未推进（AR）:\n%s", report)
	}
	if !strings.Contains(report, "汇总: 写入 0、删除 0、chmod 0、冲突 1") {
		t.Errorf("汇总的冲突数应为全集（1）:\n%s", report)
	}
	if strings.Contains(report, "参考：") {
		t.Errorf("conflicts/ 无条目时不得打印「参考：」行（不实信息）:\n%s", report)
	}

	st := mustLoadState(t, proj)
	if st.Template.Revision != "v1.0.0" {
		t.Errorf("有待解决项时不得推进模板版本: %q", st.Template.Revision)
	}
	if st.Template.Commit != oldCommit {
		t.Errorf("有待解决项时不得推进 commit: %q → %q", oldCommit, st.Template.Commit)
	}
	// 精确条目判断（APFS 折叠教训）：.xinit/ 下不得出现 conflicts 这一格。
	ents, err := os.ReadDir(filepath.Join(proj, ".xinit"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if e.Name() == "conflicts" {
			t.Error("无内容条目不得创建 .xinit/conflicts/")
		}
	}
	// ours 的悬空链接原样保留；base 停在旧值。
	fi, err := os.Lstat(filepath.Join(proj, "a.txt"))
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("ours 的符号链接不得被改写（mode=%v, err=%v）", fi.Mode(), err)
	}
	if got := mustContent(t, state.BaseDir(proj), "a.txt"); got != "one\n" {
		t.Errorf("base 停在旧值: %q", got)
	}
}

// AR（第 4 步早退轮）：conflicts/ 就位后未解决的早退轮同样是「版本记录未
// 推进」的轮次——报告在 Blocked 节返回之前已打过版本行，必须跟注解。
// （update_conflict_roundtrip / accept_flow 都是 dir 源、根本不打版本行，
// 钉不住这一行，所以落在本测试。）
func TestUpdateBlockedRoundVersionLineMarksPending(t *testing.T) {
	repo, proj := initProjectT(t, map[string]string{
		"xinit.yml": tmplYAML,
		"a.txt":     "one\n",
	})
	writeFilesT(t, proj, map[string]string{"a.txt": "one local\n"})
	commitT(t, repo, map[string]string{"a.txt": "two\n"}, "v1.1.0")

	// 第一跑：新冲突 → conflicts/ 就位（exit 2）
	var uerr error
	captureStdoutT(t, func() { uerr = Update(UpdateOptions{ProjectDir: proj}) })
	var ee *ExitError
	if !errors.As(uerr, &ee) || ee.Code != CodePending {
		t.Fatalf("新冲突应退 2: %v", uerr)
	}
	if _, err := os.Lstat(filepath.Join(proj, ".xinit", "conflicts", "a.txt")); err != nil {
		t.Fatalf("conflicts/a.txt 应就位: %v", err)
	}

	// 第二跑：第 4 步早退（未解决）——版本行与注解必须同时出现
	var uerr2 error
	report := captureStdoutT(t, func() { uerr2 = Update(UpdateOptions{ProjectDir: proj}) })
	if !errors.As(uerr2, &ee) || ee.Code != CodePending {
		t.Fatalf("未解决重跑应退 2: %v", uerr2)
	}
	if !strings.Contains(report, "未解决的冲突") {
		t.Fatalf("早退轮应有 Blocked 节:\n%s", report)
	}
	if !strings.Contains(report, "版本: v1.0.0 → v1.1.0") {
		t.Errorf("早退轮应有版本行（如实说明记录未推进）:\n%s", report)
	}
	if !strings.Contains(report, "（本轮仍待解决：state 里的版本记录保持 v1.0.0，收敛后重跑再推进；§10.2 第 9 步）") {
		t.Errorf("早退轮的版本行须跟注解（AR）:\n%s", report)
	}
}
