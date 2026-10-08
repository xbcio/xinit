package fsutil

import (
	"strings"
	"testing"
)

func TestCompareHashIgnoresEOL(t *testing.T) {
	a := CompareHash([]byte("a\r\nb\r\n"), false)
	b := CompareHash([]byte("a\nb\n"), false)
	if a != b {
		t.Errorf("CRLF 与 LF 的归一化 hash 应相同:\n%q\n%q", a, b)
	}
}

func TestCompareHashDistinguishesContent(t *testing.T) {
	if CompareHash([]byte("a\nb\n"), false) == CompareHash([]byte("a\nc\n"), false) {
		t.Error("不同内容不应同 hash")
	}
}

func TestCompareHashBinaryUsesRawBytes(t *testing.T) {
	// 二进制不做归一化：\r\n 与 \n 是不同的字节序列
	a := CompareHash([]byte{0x00, '\r', '\n'}, true)
	b := CompareHash([]byte{0x00, '\n'}, true)
	if a == b {
		t.Error("二进制的原始字节不同，不应同 hash")
	}
}

func TestCompareHashPrefixesAlgo(t *testing.T) {
	if got := CompareHash([]byte("x"), false); !strings.HasPrefix(got, HashAlgo+":") {
		t.Errorf("hash 应带算法前缀 %q，实际 %q", HashAlgo, got)
	}
	if HashAlgo != "sha256/normlf-v1" {
		t.Errorf("HashAlgo 被改动，会破坏已落盘状态的兼容性: %q", HashAlgo)
	}
}

func TestRenderTreeHashSeparatesPathFromContent(t *testing.T) {
	// "ab" + "" 与 "a" + "b" 必须不同 hash —— 这是 v2 的编码缺陷
	a := RenderTreeHash([]TreeEntry{
		{Src: "ab", Kind: "regular", Hash: "sha256:x"},
		{Src: "", Kind: "regular", Hash: "sha256:x"},
	})
	b := RenderTreeHash([]TreeEntry{
		{Src: "a", Kind: "regular", Hash: "sha256:x"},
		{Src: "b", Kind: "regular", Hash: "sha256:x"},
	})
	if a == b {
		t.Error("路径与内容边界不同的两棵树不应同 hash")
	}
}

func TestRenderTreeHashDetectsRename(t *testing.T) {
	base := []TreeEntry{{Src: "a.go", Kind: "regular", Hash: "sha256:x"}}
	moved := []TreeEntry{{Src: "b.go", Kind: "regular", Hash: "sha256:x"}}
	if RenderTreeHash(base) == RenderTreeHash(moved) {
		t.Error("改名应改变树 hash")
	}
}

func TestRenderTreeHashDetectsExecChange(t *testing.T) {
	plain := []TreeEntry{{Src: "a.sh", Kind: "regular", Exec: false, Hash: "sha256:x"}}
	exec := []TreeEntry{{Src: "a.sh", Kind: "regular", Exec: true, Hash: "sha256:x"}}
	if RenderTreeHash(plain) == RenderTreeHash(exec) {
		t.Error("exec 位变化应改变树 hash")
	}
}

func TestRenderTreeHashDetectsKindChange(t *testing.T) {
	reg := []TreeEntry{{Src: "a", Kind: "regular", Hash: "sha256:x"}}
	lnk := []TreeEntry{{Src: "a", Kind: "symlink", Hash: "sha256:x"}}
	if RenderTreeHash(reg) == RenderTreeHash(lnk) {
		t.Error("kind 变化应改变树 hash")
	}
}

func TestRenderTreeHashIsOrderIndependent(t *testing.T) {
	a := []TreeEntry{
		{Src: "b", Kind: "regular", Hash: "sha256:2"},
		{Src: "a", Kind: "regular", Hash: "sha256:1"},
	}
	b := []TreeEntry{
		{Src: "a", Kind: "regular", Hash: "sha256:1"},
		{Src: "b", Kind: "regular", Hash: "sha256:2"},
	}
	if RenderTreeHash(a) != RenderTreeHash(b) {
		t.Error("树 hash 必须与输入顺序无关（内部按 Src 字节序排序）")
	}
}
