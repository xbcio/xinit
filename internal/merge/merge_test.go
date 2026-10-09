package merge

import (
	"bytes"
	"strings"
	"testing"
)

func TestThreeWayClean(t *testing.T) {
	base := []byte("a\nb\nc\nd\ne\nf\ng\n")
	ours := []byte("A\nb\nc\nd\ne\nf\ng\n")   // 只动第 1 行
	theirs := []byte("a\nb\nc\nd\ne\nf\nG\n") // 只动第 7 行

	res, err := ThreeWay(ours, base, theirs)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != Clean || res.Conflicts != 0 {
		t.Fatalf("两边改动不重叠应为干净合并；got %v, conflicts=%d", res.Outcome, res.Conflicts)
	}
	want := "A\nb\nc\nd\ne\nf\nG\n"
	if string(res.Bytes) != want {
		t.Fatalf("合并结果应同时含两边的改动；got %q, want %q", res.Bytes, want)
	}
}

func TestThreeWayConflicts(t *testing.T) {
	base := []byte("a\nb\nc\nd\ne\n")
	ours := []byte("A\nb\nc\nd\nE\n")
	theirs := []byte("X\nb\nc\nd\nZ\n") // 第 1、5 行各自冲突

	res, err := ThreeWay(ours, base, theirs)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != Conflicts {
		t.Fatalf("应报冲突；got %v", res.Outcome)
	}
	if res.Conflicts != 2 {
		t.Fatalf("两个冲突块的 rc 应为 2；got %d", res.Conflicts)
	}
	out := string(res.Bytes)
	for _, marker := range []string{"<<<<<<< ours\n", "||||||| base\n", "=======\n", ">>>>>>> theirs\n"} {
		if !strings.Contains(out, marker) {
			t.Errorf("diff3 输出缺少标记 %q：\n%s", marker, out)
		}
	}
}

func TestThreeWayBinaryIsError(t *testing.T) {
	// 二进制永远不会走到 Merge（§8.2 行 6 先拦下），但真走到了必须按
	// §8.6 落到 Error（调用方改判 ConflictBinary）——git 自己就拒绝：
	// rc 255 + "error: Cannot merge binary files"
	res, err := ThreeWay([]byte("aa\x00bb\n"), []byte("cc\x00dd\n"), []byte("ee\x00ff\n"))
	if err != nil {
		t.Fatalf("进程正常结束（rc 255）不应返回 Go error: %v", err)
	}
	if res.Outcome != Error {
		t.Fatalf("二进制应判 Error；got %v", res.Outcome)
	}
	if len(res.Bytes) != 0 {
		t.Fatalf("Error 时不应带回输出；got %q", res.Bytes)
	}
}

func TestThreeWayNormalizesCRLFToLF(t *testing.T) {
	// §9.1：三方先归一到 LF 再喂给 merge-file；结果是 LF，由调用方还原
	base := []byte("a\r\nb\r\nc\r\nd\r\ne\r\nf\r\ng\r\n")
	ours := []byte("A\r\nb\r\nc\r\nd\r\ne\r\nf\r\ng\r\n")
	theirs := []byte("a\r\nb\r\nc\r\nd\r\ne\r\nf\r\nG\r\n")

	res, err := ThreeWay(ours, base, theirs)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != Clean {
		t.Fatalf("got %v", res.Outcome)
	}
	if bytes.ContainsRune(res.Bytes, '\r') {
		t.Fatalf("输出必须是 LF（还原是调用方的事）；got %q", res.Bytes)
	}
	if string(res.Bytes) != "A\nb\nc\nd\ne\nf\nG\n" {
		t.Fatalf("got %q", res.Bytes)
	}
}
