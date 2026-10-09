package worktree

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/xbcio/xinit/internal/fsutil"
	"github.com/xbcio/xinit/internal/render"
)

// mkTree 建一个含各种类型条目的工作树。
func mkTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel string, data []byte, mode os.FileMode) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, mode); err != nil {
			t.Fatal(err)
		}
	}
	write("a.txt", []byte("hello\n"), 0o644)
	write("b.sh", []byte("#!/bin/sh\necho hi\n"), 0o755)
	write("crlf.txt", []byte("x\r\ny\r\n"), 0o644)
	write("mixed.txt", []byte("x\r\ny\n"), 0o644)
	write("bin.dat", []byte("a\x00b"), 0o644)
	if err := os.Symlink("a.txt", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestScan(t *testing.T) {
	root := mkTree(t)
	paths := []string{"a.txt", "b.sh", "crlf.txt", "mixed.txt", "bin.dat", "link", "sub", "missing.txt"}
	snap, contents, err := Scan(root, paths)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snap["missing.txt"]; ok {
		t.Error("不存在的路径不应入快照（缺项即 ours✗）")
	}
	if _, ok := snap["sub"]; ok {
		t.Error("目录不应入快照（P 位置是目录由 apply 期 EISDIR 兜住，§9.3）")
	}

	a := snap["a.txt"]
	if a.Kind != render.KindRegular || a.Exec || a.Binary || a.MixedEOL {
		t.Errorf("a.txt 元数据不对：%+v", a)
	}
	if a.Hash != fsutil.ContentHash([]byte("hello\n")) {
		t.Errorf("a.txt hash = %q", a.Hash)
	}
	if string(contents["a.txt"]) != "hello\n" {
		t.Errorf("contents[a.txt] = %q", contents["a.txt"])
	}

	if !snap["b.sh"].Exec {
		t.Error("b.sh 应带执行位（判据 mode&0111 != 0，与 render.Paths 一致）")
	}
	if snap["crlf.txt"].MixedEOL || snap["crlf.txt"].Binary {
		t.Errorf("crlf.txt 应只是普通文本：%+v", snap["crlf.txt"])
	}
	if snap["crlf.txt"].Hash != fsutil.ContentHash([]byte("x\ny\n")) {
		t.Error("CRLF 文本的 hash 应等于其 LF 版本（§9.1 归一化）")
	}
	if !snap["mixed.txt"].MixedEOL {
		t.Error("mixed.txt 应标 MixedEOL")
	}
	if !snap["bin.dat"].Binary {
		t.Error("bin.dat 应标 Binary")
	}
	if snap["bin.dat"].MixedEOL {
		t.Error("无 CR 的二进制内容不应标 MixedEOL（两判据独立探测，不合并）")
	}

	if snap["link"].Kind != render.KindSymlink {
		t.Errorf("link 应记为 symbol link：%+v", snap["link"])
	}
	if _, ok := contents["link"]; ok {
		t.Error("符号链接不应读取内容（不跟随）")
	}
}

func TestListNames(t *testing.T) {
	root := mkTree(t)
	names, err := ListNames(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a.txt", "b.sh", "bin.dat", "crlf.txt", "link", "mixed.txt", "sub"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("got %q, want %q（os.ReadDir 按名字节序，确定性）", names, want)
	}
	names, err = ListNames(filepath.Join(root, "nope"))
	if err != nil || names != nil {
		t.Fatalf("目录不存在应为 nil, nil；got %q, %v", names, err)
	}
}

func TestExists(t *testing.T) {
	root := mkTree(t)
	for _, rel := range []string{"a.txt", "sub", "link"} {
		ok, err := Exists(root, rel)
		if err != nil || !ok {
			t.Errorf("Exists(%q) = %v, %v；want true, nil", rel, ok, err)
		}
	}
	ok, err := Exists(root, "missing.txt")
	if err != nil || ok {
		t.Errorf("缺失路径应 false, nil；got %v, %v", ok, err)
	}
	// 中间段是文件（ENOTDIR）→ 精确语义下「不存在」，不报错
	ok, err = Exists(root, "a.txt/child")
	if err != nil || ok {
		t.Errorf("中间段是文件应 false, nil；got %v, %v", ok, err)
	}
	// 中间段是符号链接 → 不穿透，视为不存在
	ok, err = Exists(root, "link/child")
	if err != nil || ok {
		t.Errorf("中间段是符号链接不应穿透；got %v, %v", ok, err)
	}
	// 非法段直接报错（调用方传入的一定是已校验的 Dst；来这里说明有 bug）
	if _, err := Exists(root, "a/../b"); err == nil {
		t.Error("含 .. 的路径应报错")
	}
}

func TestWithinResolvedRoot(t *testing.T) {
	root := mkTree(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	ok, err := WithinResolvedRoot(root, "sub/new.txt")
	if err != nil || !ok {
		t.Fatalf("根内新路径应为 true；got %v, %v", ok, err)
	}
	ok, err = WithinResolvedRoot(root, "a.txt")
	if err != nil || !ok {
		t.Fatalf("已存在文件应为 true；got %v, %v", ok, err)
	}
	ok, err = WithinResolvedRoot(root, "escape/x.txt")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("经指向外部的符号链接必须判 false（§9.5）")
	}
	// 相对 root：内部先做 Abs，不应因相对路径产生假阴性
	if ok, err := WithinResolvedRoot(".", "internal"); err != nil || !ok {
		t.Fatalf("相对根：got %v, %v", ok, err)
	}
}
