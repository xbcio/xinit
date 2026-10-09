package hooks

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// touch 以可控 mtime 写文件（避免同秒写入导致 mtime 相同的假阴性）。
func touch(t *testing.T, root, rel, content string, mt time.Time) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(full, mt, mt); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotSkipsGitDir(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	touch(t, root, "a.txt", "a", now)
	touch(t, root, ".git/index", "x", now)
	touch(t, root, "sub/.git/config", "y", now)
	snap, err := Snapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snap["a.txt"]; !ok {
		t.Fatalf("常规文件应进快照: %v", snap)
	}
	if _, ok := snap[".git/index"]; ok {
		t.Fatal(".git 必须跳过（钩子跑 git 命令不产生噪声）")
	}
	if _, ok := snap["sub/.git/config"]; ok {
		t.Fatal("任意层级的 .git 都必须跳过")
	}
}

func TestClassifyCategories(t *testing.T) {
	t0 := time.Unix(1000, 0)
	// 内容以 Size=len(内容) 表达（Entry 只记 mtime/size）；"p"/"g"/"x"/"P"/"G"/"S" 均长 1。
	before := map[string]Entry{
		"planned.txt": {ModTime: t0, Size: 1}, // 内容 "p"
		"gen.txt":     {ModTime: t0, Size: 1}, // 内容 "g"
		"plain.txt":   {ModTime: t0, Size: 1}, // 内容 "x"
	}
	after := map[string]Entry{
		"planned.txt": {ModTime: time.Unix(2000, 0), Size: 1}, // 计划内被改写（内容 "P"）
		"gen.txt":     {ModTime: time.Unix(2000, 0), Size: 1}, // generated 重建：静默（内容 "G"）
		"plain.txt":   {ModTime: t0, Size: 1},                 // 未触碰（内容 "x"）
		"stray.txt":   {ModTime: t0, Size: 1},                 // 新增且不在计划/generated：计划外（内容 "S"）
	}
	unplanned, modified := Classify(before, after,
		map[string]bool{"planned.txt": true}, map[string]bool{"gen.txt": true})
	if !reflect.DeepEqual(unplanned, []string{"stray.txt"}) {
		t.Fatalf("unplanned=%v", unplanned)
	}
	if !reflect.DeepEqual(modified, []string{"planned.txt"}) {
		t.Fatalf("modified=%v", modified)
	}
}

func TestClassifyDeletion(t *testing.T) {
	t0 := time.Unix(1000, 0)
	before := map[string]Entry{"gone.txt": {ModTime: t0, Size: 1}, "p.txt": {ModTime: t0, Size: 1}}
	after := map[string]Entry{"p.txt": {ModTime: t0, Size: 1}}
	unplanned, modified := Classify(before, after,
		map[string]bool{"p.txt": true}, map[string]bool{})
	if !reflect.DeepEqual(unplanned, []string{"gone.txt"}) {
		t.Fatalf("钩子删除文件属于计划外副作用: unplanned=%v", unplanned)
	}
	if len(modified) != 0 {
		t.Fatalf("modified=%v", modified)
	}
}

func TestClassifySameMtimeDifferentSize(t *testing.T) {
	t0 := time.Unix(1000, 0)
	before := map[string]Entry{"a": {ModTime: t0, Size: 2}} // 内容 "ab"
	after := map[string]Entry{"a": {ModTime: t0, Size: 4}}  // 内容 "abcd"
	unplanned, _ := Classify(before, after, map[string]bool{}, map[string]bool{})
	if !reflect.DeepEqual(unplanned, []string{"a"}) {
		t.Fatalf("size 变化必须判触碰: %v", unplanned)
	}
}
