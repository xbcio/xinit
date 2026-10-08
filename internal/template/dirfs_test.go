package template

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/xbcio/xinit/internal/fsutil"
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
	fsys, err := OpenDir(dir)
	if err != nil {
		t.Fatalf("OpenDir: %v", err)
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
	h1, err := TreeHashOf(dir, nil)
	if err != nil {
		t.Fatalf("TreeHashOf2: %v", err)
	}
	for i := 0; i < 10; i++ {
		h2, err := TreeHashOf(dir, nil)
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

	ha, _ := TreeHashOf(a, nil)
	hb, _ := TreeHashOf(b, nil)
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

	ha, _ := TreeHashOf(a, nil)
	hb, _ := TreeHashOf(b, nil)
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

	ha, _ := TreeHashOf(a, nil)
	hb, _ := TreeHashOf(b, nil)
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
	before, err := TreeHashOf(dir, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	after, err := TreeHashOf(dir, nil)
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
	h1, err := TreeHashOf(mk([]string{"B.go", "a.go", "Z.go"}), nil)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := TreeHashOf(mk([]string{"Z.go", "B.go", "a.go"}), nil)
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

	h1, err := TreeHashOf(dir, nil)
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
	h2, err := TreeHashOf(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h2 {
		t.Error("链接文本变化应改变树 hash（hash 目标内容会让二者相同）")
	}
}

// TestTreeHashHonorsExclude 钉住 F1：dir 源的树 hash 必须按清单 exclude
// 排除，且目录命中要整树跳过（fs.SkipDir），与 render.Paths 的 §3.2
// 语义一致 —— 否则被排除目录下新增文件不会被跳过，只是不参与 hash 的
// "文件级" 排除是不够的，必须验证 SkipDir 真的生效。
func TestTreeHashHonorsExclude(t *testing.T) {
	dir := writeTree(t, map[string]struct {
		data string
		mode os.FileMode
	}{
		"keep.txt":   {"keep-v1", 0o644},
		"skip/a.txt": {"skip-v1", 0o644},
	})
	m, err := fsutil.CompileGlob([]string{"skip/**"})
	if err != nil {
		t.Fatalf("CompileGlob: %v", err)
	}

	base, err := TreeHashOf(dir, m)
	if err != nil {
		t.Fatalf("TreeHashOf: %v", err)
	}

	// (1) 改 skip/a.txt 内容 → hash 不变
	if err := os.WriteFile(filepath.Join(dir, "skip", "a.txt"), []byte("skip-v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	afterEdit, err := TreeHashOf(dir, m)
	if err != nil {
		t.Fatalf("TreeHashOf: %v", err)
	}
	if afterEdit != base {
		t.Error("改 exclude 命中的文件内容应不改变树 hash")
	}

	// (2) skip/ 下新增文件 → hash 不变（证明 SkipDir 真的生效，
	// 不是只在文件级跳过已存在的文件）
	if err := os.WriteFile(filepath.Join(dir, "skip", "b.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	afterAdd, err := TreeHashOf(dir, m)
	if err != nil {
		t.Fatalf("TreeHashOf: %v", err)
	}
	if afterAdd != base {
		t.Error("exclude 命中的目录下新增文件应不改变树 hash（SkipDir 未生效）")
	}

	// (3) 改 keep.txt → hash 变
	if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("keep-v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	afterKeepEdit, err := TreeHashOf(dir, m)
	if err != nil {
		t.Fatalf("TreeHashOf: %v", err)
	}
	if afterKeepEdit == base {
		t.Error("改未被排除的文件应改变树 hash")
	}
}
