package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/xbcio/xinit/internal/fsutil"
	"github.com/xbcio/xinit/internal/state"
)

func TestCheckKindStrings(t *testing.T) {
	// 级别词是稳定标识（§18.6）：脚本按它 grep，拼写不可改。
	for k, want := range map[checkKind]string{kindFail: "FAIL", kindWarn: "warn", kindSkip: "skip"} {
		if got := k.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", k, got, want)
		}
	}
}

func TestPrintEmptyReport(t *testing.T) {
	var buf bytes.Buffer
	newCheckReport("https://example.com/t.git").Print(&buf)
	want := "xinit check: https://example.com/t.git\n" +
		"A 状态与 base 树（§4.3 / §5.1 / §5.2）:\n" +
		"B 冲突残留（§10.4）:\n" +
		"C 配置与 git 契约（§3.2 / §5.5）:\n" +
		"D 模板面（§5.2 / §7.6 / §11.4）:\n" +
		"检查汇总: 通过（0 项失败、0 项警告、0 项跳过）\n"
	if got := buf.String(); got != want {
		t.Errorf("空报告输出:\n%s\nwant:\n%s", got, want)
	}
}

func TestPrintFailsSummary(t *testing.T) {
	r := newCheckReport("src")
	l := r.fail(secA, "base/%s 缺失", "a.txt")
	l.Detail = []string{"附加行 x"}
	r.warn(secC, "w")
	r.skip(secD, "s")
	if r.Fails != 1 || r.Warns != 1 || r.Skips != 1 {
		t.Fatalf("计数 = %d/%d/%d", r.Fails, r.Warns, r.Skips)
	}
	var buf bytes.Buffer
	r.Print(&buf)
	want := "xinit check: src\n" +
		"A 状态与 base 树（§4.3 / §5.1 / §5.2）:\n" +
		"  FAIL base/a.txt 缺失\n" +
		"  附加行 x\n" +
		"B 冲突残留（§10.4）:\n" +
		"C 配置与 git 契约（§3.2 / §5.5）:\n" +
		"  warn w\n" +
		"D 模板面（§5.2 / §7.6 / §11.4）:\n" +
		"  skip s\n" +
		"检查汇总: 1 项失败、1 项警告、1 项跳过（退出码 6）\n"
	if got := buf.String(); got != want {
		t.Errorf("输出:\n%s\nwant:\n%s", got, want)
	}
	if !strings.Contains(buf.String(), "（退出码 6）") {
		t.Error("汇总行应带退出码 6")
	}
}

// writeStateAndBase 落盘一个自洽的 .xinit/（state.json + base/），经
// mutate 注入扰动后由 state.Load 真实路径读回。files 的记录位按内容探测，
// 模板字段合法——不 mutate 时 A 组应零行。
func writeStateAndBase(t *testing.T, files map[string][]byte, mutate func(*state.State)) (string, *state.State) {
	t.Helper()
	dir := t.TempDir()
	st := state.New(EngineVersion)
	st.Template = state.TemplateRef{Source: "https://example.com/t.git", Kind: "git", Revision: "v1.0.0", Commit: "abc123"}
	base := state.BaseDir(dir)
	for p, c := range files {
		bin := fsutil.IsBinary(c)
		st.Files[p] = state.FileState{
			Src:      p,
			Hash:     fsutil.CompareHash(c, bin),
			Binary:   bin,
			MixedEOL: fsutil.DetectEOL(c) == fsutil.EOLMixed,
		}
		target := filepath.Join(base, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, c, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if mutate != nil {
		mutate(st)
	}
	if err := st.Save(dir); err != nil {
		t.Fatal(err)
	}
	got, err := state.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return dir, got
}

// runGroupA 跑 A 组并返回 A 组行文本。
func runGroupA(t *testing.T, dir string, st *state.State) (*checkReport, []string) {
	t.Helper()
	r := newCheckReport(st.Template.Source)
	if err := checkGroupA(r, dir, st); err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, l := range r.Sections[secA].Lines {
		if l.Kind != kindFail {
			t.Errorf("A 组只出 FAIL 行，得到 %s: %s", l.Kind, l.Text)
		}
		texts = append(texts, l.Text)
	}
	return r, texts
}

// wantLines 断言恰有 n 行、且每个锚都被某一行包含。
func wantLines(t *testing.T, lines []string, n int, anchors ...string) {
	t.Helper()
	if len(lines) != n {
		t.Errorf("A 组 %d 行，want %d:\n%s", len(lines), n, strings.Join(lines, "\n"))
	}
	for _, a := range anchors {
		if !slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, a) }) {
			t.Errorf("缺少含 %q 的行:\n%s", a, strings.Join(lines, "\n"))
		}
	}
}

func TestGroupAValidKey(t *testing.T) {
	for p, want := range map[string]bool{
		"a/b.txt": true,
		"a.txt":   true,
		"":        false,
		"../x":    false,
		"/x":      false,
		"a/./b":   false,
		"a//b":    false,
		".":       false,
		"a/..":    false,
		"x/":      false,
	} {
		if got := validCheckKey(p); got != want {
			t.Errorf("validCheckKey(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestGroupAClean(t *testing.T) {
	// 含子目录的干净项目：目录不入表，A 组零行（§18.9 验收 4 后半）。
	dir, st := writeStateAndBase(t, map[string][]byte{
		"a.txt":         []byte("a\n"),
		"sub/deep/b.sh": []byte("#!/bin/sh\n"),
	}, nil)
	r, lines := runGroupA(t, dir, st)
	wantLines(t, lines, 0)
	if r.Fails != 0 {
		t.Errorf("Fails = %d", r.Fails)
	}
}

func TestGroupAMissingBaseDir(t *testing.T) {
	// base 目录整棵缺失：不是 IO 错误，每个键报缺失。
	dir, st := writeStateAndBase(t, map[string][]byte{"a.txt": []byte("a\n")}, nil)
	if err := os.RemoveAll(state.BaseDir(dir)); err != nil {
		t.Fatal(err)
	}
	_, lines := runGroupA(t, dir, st)
	wantLines(t, lines, 1, "base/a.txt 缺失（§5.1")
}

func TestGroupAKeyIllegal(t *testing.T) {
	dir, st := writeStateAndBase(t, map[string][]byte{"a.txt": []byte("a\n")}, func(s *state.State) {
		s.Files["../x"] = state.FileState{Src: "x", Hash: "sha256:00"}
	})
	// base/../x 即 .xinit/x：放一个哨兵，hash 与记账不符——若被读到会出「不符」行。
	if err := os.WriteFile(filepath.Join(dir, state.DirName, "x"), []byte("sentinel\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, lines := runGroupA(t, dir, st)
	wantLines(t, lines, 1, `files 键 "../x" 不是规范的相对路径（§4.3）`)
	if r.Fails != 1 {
		t.Errorf("Fails = %d, want 1", r.Fails)
	}
}

func TestGroupAFoldCollision(t *testing.T) {
	dir, st := writeStateAndBase(t, map[string][]byte{"a.txt": []byte("a\n")}, func(s *state.State) {
		s.Files["A.txt"] = s.Files["a.txt"]
	})
	_, lines := runGroupA(t, dir, st)
	// 大小写不敏感文件系统上 base/A.txt 读得到（A4 无行）；敏感文件系统上
	// A4 连带一行缺失——接受（§18.9 验收 6），只断言折叠行存在且只有一条。
	n := 0
	for _, l := range lines {
		if strings.Contains(l, "折叠等价冲突（§9.3）") {
			n++
			if !strings.Contains(l, `"A.txt" 与 "a.txt"`) {
				t.Errorf("折叠行键序: %s", l)
			}
		}
	}
	if n != 1 {
		t.Errorf("折叠行 %d 条, want 1:\n%s", n, strings.Join(lines, "\n"))
	}
}

func TestGroupASrcEmpty(t *testing.T) {
	dir, st := writeStateAndBase(t, map[string][]byte{"a.txt": []byte("a\n")}, func(s *state.State) {
		f := s.Files["a.txt"]
		f.Src = ""
		s.Files["a.txt"] = f
	})
	_, lines := runGroupA(t, dir, st)
	wantLines(t, lines, 1, `files["a.txt"].src 为空（§4.3）`)
}

func TestGroupAMissingAndHashMismatch(t *testing.T) {
	dir, st := writeStateAndBase(t, map[string][]byte{
		"gone.txt": []byte("g\n"),
		"mod.txt":  []byte("m\n"),
	}, nil)
	base := state.BaseDir(dir)
	if err := os.Remove(filepath.Join(base, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "mod.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, lines := runGroupA(t, dir, st)
	wantLines(t, lines, 2,
		"base/gone.txt 缺失（§5.1",
		"base/mod.txt 与记账 hash 不符（got ",
		"want "+st.Files["mod.txt"].Hash)
}

func TestGroupAResidueAndNonRegular(t *testing.T) {
	dir, st := writeStateAndBase(t, map[string][]byte{
		"a.txt":     []byte("a\n"),
		"sub/b.txt": []byte("b\n"),
		"link":      []byte("l\n"),
	}, nil)
	base := state.BaseDir(dir)
	if err := os.WriteFile(filepath.Join(base, "sub", "extra.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 记账内的键 link 在 base 里是符号链接：只报 A6，不报 A4 缺失。
	if err := os.Remove(filepath.Join(base, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a.txt", filepath.Join(base, "link")); err != nil {
		t.Fatal(err)
	}
	_, lines := runGroupA(t, dir, st)
	wantLines(t, lines, 2,
		"base/sub/extra.txt 不在记账中（残片",
		"base/link 不是普通文件（L")
}

func TestGroupABitMismatch(t *testing.T) {
	dir, st := writeStateAndBase(t, map[string][]byte{
		"bin.dat":  []byte("a\x00b"),
		"mixed.md": []byte("a\r\nb\n"),
		"run.sh":   []byte("#!/bin/sh\n"),
	}, func(s *state.State) {
		b := s.Files["bin.dat"]
		b.Binary = false // hash 用记录位口径重算，避免 A4 连带
		b.Hash = fsutil.CompareHash([]byte("a\x00b"), false)
		s.Files["bin.dat"] = b
		m := s.Files["mixed.md"]
		m.MixedEOL = false
		s.Files["mixed.md"] = m
		r := s.Files["run.sh"]
		r.Exec = true // base 写的是 0644
		s.Files["run.sh"] = r
	})
	_, lines := runGroupA(t, dir, st)
	wantLines(t, lines, 3,
		`files["bin.dat"].binary=false 但 base 内容是 true`,
		`files["mixed.md"].mixed_eol=false 但 base 内容是 true`,
		`files["run.sh"].exec=true 但 base 文件无可执行位`)
}

func TestGroupAExecBitFromMode(t *testing.T) {
	// 反向：记录 exec=false，base 是 0755 → 报「有可执行位」。
	dir, st := writeStateAndBase(t, map[string][]byte{"run.sh": []byte("#!/bin/sh\n")}, nil)
	if err := os.Chmod(filepath.Join(state.BaseDir(dir), "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, lines := runGroupA(t, dir, st)
	wantLines(t, lines, 1, `files["run.sh"].exec=false 但 base 文件有可执行位`)
}

func TestGroupATemplateFields(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*state.State)
		anchor string
	}{
		{"kind", func(s *state.State) { s.Template.Kind = "foo" }, `template.kind "foo" 非法（§4.2）`},
		{"source", func(s *state.State) { s.Template.Source = "" }, "template.source 为空（§4.2）"},
		{"commit", func(s *state.State) { s.Template.Commit = "" }, "template.commit 为空（§4.2）"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir, st := writeStateAndBase(t, map[string][]byte{"a.txt": []byte("a\n")}, c.mutate)
			_, lines := runGroupA(t, dir, st)
			wantLines(t, lines, 1, c.anchor)
		})
	}
	// dir 也是合法 kind。
	dir, st := writeStateAndBase(t, nil, func(s *state.State) { s.Template.Kind = "dir" })
	_, lines := runGroupA(t, dir, st)
	wantLines(t, lines, 0)
}
