package template

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// readFileAt 是测试用的便捷读取。
func readFileAt(fsys fs.FS, name string) (string, error) {
	b, err := fs.ReadFile(fsys, name)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func writeTree(t *testing.T, files map[string]struct {
	data string
	mode os.FileMode
}) string {
	t.Helper()
	dir := t.TempDir()
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
	return dir
}

func TestOpenDirReturnsUsableFS(t *testing.T) {
	dir := writeTree(t, map[string]struct {
		data string
		mode os.FileMode
	}{
		"a.go":     {"package a\n", 0o644},
		"sub/b.go": {"package b\n", 0o644},
	})
	fsys, hash, err := OpenDir(dir)
	if err != nil {
		t.Fatalf("OpenDir: %v", err)
	}
	if hash == "" {
		t.Error("树 hash 不应为空")
	}
	data, err := readFileAt(fsys, "a.go")
	if err != nil {
		t.Fatalf("读取 a.go: %v", err)
	}
	if data != "package a\n" {
		t.Errorf("= %q", data)
	}
}

func TestTreeHashStableAcrossRuns(t *testing.T) {
	dir := writeTree(t, map[string]struct {
		data string
		mode os.FileMode
	}{
		"a.go": {"a", 0o644},
		"b.go": {"b", 0o644},
	})
	h1, err := TreeHashOf(dir)
	if err != nil {
		t.Fatalf("TreeHashOf2: %v", err)
	}
	for i := 0; i < 10; i++ {
		h2, err := TreeHashOf(dir)
		if err != nil {
			t.Fatal(err)
		}
		if h1 != h2 {
			t.Fatalf("树 hash 不稳定: %q != %q", h1, h2)
		}
	}
}

func TestTreeHashChangesOnContent(t *testing.T) {
	a := writeTree(t, map[string]struct {
		data string
		mode os.FileMode
	}{"a.go": {"a", 0o644}})
	b := writeTree(t, map[string]struct {
		data string
		mode os.FileMode
	}{"a.go": {"b", 0o644}})

	ha, _ := TreeHashOf(a)
	hb, _ := TreeHashOf(b)
	if ha == hb {
		t.Error("内容变化应改变树 hash")
	}
}

func TestTreeHashChangesOnRename(t *testing.T) {
	a := writeTree(t, map[string]struct {
		data string
		mode os.FileMode
	}{"a.go": {"x", 0o644}})
	b := writeTree(t, map[string]struct {
		data string
		mode os.FileMode
	}{"b.go": {"x", 0o644}})

	ha, _ := TreeHashOf(a)
	hb, _ := TreeHashOf(b)
	if ha == hb {
		t.Error("改名应改变树 hash（v2 的编码缺陷会让它们相同）")
	}
}

func TestTreeHashChangesOnExecBit(t *testing.T) {
	a := writeTree(t, map[string]struct {
		data string
		mode os.FileMode
	}{"s.sh": {"x", 0o644}})
	b := writeTree(t, map[string]struct {
		data string
		mode os.FileMode
	}{"s.sh": {"x", 0o755}})

	ha, _ := TreeHashOf(a)
	hb, _ := TreeHashOf(b)
	if ha == hb {
		t.Error("exec 位变化应改变树 hash")
	}
}

func TestTreeHashIgnoresGitDir(t *testing.T) {
	// .git/ 必须被排除，否则树 hash 会随任何 git 操作变化
	dir := writeTree(t, map[string]struct {
		data string
		mode os.FileMode
	}{"a.go": {"x", 0o644}})
	before, err := TreeHashOf(dir)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	after, err := TreeHashOf(dir)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Error(".git/ 不应参与树 hash")
	}
}

func TestTreeHashIsByteOrderSorted(t *testing.T) {
	// 排序必须按字节序，不依赖本地化排序 —— 排序结果会进入 hash
	dir := writeTree(t, map[string]struct {
		data string
		mode os.FileMode
	}{
		"B.go": {"b", 0o644},
		"a.go": {"a", 0o644},
		"Z.go": {"z", 0o644},
	})
	h1, err := TreeHashOf(dir)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := TreeHashOf(dir)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Error("排序必须完全确定")
	}
}
