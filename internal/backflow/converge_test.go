package backflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbcio/xinit/internal/fsutil"
	"github.com/xbcio/xinit/internal/state"
)

// ---------------- 夹具 ----------------

// convergeTmplYAML 是最小清单（无 vars/subst），其渲染产物 = 模板原文。
const convergeTmplYAML = "manifest_version: 1\n"

// mkConvergeRepoT 建真 git 模板仓（工作副本当模板源），返回 (仓目录, 记录 commit)。
// 沿用 internal/cli/restore_test.go 的 mkTemplateRepoT 模式（跨包不可复用）。
func mkConvergeRepoT(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	dir := mkRepoT(t, files)
	return dir, runGitT(t, dir, "rev-parse", "HEAD")
}

// mkConvergedProjectT 造一个「与记录 commit 同源」的派生项目：
// .xinit/state.json（Files[Dst] 的 Hash = 模板原文的 ContentHash）+ .xinit/base/<Dst>
// 落盘。传入的 files 同时充当两侧内容（minimal 清单下渲染即原文）。
func mkConvergedProjectT(t *testing.T, repo, commit string, files map[string]string) (string, *state.State) {
	t.Helper()
	dir := t.TempDir()
	st := state.New("test")
	st.Template = state.TemplateRef{Source: repo, Kind: "git", Revision: "main", Commit: commit}
	for dst, content := range files {
		b := []byte(content)
		st.Files[dst] = state.FileState{Src: dst, Hash: fsutil.ContentHash(b), Binary: fsutil.IsBinary(b)}
		writeFileT(t, filepath.Join(dir, state.DirName, "base"), dst, content)
	}
	if err := st.Save(dir); err != nil {
		t.Fatalf("Save state: %v", err)
	}
	return dir, st
}

// basePathT 返回 base 里某个 Dst 的磁盘路径。
func basePathT(proj, dst string) string {
	return filepath.Join(proj, state.DirName, "base", filepath.FromSlash(dst))
}

// ---------------- CheckConverged ----------------

func TestCheckConvergedCleanStateIsEmpty(t *testing.T) {
	repo, commit := mkConvergeRepoT(t, map[string]string{
		"xinit.yml": convergeTmplYAML,
		"a.txt":     "hello\n",
		"dir/b.txt": "world\n",
	})
	proj, st := mkConvergedProjectT(t, repo, commit, map[string]string{
		"a.txt":     "hello\n",
		"dir/b.txt": "world\n",
	})
	res, err := CheckConverged(proj, st, t.TempDir(), map[string]string{
		"a.txt":     "a.txt",
		"dir/b.txt": "dir/b.txt",
	})
	if err != nil {
		t.Fatalf("CheckConverged: %v", err)
	}
	if len(res.Unconverged) != 0 || len(res.SourceGone) != 0 {
		t.Fatalf("一致态应零告警，got %+v", res)
	}
}

func TestCheckConvergedTamperedBaseIsUnconverged(t *testing.T) {
	// 可选加固：base 落盘字节与 state 记录不符（手工篡改 base 让 B 失真）
	// ⇒ 成因①（整体拒绝，出口「先跑 xinit update 收敛」）。
	repo, commit := mkConvergeRepoT(t, map[string]string{
		"xinit.yml": convergeTmplYAML,
		"a.txt":     "hello\n",
		"b.txt":     "world\n",
	})
	proj, st := mkConvergedProjectT(t, repo, commit, map[string]string{
		"a.txt": "hello\n",
		"b.txt": "world\n",
	})
	writeFileT(t, filepath.Join(proj, state.DirName, "base"), "a.txt", "tampered\n")

	res, err := CheckConverged(proj, st, t.TempDir(), map[string]string{"a.txt": "a.txt", "b.txt": "b.txt"})
	if err != nil {
		t.Fatalf("CheckConverged: %v", err)
	}
	if len(res.Unconverged) != 1 || res.Unconverged[0] != "a.txt" {
		t.Errorf("Unconverged = %v, want [a.txt]", res.Unconverged)
	}
	if len(res.SourceGone) != 0 {
		t.Errorf("SourceGone = %v, want 空", res.SourceGone)
	}
}

func TestCheckConvergedPendingHashMismatch(t *testing.T) {
	// pending 混合态：state 记的 Hash/base 是「上一轮」渲染值，而记录 commit
	// 重渲染得到新值（B 与 T 不同源）⇒ 成因①。
	repo, commit := mkConvergeRepoT(t, map[string]string{
		"xinit.yml": convergeTmplYAML,
		"a.txt":     "hello\n",
	})
	proj, st := mkConvergedProjectT(t, repo, commit, map[string]string{"a.txt": "old\n"})

	res, err := CheckConverged(proj, st, t.TempDir(), map[string]string{"a.txt": "a.txt"})
	if err != nil {
		t.Fatalf("CheckConverged: %v", err)
	}
	if len(res.Unconverged) != 1 || res.Unconverged[0] != "a.txt" {
		t.Errorf("重渲染 hash 与记录不符 ⇒ 成因①，got %+v", res)
	}
}

func TestCheckConvergedMissingBaseFileIsUnconverged(t *testing.T) {
	// base 落盘缺失：不能比对（且 update 会重建它）⇒ 与成因① 同出口 fail-closed。
	repo, commit := mkConvergeRepoT(t, map[string]string{
		"xinit.yml": convergeTmplYAML,
		"a.txt":     "hello\n",
	})
	proj, st := mkConvergedProjectT(t, repo, commit, map[string]string{"a.txt": "hello\n"})
	if err := os.Remove(basePathT(proj, "a.txt")); err != nil {
		t.Fatal(err)
	}

	res, err := CheckConverged(proj, st, t.TempDir(), map[string]string{"a.txt": "a.txt"})
	if err != nil {
		t.Fatalf("CheckConverged: %v", err)
	}
	if len(res.Unconverged) != 1 || res.Unconverged[0] != "a.txt" {
		t.Errorf("base 缺失 ⇒ 成因①，got %+v", res)
	}
}

func TestCheckConvergedByDstMissingIsSourceGone(t *testing.T) {
	// 模板已移除/改名：记录 commit 的重渲染产不出该 Dst（KeepWarned 类，
	// update 永远收敛不了）⇒ 成因②，该路径警告不导出。
	repo, commit := mkConvergeRepoT(t, map[string]string{
		"xinit.yml": convergeTmplYAML,
		"a.txt":     "hello\n",
	})
	proj, st := mkConvergedProjectT(t, repo, commit, map[string]string{
		"a.txt":    "hello\n",
		"gone.txt": "old content\n",
	})

	res, err := CheckConverged(proj, st, t.TempDir(), map[string]string{"a.txt": "a.txt", "gone.txt": "gone.txt"})
	if err != nil {
		t.Fatalf("CheckConverged: %v", err)
	}
	if len(res.Unconverged) != 0 {
		t.Errorf("byDst 缺失是成因②（不整体拒绝），got Unconverged=%v", res.Unconverged)
	}
	if len(res.SourceGone) != 1 || res.SourceGone[0] != "gone.txt" {
		t.Errorf("SourceGone = %v, want [gone.txt]", res.SourceGone)
	}
}

func TestCheckConvergedNewFileExempt(t *testing.T) {
	// ⑦ 新增文件豁免：无 state 条目（无 B）则无同源问题，收敛校验跳过它。
	repo, commit := mkConvergeRepoT(t, map[string]string{
		"xinit.yml": convergeTmplYAML,
		"a.txt":     "hello\n",
	})
	proj, st := mkConvergedProjectT(t, repo, commit, map[string]string{"a.txt": "hello\n"})

	res, err := CheckConverged(proj, st, t.TempDir(), map[string]string{
		"a.txt":         "a.txt",
		"brand-new.txt": "brand-new.txt",
	})
	if err != nil {
		t.Fatalf("CheckConverged: %v", err)
	}
	if len(res.Unconverged) != 0 || len(res.SourceGone) != 0 {
		t.Errorf("新增文件不得被收敛校验拦截，got %+v", res)
	}
}

func TestCheckConvergedOutputIsSorted(t *testing.T) {
	repo, commit := mkConvergeRepoT(t, map[string]string{
		"xinit.yml": convergeTmplYAML,
		"a.txt":     "a\n",
		"m.txt":     "m\n",
		"z.txt":     "z\n",
	})
	proj, st := mkConvergedProjectT(t, repo, commit, map[string]string{
		"a.txt": "a\n",
		"m.txt": "m\n",
		"z.txt": "z\n",
	})
	writeFileT(t, filepath.Join(proj, state.DirName, "base"), "z.txt", "zz\n")
	writeFileT(t, filepath.Join(proj, state.DirName, "base"), "a.txt", "aa\n")
	writeFileT(t, filepath.Join(proj, state.DirName, "base"), "m.txt", "mm\n")

	// srcs 是 map（随机序）——输出必须按字节序，两次运行同结果。
	res, err := CheckConverged(proj, st, t.TempDir(), map[string]string{
		"z.txt": "z.txt", "m.txt": "m.txt", "a.txt": "a.txt",
	})
	if err != nil {
		t.Fatalf("CheckConverged: %v", err)
	}
	want := []string{"a.txt", "m.txt", "z.txt"}
	if len(res.Unconverged) != 3 ||
		res.Unconverged[0] != want[0] || res.Unconverged[1] != want[1] || res.Unconverged[2] != want[2] {
		t.Errorf("Unconverged = %v, want %v（确定性排序）", res.Unconverged, want)
	}
}

func TestCheckConvergedRejectsDirSource(t *testing.T) {
	// §20.7 表：dir 源拒绝 —— 没有 git blob 对象可作补丁 pre-image，且目录
	// 无版本锚（Commit 字段是树 hash）。
	repo, commit := mkConvergeRepoT(t, map[string]string{"xinit.yml": convergeTmplYAML})
	st := state.New("test")
	st.Template = state.TemplateRef{Source: repo, Kind: "dir", Commit: commit}

	_, err := CheckConverged(t.TempDir(), st, t.TempDir(), nil)
	if err == nil {
		t.Fatal("dir 源应被拒绝")
	}
	for _, want := range []string{"git", "pre-image", "版本锚"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("拒绝文案缺 %q: %v", want, err)
		}
	}
}

func TestCheckConvergedRejectsMissingRecordedCommit(t *testing.T) {
	// §20.7 表：git 源但记录 commit 不可解析（极旧 state / 镜像缺失）⇒ 拒绝
	// （「无 T 则无翻译基线」，语义同 ErrMirrorMissing 系）。
	repo, _ := mkConvergeRepoT(t, map[string]string{"xinit.yml": convergeTmplYAML})
	st := state.New("test")
	st.Template = state.TemplateRef{Source: repo, Kind: "git", Revision: "main", Commit: strings.Repeat("d", 40)}

	_, err := CheckConverged(t.TempDir(), st, t.TempDir(), nil)
	if err == nil {
		t.Fatal("记录 commit 不可解析应被拒绝")
	}
	if !strings.Contains(err.Error(), "无 T 则无翻译基线") {
		t.Errorf("拒绝文案应点名「无 T 则无翻译基线」: %v", err)
	}
}

func TestCheckConvergedRejectsMissingManifest(t *testing.T) {
	// 记录 commit 的清单缺失/解析失败 ⇒ 报错，不静默变 vacuous（§20.5 句 3）。
	repo, commit := mkConvergeRepoT(t, map[string]string{"a.txt": "hello\n"})
	proj, st := mkConvergedProjectT(t, repo, commit, map[string]string{"a.txt": "hello\n"})

	_, err := CheckConverged(proj, st, t.TempDir(), map[string]string{"a.txt": "a.txt"})
	if err == nil {
		t.Fatal("记录 commit 缺 xinit.yml 应报错")
	}
	if !strings.Contains(err.Error(), "xinit.yml") {
		t.Errorf("错误信息应点名 xinit.yml: %v", err)
	}
}
