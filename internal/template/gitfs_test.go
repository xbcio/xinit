package template

import (
	"io/fs"
	"os"
	"os/exec"
	"sort"
	"testing"
)

func gitFSFor(t *testing.T, files map[string]struct {
	data string
	mode os.FileMode
}) fs.FS {
	t.Helper()
	dir, commit := newRepo(t, files)
	g, err := NewGitFS(dir, commit)
	if err != nil {
		t.Fatalf("NewGitFS: %v", err)
	}
	return g
}

func TestGitFSReadFile(t *testing.T) {
	g := gitFSFor(t, map[string]struct {
		data string
		mode os.FileMode
	}{
		"main.go":   {"package main\n", 0o644},
		"sub/a.txt": {"hello\n", 0o644},
	})
	got, err := fs.ReadFile(g, "main.go")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "package main\n" {
		t.Errorf("= %q", got)
	}
	got, err = fs.ReadFile(g, "sub/a.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "hello\n" {
		t.Errorf("= %q", got)
	}
}

func TestGitFSWalkSynthesizesDirectories(t *testing.T) {
	// ls-tree -r 不输出目录 —— 目录树必须从路径前缀合成，
	// 否则 fs.WalkDir 走不进子目录
	g := gitFSFor(t, map[string]struct {
		data string
		mode os.FileMode
	}{
		"a.go":         {"x", 0o644},
		"deep/er/b.go": {"y", 0o644},
	})
	var paths []string
	err := fs.WalkDir(g, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p != "." {
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir: %v", err)
	}
	sort.Strings(paths)
	want := []string{"a.go", "deep", "deep/er", "deep/er/b.go"}
	if len(paths) != len(want) {
		t.Fatalf("遍历结果 = %v, want %v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("遍历结果 = %v, want %v", paths, want)
		}
	}
}

func TestGitFSSymlinkReportsModeSymlink(t *testing.T) {
	// symlink 必须在 fs.FS 层就报出 ModeSymlink，
	// 好让 render 的单一检查生效
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "core.symlinks", "true")
	if err := os.Symlink("target.txt", dir+"/link"); err != nil {
		t.Skipf("本机不支持创建 symlink: %v", err)
	}
	if err := os.WriteFile(dir+"/target.txt", []byte("t\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "init")

	commit, _ := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	g, err := NewGitFS(dir, trimNL(string(commit)))
	if err != nil {
		t.Fatalf("NewGitFS: %v", err)
	}

	info, err := fs.Stat(g, "link")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode()&fs.ModeSymlink == 0 {
		t.Errorf("link 的 mode = %v，应含 ModeSymlink", info.Mode())
	}
}

func TestGitFSPreservesExecBit(t *testing.T) {
	g := gitFSFor(t, map[string]struct {
		data string
		mode os.FileMode
	}{
		"run.sh": {"#!/bin/sh\n", 0o755},
	})
	info, err := fs.Stat(g, "run.sh")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Errorf("exec 位丢失: %v", info.Mode())
	}
}

func TestGitFSMissingFile(t *testing.T) {
	g := gitFSFor(t, map[string]struct {
		data string
		mode os.FileMode
	}{
		"a.go": {"x", 0o644},
	})
	if _, err := fs.ReadFile(g, "nope.go"); err == nil {
		t.Fatal("读取不存在的文件应报错")
	}
}

func TestGitFSIsDeterministic(t *testing.T) {
	g := gitFSFor(t, map[string]struct {
		data string
		mode os.FileMode
	}{
		"b.go": {"b", 0o644},
		"a.go": {"a", 0o644},
	})
	for i := 0; i < 10; i++ {
		ents, err := fs.ReadDir(g, ".")
		if err != nil {
			t.Fatalf("ReadDir: %v", err)
		}
		var names []string
		for _, e := range ents {
			names = append(names, e.Name())
		}
		if len(names) != 2 || names[0] != "a.go" || names[1] != "b.go" {
			t.Fatalf("ReadDir 顺序必须确定（字节序）: %v", names)
		}
	}
}
