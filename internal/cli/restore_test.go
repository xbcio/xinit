package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbcio/xinit/internal/state"
)

// runGitT 在指定目录跑一条 git 命令（固定作者身份，避免依赖用户全局配置）。
// 包内通用 helper，Task 12 的夹具继续复用。
func runGitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// writeFilesT 往目录里写一批文件（建父目录）。
func writeFilesT(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// mkTemplateRepoT 建一个真 git 模板仓（工作副本当模板源），返回 (source, commit)。
func mkTemplateRepoT(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	runGitT(t, dir, "init", "-q", "-b", "main")
	writeFilesT(t, dir, files)
	runGitT(t, dir, "add", "-A")
	runGitT(t, dir, "commit", "-q", "-m", "init")
	return dir, runGitT(t, dir, "rev-parse", "HEAD")
}

// 模板：xinit.yml 清单（无 vars）+ a.txt。
const tmplYAML = "manifest_version: 1\n"

func projectWithState(t *testing.T, tmpl state.TemplateRef, files map[string]state.FileState) string {
	t.Helper()
	dir := t.TempDir()
	st := state.New("test")
	st.Template = tmpl
	st.Files = files
	if err := st.Save(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

func mustLoadState(t *testing.T, dir string) *state.State {
	t.Helper()
	st, err := state.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return st
}
