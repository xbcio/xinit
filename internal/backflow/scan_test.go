package backflow

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"

	"github.com/xbcio/xinit/internal/render"
)

// ---------------- BuildTokens ----------------

func TestBuildTokensUnionSubstAndRename(t *testing.T) {
	c := mkCompiledT(t, `manifest_version: 1
vars:
  - name: pkg
  - name: name
substitute:
  - from: PKG
    to: "{{ .pkg }}"
    paths: ["**"]
  - from: NAME
    to: "{{ .name }}"
    paths: ["**"]
rename:
  - from: PKG
    to: "{{ .pkg }}"
`)
	tokens, dropped, vacuous := BuildTokens(c, render.Vars{"pkg": "acme", "name": "widget"})
	want := []string{"acme", "widget"}
	if len(dropped) != 0 {
		t.Fatalf("dropped = %q, want 空", dropped)
	}
	if vacuous {
		t.Fatal("token 非空时 vacuous 必须为 false")
	}
	if strings.Join(tokens, "|") != strings.Join(want, "|") {
		t.Fatalf("tokens = %q, want %q（subst 声明序在前、rename 在后）", tokens, want)
	}
}

func TestBuildTokensLiteralAndDedup(t *testing.T) {
	c := mkCompiledT(t, `manifest_version: 1
vars:
  - name: pkg
substitute:
  - from: PKG
    to: "{{ .pkg }}"
    paths: ["**"]
  - from: PKG2
    to: "Acme"
    paths: ["**"]
rename:
  - from: PKG
    to: "{{ .pkg }}"
`)
	tokens, _, _ := BuildTokens(c, render.Vars{"pkg": "acme"})
	// 匹配大小写不敏感 ⇒ token 大小写不敏感去重，保留首个拼写（'Acme' 与 'acme' 等价）。
	// 字面 to（无 {{ }}）原样进集。
	if len(tokens) != 1 || tokens[0] != "acme" {
		t.Fatalf("tokens = %q, want [acme]（大小写不敏感去重、保留首个拼写）", tokens)
	}
}

func TestBuildTokensDropsEmptyRender(t *testing.T) {
	c := mkCompiledT(t, `manifest_version: 1
vars:
  - name: opt
  - name: pkg
substitute:
  - from: OPT
    to: "{{ .opt }}"
    paths: ["**"]
  - from: PKG
    to: "{{ .pkg }}"
    paths: ["**"]
rename:
  - from: SUF
    to: "{{ .opt }}"
`)
	tokens, dropped, vacuous := BuildTokens(c, render.Vars{"opt": "", "pkg": "acme"})
	if len(tokens) != 1 || tokens[0] != "acme" {
		t.Fatalf("tokens = %q, want [acme]（空 token 必须剔除）", tokens)
	}
	// 空 token 进 | 连接等价「匹配一切」——必须逐条点名剔除的规则。
	if len(dropped) != 2 {
		t.Fatalf("dropped = %q, want 2 条（substitute[0].to 与 rename[0].to）", dropped)
	}
	if !strings.Contains(dropped[0], "substitute[0].to") || !strings.Contains(dropped[1], "rename[0].to") {
		t.Fatalf("dropped = %q, want 逐条点名规则", dropped)
	}
	if vacuous {
		t.Fatal("仍有非空 token 时 vacuous 必须为 false")
	}
}

func TestBuildTokensRenderFailureIsDropped(t *testing.T) {
	c := mkCompiledT(t, `manifest_version: 1
vars:
  - name: pkg
substitute:
  - from: PKG
    to: "{{ .missing }}"
    paths: ["**"]
`)
	tokens, dropped, vacuous := BuildTokens(c, render.Vars{"pkg": "acme"})
	if len(tokens) != 0 || !vacuous {
		t.Fatalf("tokens = %q vacuous = %v, want 空集 + vacuous", tokens, vacuous)
	}
	if len(dropped) != 1 || !strings.Contains(dropped[0], "substitute[0].to") ||
		!strings.Contains(dropped[0], "渲染失败") {
		t.Fatalf("dropped = %q, want 一条点名 substitute[0].to 的渲染失败", dropped)
	}
}

func TestBuildTokensVacuousOnNoRules(t *testing.T) {
	c := mkCompiledT(t, "manifest_version: 1\n")
	tokens, dropped, vacuous := BuildTokens(c, render.Vars{})
	if len(tokens) != 0 || len(dropped) != 0 {
		t.Fatalf("tokens = %q dropped = %q, want 双空", tokens, dropped)
	}
	if !vacuous {
		t.Fatal("无规则 ⇒ 空集 ⇒ vacuous（显式空表行由报告层打印）")
	}
}

// TestBuildTokensIgnoresPathVariableValues 钉住「路径变量的值不进内容扫描」：
// token 集只来自 subst/rename 的 to 渲染值，不枚举 state.Vars 的值本身
// （否则 `src` 这类通用词会把整个仓库判成命中）。
func TestBuildTokensIgnoresPathVariableValues(t *testing.T) {
	c := mkCompiledT(t, `manifest_version: 1
vars:
  - name: src
    default: src
  - name: dst
    default: internal
  - name: pkg
substitute:
  - from: PKG
    to: "{{ .pkg }}"
    paths: ["**"]
rename:
  - from: PKGDIR
    to: "{{ .pkg }}"
`)
	tokens, _, _ := BuildTokens(c, render.Vars{"src": "src", "dst": "internal", "pkg": "acme"})
	for _, tok := range tokens {
		if tok == "src" || tok == "internal" {
			t.Fatalf("tokens = %q：路径变量的值不得进内容扫描", tokens)
		}
	}
	if len(tokens) != 1 || tokens[0] != "acme" {
		t.Fatalf("tokens = %q, want [acme]", tokens)
	}
}

// ---------------- Scan：边界判据 ----------------

// TestScanBoundaryMatrix 固化边界判据 `(?<![A-Za-z])(?:tok)(?![A-Za-z])` 的
// 字节级等价实现（大小写不敏感、ASCII）。下划线/数字邻居两条是「禁 \b 近似」的
// 判别用例：\b 会把它们判成非边界，`[A-Za-z]` 判据命中。
func TestScanBoundaryMatrix(t *testing.T) {
	cases := []struct {
		name  string
		token string
		text  string
		want  bool
	}{
		{"space-delimited", "foo", " foo ", true},
		{"parens", "foo", "(foo)", true},
		{"line-start", "foo", "foo bar", true},
		{"line-end", "foo", "bar foo", true},
		{"whole-line", "foo", "foo", true},
		{"prefix-letter", "foo", "foobar", false},
		{"suffix-letter", "foo", "xfoo", false},
		{"both-side-letters", "foo", "xfoox", false},
		{"doubled-no-boundary", "foo", "foofoo", false},
		{"uppercase-text", "foo", "FOO()", true},
		{"mixed-case-text", "foo", "Foo", true},
		{"uppercase-token", "FOO", "foo", true},
		{"underscore-neighbor", "foo", "foo_bar", true},
		{"digit-neighbor", "foo", "foo1", true},
		{"hyphen-neighbor", "foo", "acme-foo-bar", true},
		{"dot-neighbor", "foo", "x.foo", true},
		{"slash-neighbor", "foo", "a/foo/b", true},
		{"non-ascii-neighbor", "foo", "éfoo", true},
		{"token-longer-than-text", "foo", "fo", false},
		{"empty-token", "", "anything", false},
		{"token-with-nonletter", "a.b", "xa.by", false},
		{"token-with-nonletter-hit", "a.b", "(a.b)", true},
		{"token-casefolded-match", "foobar", "FOOBAR", true},
		{"token-prefix-of-ident", "pkg", "x_pkg_y", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hits := Scan([]AddedLine{{File: "internal/app.go", Line: 7, Text: tc.text}},
				nil, []string{tc.token})
			if tc.want {
				if len(hits) != 1 {
					t.Fatalf("命中数 = %d, want 1（%q 对 %q）", len(hits), tc.token, tc.text)
				}
				if hits[0].IsPath || hits[0].Line != 7 || hits[0].Where != "internal/app.go" ||
					hits[0].Token != tc.token || hits[0].Context != tc.text {
					t.Fatalf("命中形状错：%+v", hits[0])
				}
				return
			}
			if len(hits) != 0 {
				t.Fatalf("不该命中：%+v", hits)
			}
		})
	}
}

// ---------------- Scan：扫描面（新增行内容 + 路径段） ----------------

func TestScanContentAndPathSegments(t *testing.T) {
	added := []AddedLine{
		{File: "a.go", Line: 1, Text: "acme one"},
		{File: "a.go", Line: 2, Text: "clean line"},
		{File: "acme/x.go", Line: 5, Text: "no token here"},
		{File: "b/acme.go", Line: 1, Text: "acme two"},
	}
	hits := Scan(added, nil, []string{"acme"})

	// 内容面按新增行序，路径面按路径首见序；路径命中行号恒 0。
	want := []ScanHit{
		{Where: "a.go", Line: 1, Token: "acme", Context: "acme one"},
		{Where: "b/acme.go", Line: 1, Token: "acme", Context: "acme two"},
		{Where: "acme/x.go", Line: 0, Token: "acme", Context: "acme", IsPath: true},
		{Where: "b/acme.go", Line: 0, Token: "acme", Context: "acme.go", IsPath: true},
	}
	if len(hits) != len(want) {
		t.Fatalf("命中数 = %d, want %d：%+v", len(hits), len(want), hits)
	}
	for i, w := range want {
		if hits[i] != w {
			t.Fatalf("hits[%d] = %+v, want %+v", i, hits[i], w)
		}
	}
}

func TestScanPathHitDedupAndNewFilePaths(t *testing.T) {
	added := []AddedLine{
		{File: "docs/acme-adr.md", Line: 1, Text: "clean"},
		{File: "docs/acme-adr.md", Line: 2, Text: "clean"},
	}
	// 空的新增文件没有新增行（diff 无 hunk），路径段扫描只能靠 newFilePaths。
	newFilePaths := [][]string{
		{"docs", "acme-adr.md"}, // 与新增行的 File 同一路径 ⇒ 去重
		{"docs", "empty-acme.txt"},
		{"docs", "clean.txt"},
	}
	hits := Scan(added, newFilePaths, []string{"acme"})
	want := []ScanHit{
		{Where: "docs/acme-adr.md", Line: 0, Token: "acme", Context: "acme-adr.md", IsPath: true},
		{Where: "docs/empty-acme.txt", Line: 0, Token: "acme", Context: "empty-acme.txt", IsPath: true},
	}
	if len(hits) != len(want) {
		t.Fatalf("命中数 = %d, want %d：%+v", len(hits), len(want), hits)
	}
	for i, w := range want {
		if hits[i] != w {
			t.Fatalf("hits[%d] = %+v, want %+v", i, hits[i], w)
		}
	}
}

func TestScanTruncatesAt40(t *testing.T) {
	var added []AddedLine
	for i := 1; i <= 41; i++ {
		added = append(added, AddedLine{File: "f.go", Line: i, Text: "acme line"})
	}
	hits := Scan(added, nil, []string{"acme"})
	const wantMax = 40
	if len(hits) != wantMax {
		t.Fatalf("命中数 = %d, want %d（>40 截断）", len(hits), wantMax)
	}
	if hits[len(hits)-1].Line != wantMax {
		t.Fatalf("截断后末条 = %+v, want 前 %d 条按序保留", hits[len(hits)-1], wantMax)
	}
	// 恰 40 条不截断（边界另一侧）。
	hits = Scan(added[:40], nil, []string{"acme"})
	if len(hits) != wantMax {
		t.Fatalf("40 条输入时命中数 = %d, want %d（≤40 不截断）", len(hits), wantMax)
	}
}

func TestScanVacuousAndEmptyInputs(t *testing.T) {
	added := []AddedLine{{File: "acme/x.go", Line: 1, Text: "acme"}}
	if hits := Scan(added, [][]string{{"acme"}}, nil); hits != nil {
		t.Fatalf("空 token 集 ⇒ 零命中（vacuous pass），实际 %+v", hits)
	}
	if hits := Scan(nil, nil, []string{"acme"}); hits != nil {
		t.Fatalf("零输入 ⇒ 零命中，实际 %+v", hits)
	}
	// 空串 token 不得命中一切（BuildTokens 已剔除，这里是纵深防御）。
	if hits := Scan(added, nil, []string{""}); hits != nil {
		t.Fatalf("空串 token 不得命中，实际 %+v", hits)
	}
}

// ---------------- AddedLinesFromPatch ----------------

func TestAddedLinesFromPatchModifiedFile(t *testing.T) {
	// 手写夹具：两处 hunk，含上下文行/删除行与行号推进。
	patch := `diff --git a/internal/util/x.go b/internal/util/x.go
index 1111111111111111111111111111111111111111..2222222222222222222222222222222222222222 100644
--- a/internal/util/x.go
+++ b/internal/util/x.go
@@ -1,3 +1,3 @@
 package util
+// acme added
 var X = 1
-var Y = 2
+var Y = 3
@@ -10,2 +11,3 @@ func f() {
 return
 }
+// tail
`
	got := AddedLinesFromPatch([]byte(patch))
	want := []AddedLine{
		{File: "internal/util/x.go", Line: 2, Text: "// acme added"},
		{File: "internal/util/x.go", Line: 4, Text: "var Y = 3"},
		{File: "internal/util/x.go", Line: 13, Text: "// tail"},
	}
	if len(got) != len(want) {
		t.Fatalf("+ 行数 = %d, want %d：%+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("got[%d] = %+v, want %+v", i, got[i], w)
		}
	}
}

func TestAddedLinesFromPatchNewFile(t *testing.T) {
	// §20.6 配方 2'：/dev/null 形态（--no-prefix 下 +++ 为 b/<path>）。
	patch := `diff --git b/internal/util/new.go b/internal/util/new.go
new file mode 100644
index 0000000000000000000000000000000000000000..3333333333333333333333333333333333333333
--- /dev/null
+++ b/internal/util/new.go
@@ -0,0 +1,2 @@
+package util
+// acme new
`
	got := AddedLinesFromPatch([]byte(patch))
	want := []AddedLine{
		{File: "internal/util/new.go", Line: 1, Text: "package util"},
		{File: "internal/util/new.go", Line: 2, Text: "// acme new"},
	}
	if len(got) != len(want) {
		t.Fatalf("+ 行数 = %d, want %d：%+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("got[%d] = %+v, want %+v", i, got[i], w)
		}
	}
}

func TestAddedLinesFromPatchEmptyAddedLineAndNoNewline(t *testing.T) {
	patch := `diff --git a/nn.txt b/nn.txt
index 27e20cddb730695d0e2c1402d860f8f5365bfb75..51740510f688c953d781bda002e280d1518a446e 100644
--- a/nn.txt
+++ b/nn.txt
@@ -1,2 +1,3 @@
 x
-old
+
+new
\ No newline at end of file
`
	got := AddedLinesFromPatch([]byte(patch))
	want := []AddedLine{
		{File: "nn.txt", Line: 2, Text: ""},
		{File: "nn.txt", Line: 3, Text: "new"},
	}
	if len(got) != len(want) {
		t.Fatalf("+ 行数 = %d, want %d：%+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("got[%d] = %+v, want %+v", i, got[i], w)
		}
	}
}

func TestAddedLinesFromPatchQuotedPath(t *testing.T) {
	// git 对含空格/非 ASCII 的路径加引号并做 C 风格转义（core.quotePath 默认）。
	patch := `diff --git "a/dir with space/x.txt" "b/dir with space/x.txt"
index aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa..bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb 100644
--- "a/dir with space/x.txt"
+++ "b/dir with space/x.txt"
@@ -1 +1 @@
-a
+b
`
	got := AddedLinesFromPatch([]byte(patch))
	if len(got) != 1 || got[0] != (AddedLine{File: "dir with space/x.txt", Line: 1, Text: "b"}) {
		t.Fatalf("got = %+v, want 单条 {dir with space/x.txt 1 b}", got)
	}
}

func TestAddedLinesFromPatchIgnoresMailboxBody(t *testing.T) {
	// mailbox 正文/stat 段里的 `@@`/`+`/`---` 形状不得被当成 hunk：
	// 只有见到 `+++ ` 行之后才进入 hunk 解析。
	patch := `From 1111111111111111111111111111111111111111 Mon Sep 17 00:00:00 2001
From: t <t@e>
Date: Thu, 1 Jan 2026 00:00:00 +0000
Subject: [PATCH] demo

正文示例：
@@ -1 +1 @@
+not a patch line
---
 internal/util/x.go | 1 +
 1 file changed, 1 insertion(+)

diff --git a/internal/util/x.go b/internal/util/x.go
index 1111111111111111111111111111111111111111..2222222222222222222222222222222222222222 100644
--- a/internal/util/x.go
+++ b/internal/util/x.go
@@ -1 +1,2 @@
 a
+b
`
	got := AddedLinesFromPatch([]byte(patch))
	if len(got) != 1 || got[0] != (AddedLine{File: "internal/util/x.go", Line: 2, Text: "b"}) {
		t.Fatalf("got = %+v, want 只取真 diff 段的一条", got)
	}
}

func TestAddedLinesFromPatchIgnoresDiffShapeInBody(t *testing.T) {
	// 消息正文引用的整段 diff 形状文本（`+++ b/…` + `@@` + `+`）不得被当作 hunk：
	// 非 hunk 态的 `+++ ` 只在已见 `diff --git ` 段头之后才生效（M8 观察-1）。
	patch := `From 1111111111111111111111111111111111111111 Mon Sep 17 00:00:00 2001
From: t <t@e>
Date: Thu, 1 Jan 2026 00:00:00 +0000
Subject: [PATCH] demo

正文引用了一段 diff 形状：
+++ b/phantom.go
@@ -1 +1 @@
+myapp ghost line
diff --git a/internal/util/x.go b/internal/util/x.go
index 1111111111111111111111111111111111111111..2222222222222222222222222222222222222222 100644
--- a/internal/util/x.go
+++ b/internal/util/x.go
@@ -1 +1,2 @@
 a
+b
`
	got := AddedLinesFromPatch([]byte(patch))
	if len(got) != 1 || got[0] != (AddedLine{File: "internal/util/x.go", Line: 2, Text: "b"}) {
		t.Fatalf("got = %+v, want 只取真 diff 段的一条（正文幽灵段不得进入）", got)
	}
}

func TestAddedLinesFromPatchRecipeDiff(t *testing.T) {
	// 直喂 §20.6 配方真实产出的 diff 字节（防 git 输出形态漂移）。
	tmp := t.TempDir()
	writeFileT(t, tmp, "a/internal/util/x.go", "a\nb\nc\n")
	writeFileT(t, tmp, "b/internal/util/x.go", "a\nb2\nc\n")
	writeFileT(t, tmp, "b/internal/util/new.go", "package util\n")
	writeFileT(t, tmp, "a/crlf.txt", "x\r\nold\r\n")
	writeFileT(t, tmp, "b/crlf.txt", "x\r\nnew\r\n")

	mod := recipeDiffT(t, tmp, "diff", "--no-index", "--full-index", "--no-prefix",
		"a/internal/util/x.go", "b/internal/util/x.go")
	if got := AddedLinesFromPatch([]byte(mod)); len(got) != 1 ||
		got[0] != (AddedLine{File: "internal/util/x.go", Line: 2, Text: "b2"}) {
		t.Fatalf("修改文件：got = %+v", got)
	}

	add := recipeDiffT(t, tmp, "diff", "--no-index", "--full-index", "--no-prefix",
		"/dev/null", "b/internal/util/new.go")
	if got := AddedLinesFromPatch([]byte(add)); len(got) != 1 ||
		got[0] != (AddedLine{File: "internal/util/new.go", Line: 1, Text: "package util"}) {
		t.Fatalf("新增文件：got = %+v", got)
	}

	// CRLF 文件：行尾 CR 是行终止符的一部分，不进 Text（否则报告行会被回车打断）。
	crlf := recipeDiffT(t, tmp, "diff", "--no-index", "--full-index", "--no-prefix",
		"a/crlf.txt", "b/crlf.txt")
	if got := AddedLinesFromPatch([]byte(crlf)); len(got) != 1 ||
		got[0] != (AddedLine{File: "crlf.txt", Line: 2, Text: "new"}) {
		t.Fatalf("CRLF 文件：got = %+v", got)
	}
}

func TestAddedLinesFromPatchEmptyNewFile(t *testing.T) {
	// 空新增文件的配方 diff 没有 ---/+++/@@（只有 new file mode + index）：
	// 零个 + 行是正确结果，路径段扫描由调用方的 newFilePaths 承担。
	patch := `diff --git b/empty.txt b/empty.txt
new file mode 100644
index 0000000000000000000000000000000000000000..e69de29bb2d1d6434b8b29ae775ad8c2e48c5391
`
	if got := AddedLinesFromPatch([]byte(patch)); got != nil {
		t.Fatalf("空新增文件应零 + 行，实际 %+v", got)
	}
}

// recipeDiffT 跑一次 §20.6 配方的 `git diff --no-index`（两侧有差异时 rc=1，
// 故不能复用 runGitT 的「失败即 Fatal」），返回 stdout 字节。
func recipeDiffT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok || ee.ExitCode() > 1 { // rc=1 = 有差异，正常
			t.Fatalf("git %v: %v\n%s", args, err, stderr.String())
		}
	}
	return stdout.String()
}
