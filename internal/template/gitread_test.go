package template

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newRepo 建一个临时 git 仓库并返回 (目录, commit sha)。
//
// 注意：返回值就是最终的 (dir, commit) 对，调用方不需要（也不应该）
// 再对返回的 commit 字符串执行 `git -C <commit> ...`——那会把 40 位
// hex 当成目录路径，必然以 128 退出。
func newRepo(t *testing.T, files map[string]struct {
	data string
	mode os.FileMode
}) (dir, commit string) {
	t.Helper()
	dir = t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")

	for rel, f := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		mode := f.mode
		if mode == 0 {
			mode = 0o644
		}
		if err := os.WriteFile(full, []byte(f.data), mode); err != nil {
			t.Fatal(err)
		}
	}
	run("add", "-A")
	run("commit", "-q", "-m", "init")

	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	return dir, trimNL(string(out))
}

func trimNL(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

func TestListTreeModes(t *testing.T) {
	dir, commit := newRepo(t, map[string]struct {
		data string
		mode os.FileMode
	}{
		"a.go":     {"package a\n", 0o644},
		"run.sh":   {"#!/bin/sh\n", 0o755},
		"sub/b.go": {"package b\n", 0o644},
	})

	entries, err := ListTree(dir, commit)
	if err != nil {
		t.Fatalf("ListTree: %v", err)
	}
	byPath := map[string]TreeEntry{}
	for _, e := range entries {
		byPath[e.Path] = e
	}

	a, ok := byPath["a.go"]
	if !ok {
		t.Fatalf("未找到 a.go，实际: %+v", entries)
	}
	if a.Exec {
		t.Error("0644 不应带 Exec")
	}
	if a.Symlink {
		t.Error("普通文件不应标为 symlink")
	}

	sh, ok := byPath["run.sh"]
	if !ok {
		t.Fatal("未找到 run.sh")
	}
	if !sh.Exec {
		t.Error("100755 应带 Exec")
	}

	if _, ok := byPath["sub/b.go"]; !ok {
		t.Error("子目录下的文件应被 -r 列出")
	}
}

func TestListTreeIncludesExportIgnoredFiles(t *testing.T) {
	// 这是不用 git archive 的核心理由：实测 git archive 会静默丢弃
	// 标记 export-ignore 的文件
	dir, commit := newRepo(t, map[string]struct {
		data string
		mode os.FileMode
	}{
		".gitattributes": {"secret.txt export-ignore\n", 0o644},
		"ok.txt":         {"ok\n", 0o644},
		"secret.txt":     {"secret\n", 0o644},
	})

	// 先确认 git archive 确实丢文件（固定这个前提，免得将来被"优化"回去）。
	//
	// 注意：探测的是 secret.txt 的**内容**（"secret\n"），不是它的文件名
	// ("secret.txt")——文件名作为纯文本会出现在 .gitattributes 自身的
	// 内容里（.gitattributes 没有被 export-ignore，必然被打包），按文件名
	// 做子串匹配会产生假阳性。内容字符串在仓库里是唯一的，不会被其他
	// 被打包的文件带进 tar。
	archiveOut, err := exec.Command("git", "-C", dir, "archive", "--format=tar", commit).Output()
	if err != nil {
		t.Fatalf("git archive: %v", err)
	}
	if containsBytes(archiveOut, []byte("secret\n")) {
		t.Fatal("前提变了：git archive 现在会保留 export-ignore 的文件，请重新评估读取方式")
	}

	// ls-tree 必须看得到它
	entries, err := ListTree(dir, commit)
	if err != nil {
		t.Fatalf("ListTree: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Path == "secret.txt" {
			found = true
		}
	}
	if !found {
		t.Error("ls-tree 必须列出 export-ignore 的文件")
	}
}

func containsBytes(haystack, needle []byte) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		indexBytes(haystack, needle) >= 0
}

func indexBytes(h, n []byte) int {
	for i := 0; i+len(n) <= len(h); i++ {
		match := true
		for j := range n {
			if h[i+j] != n[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func TestListTreeRejectsSubmodule(t *testing.T) {
	// mode 160000 既不是 blob，也没有对应的 Kind —— 明确拒绝
	// 这里不真的建 submodule（需要网络），改为直接断言 mode 映射函数
	if _, err := mapGitMode("160000", "deadbeef"); err == nil {
		t.Fatal("160000（submodule）应报错拒绝")
	}
}

func TestMapGitMode(t *testing.T) {
	tests := []struct {
		mode      string
		wantExec  bool
		wantLink  bool
		wantError bool
	}{
		{"100644", false, false, false},
		{"100664", false, false, false}, // 历史上的 group-writable
		{"100755", true, false, false},
		{"120000", false, true, false},
		{"160000", false, false, true},
		{"040000", false, false, true},
		{"999999", false, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			e, err := mapGitMode(tt.mode, "hash")
			if tt.wantError {
				if err == nil {
					t.Fatalf("mode %s 应报错", tt.mode)
				}
				return
			}
			if err != nil {
				t.Fatalf("mode %s: %v", tt.mode, err)
			}
			if e.Exec != tt.wantExec {
				t.Errorf("Exec = %v, want %v", e.Exec, tt.wantExec)
			}
			if e.Symlink != tt.wantLink {
				t.Errorf("Symlink = %v, want %v", e.Symlink, tt.wantLink)
			}
		})
	}
}

func TestCatBlobRoundTrip(t *testing.T) {
	dir, commit := newRepo(t, map[string]struct {
		data string
		mode os.FileMode
	}{
		"a.txt": {"hello\n", 0o644},
	})
	entries, err := ListTree(dir, commit)
	if err != nil {
		t.Fatalf("ListTree: %v", err)
	}
	got, err := CatBlob(dir, entries[0].BlobHash)
	if err != nil {
		t.Fatalf("CatBlob: %v", err)
	}
	if string(got) != "hello\n" {
		t.Errorf("= %q", got)
	}
}

func TestCatBlobHandlesContentThatLooksLikeHeader(t *testing.T) {
	// 文件内容恰好形如 "<40hex> blob 5" 时不能串位 —— 解析必须按 header
	// 里的 size 精确读取
	dir, commit := newRepo(t, map[string]struct {
		data string
		mode os.FileMode
	}{
		"tricky.txt": {"deadbeefdeadbeefdeadbeefdeadbeefdeadbeef blob 5\nmore\n", 0o644},
	})
	entries, err := ListTree(dir, commit)
	if err != nil {
		t.Fatalf("ListTree: %v", err)
	}
	got, err := CatBlob(dir, entries[0].BlobHash)
	if err != nil {
		t.Fatalf("CatBlob: %v", err)
	}
	want := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef blob 5\nmore\n"
	if string(got) != want {
		t.Errorf("= %q, want %q", got, want)
	}
}

func TestListTreeHandlesNewlineInFilename(t *testing.T) {
	// 不用 -z 的话 git 会给含换行的文件名加引号
	dir, commit := newRepo(t, map[string]struct {
		data string
		mode os.FileMode
	}{
		"nl\nname.txt": {"x\n", 0o644},
	})
	entries, err := ListTree(dir, commit)
	if err != nil {
		t.Fatalf("ListTree: %v", err)
	}
	if len(entries) != 1 || entries[0].Path != "nl\nname.txt" {
		t.Errorf("含换行的文件名解析错误: %+v", entries)
	}
}

func TestRunGitRejectsOptionLikeArgs(t *testing.T) {
	// revision 不得以 - 开头，否则会被当成 git 选项
	dir := t.TempDir()
	if _, err := ListTree(dir, "--upload-pack=evil"); err == nil {
		t.Fatal("以 - 开头的 revision 应被拒绝")
	}
}

func TestCatBlobErrorIncludesGitStderr(t *testing.T) {
	// git 失败时必须带上 git 自己的 stderr，只报 "exit status 128"
	// 会把真正原因丢掉（例如目录不存在）
	missing := filepath.Join(t.TempDir(), "no-such-repo")
	hash := strings.Repeat("0", 40)
	_, err := CatBlob(missing, hash)
	if err == nil {
		t.Fatal("仓库目录不存在应报错")
	}
	if !strings.Contains(err.Error(), "fatal") {
		t.Errorf("错误应包含 git 的 stderr（fatal: ...）: %v", err)
	}
}

func TestListTreeHandlesTabInFilename(t *testing.T) {
	// TAB 正是 ls-tree -z 的字段分隔符：按「第一个 TAB」切分才对，
	// 若将来有人改成 LastIndexByte 之类，这条测试会红
	dir, commit := newRepo(t, map[string]struct {
		data string
		mode os.FileMode
	}{
		"a\tb.txt": {"x\n", 0o644},
	})
	entries, err := ListTree(dir, commit)
	if err != nil {
		t.Fatalf("ListTree: %v", err)
	}
	if len(entries) != 1 || entries[0].Path != "a\tb.txt" {
		t.Fatalf("含 TAB 的文件名必须原样保留: %+v", entries)
	}
}
