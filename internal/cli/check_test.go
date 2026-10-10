package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
		"  FAIL  base/a.txt 缺失\n" +
		"  附加行 x\n" +
		"B 冲突残留（§10.4）:\n" +
		"C 配置与 git 契约（§3.2 / §5.5）:\n" +
		"  warn  w\n" +
		"D 模板面（§5.2 / §7.6 / §11.4）:\n" +
		"  skip  s\n" +
		"检查汇总: 1 项失败、1 项警告、1 项跳过（退出码 6）\n"
	if got := buf.String(); got != want {
		t.Errorf("输出:\n%s\nwant:\n%s", got, want)
	}
}

func TestCheckLinePointerStable(t *testing.T) {
	// 回归（T1 评审 Important-1）：同组先追加两行、再给第一行挂 Detail——
	// 切片扩容不得让早先返回的行指针失效。
	r := newCheckReport("src")
	first := r.fail(secB, "one")
	for i := 0; i < 8; i++ {
		r.fail(secB, "more %d", i)
	}
	first.Detail = []string{"detail-of-one"}
	var buf bytes.Buffer
	r.Print(&buf)
	if !strings.Contains(buf.String(), "  FAIL  one\n  detail-of-one\n") {
		t.Errorf("第一行的 Detail 丢失:\n%s", buf.String())
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
	// base 目录恒建（空模板也有 base 根）：根缺失是单独的注入场景。
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
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
		t.Errorf("行数 %d 不符，want %d:\n%s", len(lines), n, strings.Join(lines, "\n"))
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
		"./a":     false,
		"a/b/":    false,
		"a/../b":  false,
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
	// base 目录整棵缺失：一条根因行，不逐键报缺失（§18.2 A4 注）。
	dir, st := writeStateAndBase(t, map[string][]byte{
		"a.txt": []byte("a\n"),
		"b.txt": []byte("b\n"),
	}, nil)
	if err := os.RemoveAll(state.BaseDir(dir)); err != nil {
		t.Fatal(err)
	}
	_, lines := runGroupA(t, dir, st)
	wantLines(t, lines, 1, "base 树不可用：.xinit/base 不存在（全部记账路径按缺失处理，§5.1）")
}

func TestGroupABaseRootSymlink(t *testing.T) {
	// base 根被换成指向真实目录的符号链接：一条根因行（含「不是目录」），
	// 无逐键缺失；A1–A3 与 A10 照常。
	dir, st := writeStateAndBase(t, map[string][]byte{"a.txt": []byte("a\n")}, func(s *state.State) {
		s.Template.Commit = ""
	})
	base := state.BaseDir(dir)
	real := filepath.Join(dir, "real-base")
	if err := os.Rename(base, real); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, base); err != nil {
		t.Fatal(err)
	}
	_, lines := runGroupA(t, dir, st)
	wantLines(t, lines, 2,
		"base 树不可用：.xinit/base 不是目录（symlink 等",
		"template.commit 为空（§4.2）")
	for _, l := range lines {
		if strings.Contains(l, "缺失（§5.1") {
			t.Errorf("根因已报，不应逐键报缺失: %s", l)
		}
	}
}

func TestGroupAResidueIgnoresIllegalKey(t *testing.T) {
	// A5 只收合法键：非法键 a/./b.txt 不能让 base 中的 a/b.txt 逃过残片判定。
	dir, st := writeStateAndBase(t, nil, func(s *state.State) {
		s.Files["a/./b.txt"] = state.FileState{Src: "b.txt", Hash: "sha256:00"}
	})
	base := state.BaseDir(dir)
	if err := os.MkdirAll(filepath.Join(base, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "a", "b.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, lines := runGroupA(t, dir, st)
	wantLines(t, lines, 2,
		`files 键 "a/./b.txt" 不是规范的相对路径（§4.3）`,
		"base/a/b.txt 不在记账中（残片")
}

func TestGroupAEscapesControlChars(t *testing.T) {
	// 合法键含换行（通过 A1：不含坏段）：base/%s 形态的消息不得裂行。
	dir, st := writeStateAndBase(t, map[string][]byte{"ok.txt": []byte("o\n")}, func(s *state.State) {
		s.Files["x\ny"] = state.FileState{Src: "x", Hash: "sha256:00"}
	})
	r, lines := runGroupA(t, dir, st)
	wantLines(t, lines, 1, `base/"x\ny" 缺失（§5.1`)
	var buf bytes.Buffer
	r.Print(&buf)
	for _, l := range strings.Split(buf.String(), "\n") {
		if strings.HasPrefix(l, "y ") {
			t.Errorf("输出裂行:\n%s", buf.String())
		}
	}
	if got := checkEscapePath("a/b.txt"); got != "a/b.txt" {
		t.Errorf("普通路径应原样: %q", got)
	}
	if got := checkEscapePath("a\x7fb"); got != `"a\x7fb"` {
		t.Errorf("DEL 应转义: %q", got)
	}
}

func TestGroupANonRegularFIFO(t *testing.T) {
	// A6 不止符号链接：FIFO 同样报非普通文件。用 mkfifo 命令而非
	// syscall.Mkfifo——后者在 windows 上不存在，会让本文件在该平台编译失败。
	if runtime.GOOS == "windows" {
		t.Skip("windows 无 FIFO")
	}
	mkfifo, err := exec.LookPath("mkfifo")
	if err != nil {
		t.Skip("无 mkfifo 命令")
	}
	dir, st := writeStateAndBase(t, map[string][]byte{"a.txt": []byte("a\n")}, nil)
	if out, err := exec.Command(mkfifo, filepath.Join(state.BaseDir(dir), "pipe")).CombinedOutput(); err != nil {
		t.Fatalf("mkfifo: %v: %s", err, out)
	}
	_, lines := runGroupA(t, dir, st)
	wantLines(t, lines, 1, "base/pipe 不是普通文件（p")
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
	// A4 按真实文件名查表（base 里只有 a.txt），注入场景必连带一行
	// base/A.txt 缺失——与文件系统大小写敏感性无关（§18.9 验收 6 接受）。
	wantLines(t, lines, 2,
		`"A.txt" 与 "a.txt" 折叠等价冲突（§9.3）`,
		"base/A.txt 缺失（§5.1")
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

func TestGroupABaseRootAbnormalKeepsA1(t *testing.T) {
	// base 根异常只跳过 A4–A9（§18.2）：A1 非法键照常报（T1 复审 parked）。
	dir, st := writeStateAndBase(t, map[string][]byte{"a.txt": []byte("a\n")}, func(s *state.State) {
		s.Files["../x"] = state.FileState{Src: "x", Hash: "sha256:00"}
	})
	base := state.BaseDir(dir)
	real := filepath.Join(dir, "real-base")
	if err := os.Rename(base, real); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, base); err != nil {
		t.Fatal(err)
	}
	_, lines := runGroupA(t, dir, st)
	wantLines(t, lines, 2,
		`files 键 "../x" 不是规范的相对路径（§4.3）`,
		"base 树不可用：.xinit/base 不是目录（symlink 等")
}

// runGroup 跑一个组函数并返回该组的行。
func runGroup(t *testing.T, fn func(*checkReport, string, *state.State) error, sec int, dir string, st *state.State) (*checkReport, []*checkLine) {
	t.Helper()
	r := newCheckReport(st.Template.Source)
	if err := fn(r, dir, st); err != nil {
		t.Fatal(err)
	}
	return r, r.Sections[sec].Lines
}

// lineTexts 把行渲染为「级别词 文本」，便于锚断言。
func lineTexts(lines []*checkLine) []string {
	var out []string
	for _, l := range lines {
		out = append(out, l.Kind.String()+" "+l.Text)
	}
	return out
}

func TestGroupBNoConflicts(t *testing.T) {
	dir, st := writeStateAndBase(t, map[string][]byte{"a.txt": []byte("a\n")}, nil)
	r, lines := runGroup(t, checkGroupB, secB, dir, st)
	if len(lines) != 0 || r.Fails != 0 {
		t.Errorf("B 组应无行: %v (Fails=%d)", lineTexts(lines), r.Fails)
	}
}

func TestGroupBConflicts(t *testing.T) {
	dir, st := writeStateAndBase(t, map[string][]byte{"a.txt": []byte("a\n")}, nil)
	writeFilesT(t, filepath.Join(dir, state.DirName, "conflicts"), map[string]string{
		"a.txt":      "theirs\n",
		"orphan.txt": "x\n",
	})
	r, lines := runGroup(t, checkGroupB, secB, dir, st)
	if len(lines) != 1 {
		t.Fatalf("B 组应恰一行: %v", lineTexts(lines))
	}
	l := lines[0]
	if l.Kind != kindFail || l.Text != "未解决的冲突（2 条，§10.4）：" {
		t.Errorf("节头 = %s %q", l.Kind, l.Text)
	}
	if r.Fails != 1 {
		t.Errorf("Fails = %d, want 1", r.Fails)
	}
	want := []string{
		"conflict             a.txt",
		"stale                orphan.txt（孤立条目，下轮 update 将清理）",
		"出路一：编辑 .xinit/conflicts/<路径> 手工解决后，运行 xinit update --accept <路径> 声明已解决（§10.3）。",
		"出路二：本轮已无事可做时直接运行 xinit update——陈旧条目随本轮自动清理（§10.2 第 8 步）。",
	}
	if !slices.Equal(l.Detail, want) {
		t.Errorf("Detail:\n%s\nwant:\n%s", strings.Join(l.Detail, "\n"), strings.Join(want, "\n"))
	}
}

func TestGroupCPruneBadGlob(t *testing.T) {
	dir, st := writeStateAndBase(t, nil, nil)
	// 两个非法条目夹一个合法条目：逐条定位，每条非法各报一行（§18.4）。
	writeFilesT(t, dir, map[string]string{".xinit/prune.txt": "# c\na/[\nok/*.txt\nb/[\n"})
	r, lines := runGroup(t, checkGroupC, secC, dir, st)
	texts := lineTexts(lines)
	for _, bad := range []string{"a/[", "b/["} {
		if !slices.ContainsFunc(texts, func(s string) bool {
			return strings.HasPrefix(s, "FAIL ") && strings.Contains(s, fmt.Sprintf("prune.txt 条目 %q", bad)) &&
				strings.Contains(s, "不是合法 glob") && strings.Contains(s, "§3.2")
		}) {
			t.Errorf("缺 %q 的 C1 FAIL 行: %v", bad, texts)
		}
	}
	if r.Fails != 2 {
		t.Errorf("Fails = %d, want 2（逐条报、合法条目不报）", r.Fails)
	}
}

func TestGroupCPruneOK(t *testing.T) {
	dir, st := writeStateAndBase(t, nil, nil)
	writeFilesT(t, dir, map[string]string{".xinit/prune.txt": "docs/**\n!docs/keep.md\nREADME.md\n"})
	runGitT(t, dir, "init", "-q")
	writeFilesT(t, dir, map[string]string{".git/info/exclude": ".xinit/\n"})
	_, lines := runGroup(t, checkGroupC, secC, dir, st)
	if len(lines) != 0 {
		t.Errorf("C 组应无行: %v", lineTexts(lines))
	}
}

func TestGroupCTracked(t *testing.T) {
	dir, st := writeStateAndBase(t, nil, nil)
	runGitT(t, dir, "init", "-q")
	runGitT(t, dir, "add", "-f", ".xinit/state.json")
	r, lines := runGroup(t, checkGroupC, secC, dir, st)
	texts := lineTexts(lines)
	if !slices.Contains(texts, "FAIL .xinit/ 已被 git 跟踪：状态目录不应进入版本历史（§5.5）；请 git rm -r --cached .xinit/ 后提交") {
		t.Errorf("缺 C2 FAIL 行: %v", texts)
	}
	if r.Fails != 1 {
		t.Errorf("Fails = %d, want 1", r.Fails)
	}
}

func TestGroupCExcludeMissing(t *testing.T) {
	// present：git init 按默认模板建出 exclude、但无 .xinit/ 行；
	// absent：exclude 文件不存在（init.templateDir 为空 / 用户删过）——NotExist 容忍。
	for _, c := range []struct {
		name   string
		remove bool
	}{{"present", false}, {"absent", true}} {
		t.Run(c.name, func(t *testing.T) {
			dir, st := writeStateAndBase(t, nil, nil)
			runGitT(t, dir, "init", "-q")
			if c.remove {
				p, ok := state.GitExcludePath(dir)
				if !ok {
					t.Fatal("GitExcludePath 定位失败")
				}
				if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
			}
			r, lines := runGroup(t, checkGroupC, secC, dir, st)
			texts := lineTexts(lines)
			want := "warn .git/info/exclude 缺少 .xinit/ 行（init/update 会自动补写；补上可避免 git add -A 误纳状态目录，§5.5）"
			if !slices.Equal(texts, []string{want}) {
				t.Errorf("C 组 = %v, want [%s]", texts, want)
			}
			if r.Fails != 0 {
				t.Errorf("Fails = %d, want 0", r.Fails)
			}
		})
	}
}

func TestGroupCExcludeOK(t *testing.T) {
	dir, st := writeStateAndBase(t, nil, nil)
	runGitT(t, dir, "init", "-q")
	// 带首尾空白也命中（TrimSpace 口径同 EnsureGitExcluded）。
	writeFilesT(t, dir, map[string]string{".git/info/exclude": "# x\n  .xinit/  \n"})
	_, lines := runGroup(t, checkGroupC, secC, dir, st)
	if len(lines) != 0 {
		t.Errorf("C 组应无行: %v", lineTexts(lines))
	}
}

func TestGroupCNotGit(t *testing.T) {
	dir, st := writeStateAndBase(t, nil, nil)
	r, lines := runGroup(t, checkGroupC, secC, dir, st)
	texts := lineTexts(lines)
	want := "skip 无法定位 git 仓库：跳过 .xinit/ 跟踪与 exclude 检查（§5.5）"
	if !slices.Equal(texts, []string{want}) {
		t.Errorf("C 组 = %v, want [%s]", texts, want)
	}
	if r.Skips != 1 || r.Fails != 0 {
		t.Errorf("Skips/Fails = %d/%d", r.Skips, r.Fails)
	}
}
