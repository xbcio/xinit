package renderstore

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// sampleManifest 故意让 Files 乱序（b 在 a 前），供排序断言。
func sampleManifest() *manifest {
	return &manifest{
		Version:    manifestVersion,
		Engine:     "e1",
		Source:     "/tpl",
		Kind:       "dir",
		Anchor:     "sha256/c1",
		VarsSHA256: "abcd",
		Files: []fileEntry{
			{Dst: "b.txt", RawSHA256: "bb", Size: 2},
			{Dst: "a.txt", RawSHA256: "aa", Size: 1},
		},
	}
}

func TestManifestRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), manifestName)
	data, err := sampleManifest().encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readManifest(p)
	if err != nil {
		t.Fatalf("readManifest: %v", err)
	}

	want := sampleManifest()
	want.Files = []fileEntry{ // encode 按 dst 字节序输出
		{Dst: "a.txt", RawSHA256: "aa", Size: 1},
		{Dst: "b.txt", RawSHA256: "bb", Size: 2},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("往返不相等：\ngot  %+v\nwant %+v", got, want)
	}
}

func TestManifestEncodeSortsFilesByDst(t *testing.T) {
	data, err := sampleManifest().encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	ia := bytes.Index(data, []byte(`"a.txt"`))
	ib := bytes.Index(data, []byte(`"b.txt"`))
	if ia < 0 || ib < 0 || ia > ib {
		t.Errorf("files 必须按 dst 字节序输出：a@%d b@%d\n%s", ia, ib, data)
	}
}

func TestManifestEncodeIsDeterministic(t *testing.T) {
	m := sampleManifest()
	d1, err := m.encode()
	if err != nil {
		t.Fatalf("encode#1: %v", err)
	}
	d2, err := m.encode()
	if err != nil {
		t.Fatalf("encode#2: %v", err)
	}
	if !bytes.Equal(d1, d2) {
		t.Error("两次 encode 必须字节相同（确定性）")
	}
	if !bytes.HasSuffix(d1, []byte("\n")) {
		t.Error("encode 应以尾换行结束")
	}
	if !bytes.Contains(d1, []byte("\n  \"version\"")) {
		t.Error("encode 必须用 MarshalIndent 两空格缩进")
	}
	// encode 不得改动接收者（乱序输入 → 输出排序，但输入顺序不动）。
	if m.Files[0].Dst != "b.txt" || m.Files[1].Dst != "a.txt" {
		t.Errorf("encode 不应改动接收者的 Files 顺序：%+v", m.Files)
	}
}

func TestReadManifestErrors(t *testing.T) {
	dir := t.TempDir()

	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readManifest(bad); err == nil {
		t.Error("坏 JSON 必须报错")
	}

	empty := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readManifest(empty); err == nil {
		t.Error("空文件必须报错")
	}

	if _, err := readManifest(filepath.Join(dir, "missing.json")); err == nil {
		t.Error("不存在的清单必须报错")
	}
}
