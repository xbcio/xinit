package backflow

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// okMsg 是合格的回流提交消息（末段 trailer 块含逐字行 `Template-Upstream: yes`）。
const okMsg = "feat: 改共享面\n\nTemplate-Upstream: yes\n"

// runGitT 在 dir 下跑一条 git 命令（固定作者身份，避免依赖用户全局配置）。
// 沿用 internal/cli/restore_test.go 的 runGitT 模式（该 helper 在 package cli，不可跨包复用）。
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

// writeFileT 写一个文件（建父目录）。
func writeFileT(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// mkRepoT 建一个真 git 仓（首个提交为 root 提交），返回仓目录。
func mkRepoT(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	runGitT(t, dir, "init", "-q", "-b", "main")
	for rel, content := range files {
		writeFileT(t, dir, rel, content)
	}
	runGitT(t, dir, "add", "-A")
	runGitT(t, dir, "commit", "-q", "-m", "init")
	return dir
}

// commitAllT 暂存全部改动并提交（消息清理走 git 默认 strip），返回新提交 sha。
func commitAllT(t *testing.T, dir, msg string) string {
	t.Helper()
	runGitT(t, dir, "add", "-A")
	runGitT(t, dir, "commit", "-q", "-m", msg)
	return runGitT(t, dir, "rev-parse", "HEAD")
}

// commitMsgT 用消息文件提交（--cleanup=verbatim：保留 CRLF 与行尾空白等原始字节），
// 返回新提交 sha。默认 strip 会吃掉这些字节，CRLF/trailer 变体夹具必须走这条。
func commitMsgT(t *testing.T, dir, msg string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "msg")
	if err := os.WriteFile(f, []byte(msg), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitT(t, dir, "add", "-A")
	runGitT(t, dir, "commit", "-q", "--cleanup=verbatim", "-F", f)
	return runGitT(t, dir, "rev-parse", "HEAD")
}
