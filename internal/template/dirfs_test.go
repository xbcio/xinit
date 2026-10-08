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

func TestTreeHashIsOrderIndependent(t *testing.T) {
	// 同一个文件集合、不同的创建顺序（遍历顺序随之不同）必须得到同一个
	// hash —— 这才是排序确定性的可观测后果。原来的写法只对同一目录调用
	// 两次，与 TestTreeHashStableAcrossRuns 重复，测不到排序。
	mk := func(order []string) string {
		dir := t.TempDir()
		for _, name := range order {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	h1, err := TreeHashOf(mk([]string{"B.go", "a.go", "Z.go"}))
	if err != nil {
		t.Fatal(err)
	}
	h2, err := TreeHashOf(mk([]string{"Z.go", "B.go", "a.go"}))
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Errorf("创建顺序不应影响树 hash: %q != %q", h1, h2)
	}
}

func TestTreeHashHandlesSymlinks(t *testing.T) {
	// 符号链接按链接文本 hash。三条子路径都必须不炸：
	// 指向目录、悬空、指向文件；且「指向两个内容相同的不同文件」必须
	// 产生不同的树 hash —— hash 目标内容会让它们相同。
	dir := writeTree(t, map[string]struct {
		data string
		mode os.FileMode
	}{
		"target.txt": {"t\n", 0o644},
		"twin.txt":   {"t\n", 0o644},
	})
	if err := os.MkdirAll(filepath.Join(dir, "realdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target.txt", filepath.Join(dir, "link")); err != nil {
		t.Skipf("本机不支持创建 symlink: %v", err)
	}
	if err := os.Symlink("nowhere", filepath.Join(dir, "dangling")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("realdir", filepath.Join(dir, "dirlink")); err != nil {
		t.Fatal(err)
	}

	h1, err := TreeHashOf(dir)
	if err != nil {
		t.Fatalf("含 symlink 的目录不应整体失败: %v", err)
	}

	// 只改链接文本（twin.txt 与 target.txt 内容相同）
	if err := os.Remove(filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("twin.txt", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	h2, err := TreeHashOf(dir)
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h2 {
		t.Error("链接文本变化应改变树 hash（hash 目标内容会让二者相同）")
	}
}
