package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbcio/xinit/internal/fsutil"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := &State{
		Version:       CurrentVersion,
		HashAlgo:      "sha256/normlf-v1",
		EngineVersion: "0.1.0",
		Template: TemplateRef{
			Source:   "git@github.com:xbcio/xkitora.git",
			Kind:     "git",
			Revision: "v0.2.4",
			Commit:   "f7bbd61f00000000000000000000000000000000",
		},
		Vars:         map[string]string{"project_name": "Xdesk", "app_slug": "xdesk"},
		TrustedHooks: map[string]string{},
		Files: map[string]FileState{
			"main.go": {Src: "main.go", Hash: "sha256:abc", Exec: false, Binary: false},
		},
	}
	if err := s.Save(dir); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Template.Commit != s.Template.Commit {
		t.Errorf("Commit = %q", got.Template.Commit)
	}
	if got.Vars["app_slug"] != "xdesk" {
		t.Errorf("Vars = %+v", got.Vars)
	}
	if got.Files["main.go"].Hash != "sha256:abc" {
		t.Errorf("Files = %+v", got.Files)
	}
}

func TestSaveIsDeterministic(t *testing.T) {
	// JSON 的 map 序列化必须稳定，否则每次 update 都会产生无意义的 diff
	dir := t.TempDir()
	s := &State{
		Version: CurrentVersion,
		Vars:    map[string]string{"z": "1", "a": "2", "m": "3"},
		Files: map[string]FileState{
			"z.go": {Src: "z.go", Hash: "h1"},
			"a.go": {Src: "a.go", Hash: "h2"},
		},
	}
	var first string
	for i := 0; i < 10; i++ {
		if err := s.Save(dir); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(Path(dir))
		if err != nil {
			t.Fatal(err)
		}
		if first == "" {
			first = string(data)
			continue
		}
		if string(data) != first {
			t.Fatalf("state.json 序列化不稳定:\n%s\n---\n%s", first, data)
		}
	}
}

func TestLoadMissing(t *testing.T) {
	_, err := Load(t.TempDir())
	if err == nil {
		t.Fatal("状态缺失应报错，不能猜")
	}
	if !os.IsNotExist(err) {
		// 允许包装，但错误里要能看出是"不存在"（errors.Is 会逐层解包）
		if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("应由 os.IsNotExist / fs.ErrNotExist 判定为不存在: %v", err)
		}
	}
}

func TestPathIsDotXinitStateJSON(t *testing.T) {
	// 落盘位置是跨任务契约：Task 18/19 与 init 的 .gitignore 处理都按
	// "<项目根>/.xinit/state.json" 读写。DirName 是常量，若有人改了它的
	// 值，所有消费方会一致地跟着漂移，只有这条断言拦得住。
	p := Path("/proj")
	if filepath.Dir(p) != filepath.Join("/proj", ".xinit") || filepath.Base(p) != "state.json" {
		t.Errorf("状态文件位置是契约 .xinit/state.json，Path = %q", p)
	}
}

func TestLoadRejectsFutureVersion(t *testing.T) {
	dir := t.TempDir()
	// hash_algo 填**合法值**：否则 Load 会因为 hash_algo 不匹配而报错，
	// 这条测试就变成「版本检查被删掉也会绿」的空转夹具。
	payload := fmt.Sprintf(`{"version": 99, "hash_algo": %q, "files": {}}`, fsutil.HashAlgo)
	p := Path(dir)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(dir)
	if err == nil {
		t.Fatal("状态版本高于本工具支持时应报错")
	}
	if os.IsNotExist(err) {
		// 前提守卫：文件没写进去时 err 也非 nil，测试会空转通过
		t.Fatalf("前提不成立：%s 未写入，Load 失败原因不是版本检查: %v", p, err)
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	// 除未知字段外，其余字段全部合法（尤其 hash_algo）：否则 DisallowUnknownFields
	// 被删掉后 Load 仍会因 hash_algo 不匹配报错，测试空转通过。
	payload := fmt.Sprintf(`{"version": 1, "hash_algo": %q, "files": {}, "nope": 1}`, fsutil.HashAlgo)
	p := Path(dir)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(dir)
	if err == nil {
		t.Fatal("未知字段应报错，避免静默忽略拼写错误")
	}
	if os.IsNotExist(err) {
		// 前提守卫：文件没写进去时 err 也非 nil，测试会空转通过
		t.Fatalf("前提不成立：%s 未写入，Load 失败原因不是未知字段: %v", p, err)
	}
}

func TestWriteFileAtomicPreservesMode(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "run.sh")

	if err := WriteFileAtomic(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("权限 = %v, want 0755 —— CreateTemp 默认是 0600，必须显式 chmod",
			info.Mode().Perm())
	}
}

func TestWriteFileAtomicPreservesReadBits(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.txt")
	if err := WriteFileAtomic(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("权限 = %v, want 0644（group/other 读权限不能丢）", info.Mode().Perm())
	}
}

func TestWriteFileAtomicReplacesContent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.txt")
	if err := WriteFileAtomic(p, []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(p, []byte("second"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "second" {
		t.Errorf("= %q", got)
	}
}

func TestWriteFileAtomicLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.txt")
	for i := 0; i < 5; i++ {
		if err := WriteFileAtomic(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 1 {
		names := []string{}
		for _, e := range ents {
			names = append(names, e.Name())
		}
		t.Errorf("临时文件未清理: %v", names)
	}
}

func TestWriteFileAtomicCreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "deep", "er", "a.txt")
	if err := WriteFileAtomic(p, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Errorf("应自动创建父目录: %v", err)
	}
}

func TestStateJSONShape(t *testing.T) {
	// 字段名是落盘契约，改名会让旧状态无法读取
	s := &State{Version: 1, Files: map[string]FileState{}}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"version"`, `"hash_algo"`, `"engine_version"`, `"template"`, `"files"`} {
		if !strings.Contains(string(data), key) {
			t.Errorf("state.json 缺少字段 %s: %s", key, data)
		}
	}
}
