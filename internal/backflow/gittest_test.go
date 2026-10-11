package backflow

import (
	"bytes"
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

// hashObjectT 把一段内容写进对象库（`git hash-object -w --stdin`），返回对象 sha。
func hashObjectT(t *testing.T, dir, kind, content string) string {
	t.Helper()
	cmd := exec.Command("git", "hash-object", "-t", kind, "-w", "--stdin")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
	cmd.Stdin = strings.NewReader(content)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		// 失败带 stderr（调试可读性）。不用 CombinedOutput：stdout 是机器解析的
		// sha，混入 stderr 会让成功路径也解析出脏值。
		t.Fatalf("git hash-object -t %s: %v\n%s", kind, err, stderr.String())
	}
	return strings.TrimSpace(string(out))
}

// craftSignedCommitT 手工造一个带 `gpgsig` 头的提交对象（tree/parent 取自当前 HEAD、
// 消息为 msg），把 HEAD 指过去——tree 不变，故工作树保持干净。返回该提交 sha。
// 用于「签名污染面」夹具：`log.showSignature=true` 时 git 会把 gpg 程序的 stderr
// 转发到自己的 stdout，且**前置**于消息正文（真 gpg 与假 gpg 同此机制）。
func craftSignedCommitT(t *testing.T, dir, msg string) string {
	t.Helper()
	obj := "tree " + runGitT(t, dir, "rev-parse", "HEAD^{tree}") + "\n" +
		"parent " + runGitT(t, dir, "rev-parse", "HEAD") + "\n" +
		"author t <t@e> 0 +0000\ncommitter t <t@e> 0 +0000\n" +
		"gpgsig -----BEGIN PGP SIGNATURE-----\n \n iQEcBAABAgAGBQJfakefake\n -----END PGP SIGNATURE-----\n\n" +
		msg
	sha := hashObjectT(t, dir, "commit", obj)
	runGitT(t, dir, "update-ref", "HEAD", sha)
	return sha
}

// fakeGpgT 写一个假 gpg 程序（标记行写 **stderr** 并退出非零——git 会把 gpg 程序的
// stderr 转发到自身 stdout 且前置，与真 gpg 校验失败的实测表现一致），返回其路径。
// 放在仓外以免污染工作树；用 `gpg.program` 指过去即可让污染面在任何机器上稳定复现，
// 不依赖本机是否装了 gpg。
func fakeGpgT(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fakegpg")
	if err := os.WriteFile(p, []byte("#!/bin/sh\necho XFAKEGPG-NOISE >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}
