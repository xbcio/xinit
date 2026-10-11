package backflow

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// 测试用 blob sha（40 位十六进制）。
const (
	shaA    = "1111111111111111111111111111111111111111"
	shaB    = "2222222222222222222222222222222222222222"
	shaZero = "0000000000000000000000000000000000000000"
)

// rawZ 拼接 `git diff-tree --raw -z` 的字节流：段间 NUL 连接、末尾一个 NUL。
func rawZ(segs ...string) []byte { return []byte(strings.Join(segs, "\x00") + "\x00") }

func TestParseRawChangesShapes(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want []Change
	}{
		{
			name: "普通修改 M",
			in:   rawZ(":100644 100644 "+shaA+" "+shaB+" M", "a.txt"),
			want: []Change{{Status: "M", OldMode: "100644", NewMode: "100644", OldSha: shaA, NewSha: shaB, Path: "a.txt"}},
		},
		{
			name: "新增 A",
			in:   rawZ(":000000 100644 "+shaZero+" "+shaA+" A", "new.txt"),
			want: []Change{{Status: "A", OldMode: "000000", NewMode: "100644", OldSha: shaZero, NewSha: shaA, Path: "new.txt"}},
		},
		{
			name: "纯 mode 变更（old/new sha 相同）",
			in:   rawZ(":100644 100755 "+shaA+" "+shaA+" M", "x.sh"),
			want: []Change{{Status: "M", OldMode: "100644", NewMode: "100755", OldSha: shaA, NewSha: shaA, Path: "x.sh"}},
		},
		{
			name: "改名 R100（旧路径在前、新路径在后）",
			in:   rawZ(":100644 100644 "+shaA+" "+shaB+" R100", "old.txt", "new.txt"),
			want: []Change{{Status: "R100", OldMode: "100644", NewMode: "100644", OldSha: shaA, NewSha: shaB, Path: "new.txt", OldPath: "old.txt"}},
		},
		{
			name: "拷贝 C75",
			in:   rawZ(":100644 100644 "+shaA+" "+shaB+" C75", "src.txt", "dst.txt"),
			want: []Change{{Status: "C75", OldMode: "100644", NewMode: "100644", OldSha: shaA, NewSha: shaB, Path: "dst.txt", OldPath: "src.txt"}},
		},
		{
			name: "类型变更 T",
			in:   rawZ(":100644 120000 "+shaA+" "+shaB+" T", "link"),
			want: []Change{{Status: "T", OldMode: "100644", NewMode: "120000", OldSha: shaA, NewSha: shaB, Path: "link"}},
		},
		{
			name: "多记录混合",
			in: rawZ(
				":100644 100644 "+shaA+" "+shaB+" M", "a.txt",
				":000000 100755 "+shaZero+" "+shaA+" A", "run.sh",
				":100644 100644 "+shaA+" "+shaB+" R100", "old.txt", "new.txt",
			),
			want: []Change{
				{Status: "M", OldMode: "100644", NewMode: "100644", OldSha: shaA, NewSha: shaB, Path: "a.txt"},
				{Status: "A", OldMode: "000000", NewMode: "100755", OldSha: shaZero, NewSha: shaA, Path: "run.sh"},
				{Status: "R100", OldMode: "100644", NewMode: "100644", OldSha: shaA, NewSha: shaB, Path: "new.txt", OldPath: "old.txt"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseRawChanges(tc.in)
			if err != nil {
				t.Fatalf("解析应成功：%v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("解析结果不符；got %+v, want %+v", got, tc.want)
			}
		})
	}
}

// A+D 同 hash 兜底：两条记录合并为一条 R100（与真实 R 记录同构）。
func TestParseRawChangesAddedDeletedSameHash(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
	}{
		{
			name: "真实拆形（A 的 new sha == D 的 old sha）",
			in: rawZ(
				":000000 100644 "+shaZero+" "+shaA+" A", "new.txt",
				":100644 000000 "+shaA+" "+shaZero+" D", "old.txt",
			),
		},
		{
			name: "四槽同 hash 构造形（控制器裁定用例）",
			in: rawZ(
				":100644 100644 "+shaA+" "+shaA+" A", "new.txt",
				":100644 100644 "+shaA+" "+shaA+" D", "old.txt",
			),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseRawChanges(tc.in)
			if err != nil {
				t.Fatalf("解析应成功：%v", err)
			}
			if len(got) != 1 {
				t.Fatalf("A+D 同 hash 应合并为 1 条；got %d 条：%+v", len(got), got)
			}
			if got[0].Status != "R100" {
				t.Errorf("Status 应为 R100；got %q", got[0].Status)
			}
			if got[0].Path != "new.txt" {
				t.Errorf("Path 应取新路径；got %q", got[0].Path)
			}
			if got[0].OldPath != "old.txt" {
				t.Errorf("OldPath 应取旧路径；got %q", got[0].OldPath)
			}
			if got[0].NewSha != shaA || got[0].OldSha != shaA {
				t.Errorf("old/new sha 应保留；got old=%q new=%q", got[0].OldSha, got[0].NewSha)
			}
		})
	}
}

// 不同 hash 的 A 与 D 不得被误并（负对照）。
func TestParseRawChangesAddedDeletedDifferentHashKept(t *testing.T) {
	got, err := parseRawChanges(rawZ(
		":000000 100644 "+shaZero+" "+shaA+" A", "new.txt",
		":100644 000000 "+shaB+" "+shaZero+" D", "old.txt",
	))
	if err != nil {
		t.Fatalf("解析应成功：%v", err)
	}
	if len(got) != 2 {
		t.Fatalf("不同 hash 的 A/D 应各留一条；got %+v", got)
	}
	if got[0].Status != "A" || got[1].Status != "D" {
		t.Fatalf("status 应保持原样；got %q, %q", got[0].Status, got[1].Status)
	}
}

func TestParseRawChangesMalformed(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
	}{
		{"空输入", nil},
		{"仅一个 NUL", []byte("\x00")},
		{"头段字段数不是 5", rawZ(":100644 100644 M", "a.txt")},
		{"头段缺冒号", rawZ("100644 100644 "+shaA+" "+shaB+" M", "a.txt")},
		{"缺路径段", rawZ(":100644 100644 " + shaA + " " + shaB + " M")},
		{"R 记录只有一条路径", rawZ(":100644 100644 "+shaA+" "+shaB+" R100", "only.txt")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := parseRawChanges(tc.in); err == nil {
				t.Fatalf("畸形输入应报错；got %+v", got)
			}
		})
	}
}

// 真仓：改名 + 小改（相似度 >50%）应只出一条 R*（R3 判据：不会被拆成 A+D）。
func TestEnumerateChangesRenameWithEdit(t *testing.T) {
	body := "l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nl9\nl10\n"
	dir := mkRepoT(t, map[string]string{"old.txt": body})
	runGitT(t, dir, "mv", "old.txt", "new.txt")
	writeFileT(t, dir, "new.txt", "l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nl9\nCHANGED\n")
	sha := commitAllT(t, dir, okMsg)

	got, err := EnumerateChanges(dir, sha)
	if err != nil {
		t.Fatalf("枚举应成功：%v", err)
	}
	if len(got) != 1 {
		t.Fatalf("改名 + 小改应只出一条记录；got %+v", got)
	}
	if !strings.HasPrefix(got[0].Status, "R") {
		t.Fatalf("应判为改名（R*）；got %q", got[0].Status)
	}
	if got[0].Path != "new.txt" || got[0].OldPath != "old.txt" {
		t.Fatalf("路径应为 new.txt ← old.txt；got %q ← %q", got[0].Path, got[0].OldPath)
	}
}

// 真仓：空提交（--allow-empty）→ 拒绝。
func TestEnumerateChangesEmptyCommit(t *testing.T) {
	dir := mkRepoT(t, map[string]string{"a.txt": "1\n"})
	runGitT(t, dir, "commit", "-q", "--allow-empty", "-m", okMsg)
	sha := runGitT(t, dir, "rev-parse", "HEAD")

	if got, err := EnumerateChanges(dir, sha); err == nil {
		t.Fatalf("空提交应报错；got %+v", got)
	}
}

// 真仓：新增可执行文件 → A 记录带 100755（exec 位是 §8.3 一等公民）。
func TestEnumerateChangesAddExecutable(t *testing.T) {
	dir := mkRepoT(t, map[string]string{"a.txt": "1\n"})
	writeFileT(t, dir, "run.sh", "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(dir, "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	sha := commitAllT(t, dir, okMsg)

	got, err := EnumerateChanges(dir, sha)
	if err != nil {
		t.Fatalf("枚举应成功：%v", err)
	}
	if len(got) != 1 {
		t.Fatalf("应只出一条记录；got %+v", got)
	}
	if got[0].Status != "A" || got[0].NewMode != "100755" {
		t.Fatalf("新可执行文件应为 A/100755；got %q/%q", got[0].Status, got[0].NewMode)
	}
	if got[0].OldMode != "000000" || got[0].OldSha != shaZero {
		t.Fatalf("新增记录的 old 侧应为零值；got mode=%q sha=%q", got[0].OldMode, got[0].OldSha)
	}
}

// 真仓：纯 mode 变更（既有文件 chmod +x）→ M 记录、old/new sha 相同、mode 不同。
func TestEnumerateChangesModeOnly(t *testing.T) {
	dir := mkRepoT(t, map[string]string{"x.sh": "#!/bin/sh\n"})
	if err := os.Chmod(filepath.Join(dir, "x.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	sha := commitAllT(t, dir, okMsg)

	got, err := EnumerateChanges(dir, sha)
	if err != nil {
		t.Fatalf("枚举应成功：%v", err)
	}
	if len(got) != 1 {
		t.Fatalf("应只出一条记录；got %+v", got)
	}
	if got[0].Status != "M" || got[0].OldMode != "100644" || got[0].NewMode != "100755" {
		t.Fatalf("纯 mode 变更应为 M/100644→100755；got %q %q→%q", got[0].Status, got[0].OldMode, got[0].NewMode)
	}
	if got[0].OldSha == shaZero || got[0].OldSha != got[0].NewSha {
		t.Fatalf("纯 mode 变更两侧 blob 应相同且非零；got old=%q new=%q", got[0].OldSha, got[0].NewSha)
	}
}

// 真仓：root 提交 → 守卫 4 先行拒绝（`<c>^` 不存在，错误须点名判据原文）。
func TestEnumerateChangesRootCommitRejected(t *testing.T) {
	dir := mkRepoT(t, map[string]string{"a.txt": "1\n"})
	root := runGitT(t, dir, "rev-list", "--max-parents=0", "HEAD")

	_, err := EnumerateChanges(dir, root)
	if err == nil {
		t.Fatal("root 提交应被拒绝")
	}
	if !strings.Contains(err.Error(), "rev-list --parents -n 1") {
		t.Fatalf("错误应打印守卫 4 判据原文；got %q", err)
	}
}
