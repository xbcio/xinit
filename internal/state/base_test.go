package state

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/xbcio/xinit/internal/render"
)

func TestSyncBaseWritesAllEntries(t *testing.T) {
	dir := t.TempDir()
	entries := []render.Entry{
		{Src: "a.go", Dst: "a.go"},
		{Src: "b.go.tmpl", Dst: "b.go"},
	}
	rendered := map[string][]byte{
		"a.go": []byte("package a\n"),
		"b.go": []byte("package b\n"),
	}
	if err := SyncBase(dir, entries, rendered); err != nil {
		t.Fatalf("SyncBase: %v", err)
	}

	got, err := ReadBase(dir, "a.go")
	if err != nil {
		t.Fatalf("ReadBase: %v", err)
	}
	if string(got) != "package a\n" {
		t.Errorf("= %q", got)
	}

	// 键必须是 Dst —— 与 state.files 的键保持一致
	got, err = ReadBase(dir, "b.go")
	if err != nil {
		t.Fatalf("ReadBase(b.go): %v", err)
	}
	if string(got) != "package b\n" {
		t.Errorf("base 必须按 Dst 布局: %q", got)
	}
}

func TestSyncBaseRemovesStaleEntries(t *testing.T) {
	dir := t.TempDir()

	if err := SyncBase(dir, []render.Entry{{Src: "old.go", Dst: "old.go"}}, map[string][]byte{
		"old.go": []byte("x"),
	}); err != nil {
		t.Fatal(err)
	}

	// 第二次同步时 old.go 已不在模板里
	if err := SyncBase(dir, []render.Entry{{Src: "new.go", Dst: "new.go"}}, map[string][]byte{
		"new.go": []byte("y"),
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := ReadBase(dir, "old.go"); err == nil {
		t.Error("已从模板移除的路径必须从 base/ 中删掉，否则 update 会拿到过期的 base")
	}
	if _, err := ReadBase(dir, "new.go"); err != nil {
		t.Errorf("新路径应写入: %v", err)
	}
}

func TestSyncBasePreservesExecBit(t *testing.T) {
	dir := t.TempDir()
	entries := []render.Entry{{Src: "run.sh", Dst: "run.sh", Exec: true}}
	if err := SyncBase(dir, entries, map[string][]byte{"run.sh": []byte("#!/bin/sh\n")}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(BaseDir(dir), "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("权限 = %v, want 0755", info.Mode().Perm())
	}
}

func TestSyncBaseIsByteExact(t *testing.T) {
	// base 存的是**渲染后的原始字节**，不做 EOL 归一化 ——
	// git merge-file 要的是可比的原始字节，不是归一化后的
	dir := t.TempDir()
	raw := []byte("a\r\nb\r\n")
	if err := SyncBase(dir,
		[]render.Entry{{Src: "a.txt", Dst: "a.txt"}},
		map[string][]byte{"a.txt": raw},
	); err != nil {
		t.Fatal(err)
	}
	got, err := ReadBase(dir, "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(raw) {
		t.Errorf("base 必须逐字节保存，实际 %q", got)
	}
}

func TestSyncBaseMissingContentIsError(t *testing.T) {
	// Entry 与 rendered 必须一致 —— 少一份内容说明调用方有 bug
	dir := t.TempDir()
	err := SyncBase(dir, []render.Entry{{Src: "a.go", Dst: "a.go"}}, map[string][]byte{})
	if err == nil {
		t.Fatal("缺少渲染内容应报错，不能写空文件")
	}
}

func TestSyncBaseRejectsTraversalDst(t *testing.T) {
	// Dst 可以引用 vars，而变量未必有 pattern —— 值可以是 "../evil.go"。
	// 写路径必须和读路径一样守（checkWithinBase 的注释点名的就是这个危险），
	// 且必须先整体校验再落盘：坏路径不能留下半棵 base 树。
	dir := t.TempDir()
	err := SyncBase(dir,
		[]render.Entry{
			{Src: "ok.go", Dst: "ok.go"},
			{Src: "evil.go", Dst: "../evil.go"},
		},
		map[string][]byte{
			"ok.go":      []byte("ok\n"),
			"../evil.go": []byte("evil\n"),
		})
	if err == nil {
		t.Fatal("含 .. 的 Dst 必须被拒绝")
	}

	// 合法的那一份也不能落盘 —— 校验必须先于任何写入
	if _, statErr := os.Stat(filepath.Join(BaseDir(dir), "ok.go")); statErr == nil {
		t.Error("校验失败时不得留下半棵 base 树")
	}
	// 越界目标（base 的上一级）不得出现
	if _, statErr := os.Stat(filepath.Join(dir, DirName, "evil.go")); statErr == nil {
		t.Error("越界路径不得落盘")
	}
}

func TestSyncBaseDeterministic(t *testing.T) {
	entries := []render.Entry{
		{Src: "a.go", Dst: "a.go"},
		{Src: "b.go", Dst: "b.go"},
	}
	rendered := map[string][]byte{"a.go": []byte("a"), "b.go": []byte("b")}

	dir1 := t.TempDir()
	dir2 := t.TempDir()
	if err := SyncBase(dir1, entries, rendered); err != nil {
		t.Fatal(err)
	}
	if err := SyncBase(dir2, entries, rendered); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"a.go", "b.go"} {
		x, err := ReadBase(dir1, name)
		if err != nil {
			t.Fatal(err)
		}
		y, err := ReadBase(dir2, name)
		if err != nil {
			t.Fatal(err)
		}
		if string(x) != string(y) {
			t.Errorf("%s 的 base 不确定", name)
		}
	}
}

func TestReadBaseRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	if err := SyncBase(dir, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadBase(dir, "../../etc/passwd"); err == nil {
		t.Fatal("路径穿越必须被拒绝")
	}
}

func TestReadBaseMissing(t *testing.T) {
	dir := t.TempDir()
	if err := SyncBase(dir, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadBase(dir, "nope.go"); err == nil {
		t.Fatal("不存在的 base 应报错")
	}
}
