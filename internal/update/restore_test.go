package update

// 来源：internal/cli/restore_test.go 行 70-318（§4.3：9 个 TestLoadBase* 随迁，
// I-2 实施批 T2/M1）——夹具段留 cli，本包用 helpers_test.go 的自足副本。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbcio/xinit/internal/fsutil"
	"github.com/xbcio/xinit/internal/state"
)

func TestLoadBaseRebuildsMissingPaths(t *testing.T) {
	repo, commit := mkTemplateRepoT(t, map[string]string{
		"xinit.yml": tmplYAML,
		"a.txt":     "hello\n",
		"b.txt":     "world\n",
	})
	content := []byte("hello\n")
	dir := projectWithState(t, state.TemplateRef{Source: repo, Kind: "git", Revision: "main", Commit: commit},
		map[string]state.FileState{
			"a.txt": {Src: "a.txt", Hash: fsutil.CompareHash(content, false)},
			"b.txt": {Src: "b.txt", Hash: fsutil.CompareHash([]byte("world\n"), false)},
		})
	// base 单文件缺失：b.txt 在、a.txt 丢（整树缺失走同一流程）
	if err := os.MkdirAll(filepath.Join(state.BaseDir(dir)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state.BaseDir(dir), "b.txt"), []byte("world\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	snap, err := loadBase(dir, mustLoadState(t, dir), t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Degraded) != 0 {
		t.Fatalf("Degraded = %v", snap.Degraded)
	}
	if got := string(snap.Bytes["a.txt"]); got != "hello\n" {
		t.Errorf("a.txt 未重建：%q", got)
	}
	if got := string(snap.Bytes["b.txt"]); got != "world\n" {
		t.Errorf("b.txt 应走健康路径：%q", got)
	}
	// 重建必须写回磁盘（下一轮就此健康）
	back, err := os.ReadFile(filepath.Join(state.BaseDir(dir), "a.txt"))
	if err != nil || string(back) != "hello\n" {
		t.Errorf("base/a.txt 未写回：%q, %v", back, err)
	}
}

// 健康路径不得触碰模板源：把 Template 设成 dir 且 Source 不存在也能跑通。
func TestLoadBaseHealthyDoesNotNeedTemplate(t *testing.T) {
	dir := projectWithState(t, state.TemplateRef{Source: "/nonexistent", Kind: "dir"},
		map[string]state.FileState{"a.txt": {Src: "a.txt", Hash: fsutil.CompareHash([]byte("hello\n"), false)}})
	if err := os.MkdirAll(state.BaseDir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state.BaseDir(dir), "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	snap, err := loadBase(dir, mustLoadState(t, dir), t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snap.Metas["a.txt"]; !ok {
		t.Error("健康路径应进 Metas")
	}
}

// §5.2 的「为什么必须逐路径」：hash 记的是历史某轮「保持旧值」的 base，
// 重渲染旧 commit 必然不一致 → 降级，不是报错。
func TestLoadBaseDegradesOnHashMismatch(t *testing.T) {
	repo, commit := mkTemplateRepoT(t, map[string]string{
		"xinit.yml": tmplYAML,
		"a.txt":     "hello\n",
	})
	dir := projectWithState(t, state.TemplateRef{Source: repo, Kind: "git", Revision: "main", Commit: commit},
		map[string]state.FileState{
			"a.txt": {Src: "a.txt", Hash: fsutil.CompareHash([]byte("旧 base\n"), false)},
		})

	snap, err := loadBase(dir, mustLoadState(t, dir), t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Degraded) != 1 || snap.Degraded[0] != "a.txt" {
		t.Fatalf("Degraded = %v", snap.Degraded)
	}
	if _, ok := snap.Metas["a.txt"]; ok {
		t.Error("降级路径不得进 Metas（本轮按 base✗ 判定）")
	}
	if _, err := os.Lstat(filepath.Join(state.BaseDir(dir), "a.txt")); !os.IsNotExist(err) {
		t.Error("降级路径不得写回 base")
	}
}

func TestLoadBaseDirSourceErrorsWithAdoptHint(t *testing.T) {
	dir := projectWithState(t, state.TemplateRef{Source: "/nonexistent", Kind: "dir"},
		map[string]state.FileState{"a.txt": {Src: "a.txt", Hash: "sha256/normlf-v1:deadbeef"}})

	_, err := loadBase(dir, mustLoadState(t, dir), t.TempDir(), false)
	if err == nil || !strings.Contains(err.Error(), "adopt") {
		t.Fatalf("dir 源无法重建应报错并指引 adopt：%v", err)
	}
}

func TestLoadBaseCommitGoneErrorsWithAdoptHint(t *testing.T) {
	repo, _ := mkTemplateRepoT(t, map[string]string{
		"xinit.yml": tmplYAML,
		"a.txt":     "hello\n",
	})
	dir := projectWithState(t, state.TemplateRef{Source: repo, Kind: "git", Revision: "main", Commit: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"},
		map[string]state.FileState{"a.txt": {Src: "a.txt", Hash: "sha256/normlf-v1:deadbeef"}})

	_, err := loadBase(dir, mustLoadState(t, dir), t.TempDir(), false)
	if err == nil || !strings.Contains(err.Error(), "adopt") {
		t.Fatalf("旧 commit 不可取应报错并指引 adopt：%v", err)
	}
}

// R10：base 文件在、但字节与 files[P].hash 不符时同样走 §5.2 恢复
// ——「全量校验」的意义就是不信任盘上现成的字节。
func TestLoadBaseRebuildsCorruptPresentPath(t *testing.T) {
	repo, commit := mkTemplateRepoT(t, map[string]string{
		"xinit.yml": tmplYAML,
		"a.txt":     "hello\n",
	})
	dir := projectWithState(t, state.TemplateRef{Source: repo, Kind: "git", Revision: "main", Commit: commit},
		map[string]state.FileState{
			"a.txt": {Src: "a.txt", Hash: fsutil.CompareHash([]byte("hello\n"), false)},
		})
	if err := os.MkdirAll(state.BaseDir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state.BaseDir(dir), "a.txt"), []byte("HELLO\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	snap, err := loadBase(dir, mustLoadState(t, dir), t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Degraded) != 0 {
		t.Fatalf("Degraded = %v", snap.Degraded)
	}
	if got := string(snap.Bytes["a.txt"]); got != "hello\n" {
		t.Errorf("损坏的 base 未从模板重建：%q", got)
	}
	back, err := os.ReadFile(filepath.Join(state.BaseDir(dir), "a.txt"))
	if err != nil || string(back) != "hello\n" {
		t.Errorf("base/a.txt 未写回：%q, %v", back, err)
	}
}

// 重建的写回必须保留 exec 位（§10.1 mode）：state 记 Exec 的文件写 0755。
func TestLoadBaseRebuildExecMode(t *testing.T) {
	repo, _ := mkTemplateRepoT(t, map[string]string{
		"xinit.yml": tmplYAML,
		"run.sh":    "#!/bin/sh\necho hi\n",
	})
	if err := os.Chmod(filepath.Join(repo, "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	runGitT(t, repo, "add", "-A")
	runGitT(t, repo, "commit", "-q", "-m", "chmod +x")
	commit := runGitT(t, repo, "rev-parse", "HEAD")

	dir := projectWithState(t, state.TemplateRef{Source: repo, Kind: "git", Revision: "main", Commit: commit},
		map[string]state.FileState{
			"run.sh": {Src: "run.sh", Hash: fsutil.CompareHash([]byte("#!/bin/sh\necho hi\n"), false), Exec: true},
		})

	snap, err := loadBase(dir, mustLoadState(t, dir), t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Metas["run.sh"].Exec {
		t.Error("Metas[run.sh].Exec 应为 true（state 权威记录）")
	}
	info, err := os.Stat(filepath.Join(state.BaseDir(dir), "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("重建的 base/run.sh 权限 = %v，应为 0755", info.Mode().Perm())
	}
}

// R9：重渲染用 state.Vars 原样，不重跑 EvalVars。
// state.vars 里留着的「清单已删除的变量」不能让重建报错。
func TestLoadBaseUsesFrozenVarsWithoutEvalVars(t *testing.T) {
	repo, commit := mkTemplateRepoT(t, map[string]string{
		"xinit.yml": tmplYAML,
		"a.txt":     "hello\n",
	})
	dir := projectWithState(t, state.TemplateRef{Source: repo, Kind: "git", Revision: "main", Commit: commit},
		map[string]state.FileState{
			"a.txt": {Src: "a.txt", Hash: fsutil.CompareHash([]byte("hello\n"), false)},
		})
	st := mustLoadState(t, dir)
	st.Vars = map[string]string{"gone": "x"} // 旧模板声明过、现已删除
	if err := st.Save(dir); err != nil {
		t.Fatal(err)
	}

	snap, err := loadBase(dir, st, t.TempDir(), false)
	if err != nil {
		t.Fatalf("R9：重渲染不得重跑 EvalVars（已删除的变量还在 state.vars 里）：%v", err)
	}
	if got := string(snap.Bytes["a.txt"]); got != "hello\n" {
		t.Errorf("a.txt 未重建：%q", got)
	}
}

// AC-M1：§5.2 重建的三处硬错误（重渲染路径/重渲染内容/写回 base）也必须
// 指引 adopt——与文件内既有 5 处保持同一形状（T8 裁定）。
func TestLoadBaseRebuildErrorsHintAdopt(t *testing.T) {
	t.Run("重渲染路径失败", func(t *testing.T) {
		repo, commit := mkTemplateRepoT(t, map[string]string{
			"xinit.yml": tmplYAML + "rename:\n  - from: \"a.txt\"\n    to: \"{{ .no_such_var }}.txt\"\n",
			"a.txt":     "one\n",
		})
		dir := projectWithState(t, state.TemplateRef{Source: repo, Kind: "git", Revision: "main", Commit: commit},
			map[string]state.FileState{"a.txt": {Src: "a.txt", Hash: "sha256/normlf-v1:deadbeef"}})
		_, err := loadBase(dir, mustLoadState(t, dir), t.TempDir(), false)
		if err == nil || !strings.Contains(err.Error(), "重建 base") || !strings.Contains(err.Error(), "adopt") {
			t.Fatalf("重渲染路径失败应报错并指引 adopt（AC-M1）：%v", err)
		}
	})

	t.Run("重渲染内容失败", func(t *testing.T) {
		repo, commit := mkTemplateRepoT(t, map[string]string{
			"xinit.yml": tmplYAML + "substitute:\n  - from: \"one\"\n    to: \"{{ .no_such_var }}\"\n    paths: [\"**/*.txt\"]\n",
			"a.txt":     "one\n",
		})
		dir := projectWithState(t, state.TemplateRef{Source: repo, Kind: "git", Revision: "main", Commit: commit},
			map[string]state.FileState{"a.txt": {Src: "a.txt", Hash: "sha256/normlf-v1:deadbeef"}})
		_, err := loadBase(dir, mustLoadState(t, dir), t.TempDir(), false)
		if err == nil || !strings.Contains(err.Error(), "重建 base/a.txt") || !strings.Contains(err.Error(), "adopt") {
			t.Fatalf("重渲染内容失败应报错并指引 adopt（AC-M1）：%v", err)
		}
	})

	t.Run("写回 base 失败", func(t *testing.T) {
		repo, commit := mkTemplateRepoT(t, map[string]string{"xinit.yml": tmplYAML, "a.txt": "one\n"})
		dir := projectWithState(t, state.TemplateRef{Source: repo, Kind: "git", Revision: "main", Commit: commit},
			map[string]state.FileState{"a.txt": {Src: "a.txt", Hash: fsutil.CompareHash([]byte("one\n"), false)}})
		// .xinit/base 是常规文件（损坏形态）：读盘看到「整树缺失」，
		// 重建的落盘必然失败（MkdirAll 撞 ENOTDIR）。
		if err := os.WriteFile(state.BaseDir(dir), []byte("not a dir"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := loadBase(dir, mustLoadState(t, dir), t.TempDir(), false)
		if err == nil || !strings.Contains(err.Error(), "重建 base/a.txt") || !strings.Contains(err.Error(), "adopt") {
			t.Fatalf("写回失败应报错并指引 adopt（AC-M1）：%v", err)
		}
	})
}
