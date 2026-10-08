package state

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestReadPrune(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, DirName), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "# 注释行\n\nfrontend/**\n  LICENSE  \n#hashtag 开头的都是注释\nlegacy.txt\n"
	if err := os.WriteFile(filepath.Join(dir, DirName, "prune.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadPrune(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"frontend/**", "LICENSE", "legacy.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestReadPruneMissingFileIsEmpty(t *testing.T) {
	got, err := ReadPrune(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("文件缺失应返回空名单，got %q", got)
	}
}
