package render

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestContentSubstAppliesRules(t *testing.T) {
	c := compile(t, `
manifest_version: 1
substitute:
  - from: "Xkitora"
    to: "{{ .project_name }}"
    paths: ["**/*.go"]
  - from: "xkitora"
    to: "{{ .app_slug }}"
    paths: ["go.mod"]
`)
	v := Vars{"project_name": "Xdesk", "app_slug": "xdesk"}
	fsys := fstest.MapFS{
		"main.go": &fstest.MapFile{Data: []byte("package Xkitora\n")},
		"go.mod":  &fstest.MapFile{Data: []byte("module xkitora\n")},
	}
	entries, _, err := Paths(fsys, c, v)
	if err != nil {
		t.Fatalf("Paths: %v", err)
	}
	// Paths 返回空列表时下面的循环什么都不做，测试会空转通过
	if len(entries) != 2 {
		t.Fatalf("Paths 应返回 2 个条目，实际 %d: %+v", len(entries), entries)
	}
	for _, e := range entries {
		got, _, _, err := Content(fsys, c, v, e)
		if err != nil {
			t.Fatalf("Content(%s): %v", e.Dst, err)
		}
		if strings.Contains(string(got), "kitora") {
			t.Errorf("%s 未被替换: %q", e.Dst, got)
		}
	}
}

func TestContentSubstRespectsPathsScope(t *testing.T) {
	c := compile(t, `
manifest_version: 1
substitute:
  - from: "xkitora"
    to: "xdesk"
    paths: ["go.mod"]
`)
	v := Vars{}
	fsys := fstest.MapFS{
		"go.mod": &fstest.MapFile{Data: []byte("module xkitora\n")},
		"a.txt":  &fstest.MapFile{Data: []byte("xkitora\n")},
	}
	entries, _, err := Paths(fsys, c, v)
	if err != nil {
		t.Fatalf("Paths: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("Paths 应返回 2 个条目，实际 %d: %+v", len(entries), entries)
	}
	for _, e := range entries {
		got, _, _, err := Content(fsys, c, v, e)
		if err != nil {
			t.Fatalf("Content: %v", err)
		}
		switch e.Dst {
		case "go.mod":
			if string(got) != "module xdesk\n" {
				t.Errorf("go.mod 应被替换: %q", got)
			}
		case "a.txt":
			if string(got) != "xkitora\n" {
				t.Errorf("a.txt 不在 paths 范围内，不应被替换: %q", got)
			}
		default:
			t.Errorf("出现未预期的路径 %q", e.Dst)
		}
	}
}

func TestContentTemplateMode(t *testing.T) {
	c := compile(t, `
manifest_version: 1
render:
  - paths: ["**/*.tmpl"]
    strip_suffix: ".tmpl"
`)
	v := Vars{"name": "World"}
	fsys := fstest.MapFS{
		"x.txt.tmpl": &fstest.MapFile{Data: []byte("hello {{ .name }}")},
	}
	entries, _, err := Paths(fsys, c, v)
	if err != nil {
		t.Fatalf("Paths: %v", err)
	}
	got, _, _, err := Content(fsys, c, v, entries[0])
	if err != nil {
		t.Fatalf("Content: %v", err)
	}
	if string(got) != "hello World" {
		t.Errorf("= %q", got)
	}
}

func TestContentTemplateModeNotAffectedBySubstitute(t *testing.T) {
	c := compile(t, `
manifest_version: 1
substitute:
  - from: "xkitora"
    to: "REPLACED"
    paths: ["**"]
render:
  - paths: ["**/*.tmpl"]
`)
	fsys := fstest.MapFS{"a.tmpl": &fstest.MapFile{Data: []byte("xkitora")}}
	entries, _, err := Paths(fsys, c, Vars{})
	if err != nil {
		t.Fatalf("Paths: %v", err)
	}
	got, _, _, err := Content(fsys, c, Vars{}, entries[0])
	if err != nil {
		t.Fatalf("Content: %v", err)
	}
	if string(got) != "xkitora" {
		t.Errorf("Template 模式的文件应排除在 substitute 之外: %q", got)
	}
}

func TestContentBinaryPassesThrough(t *testing.T) {
	c := compile(t, `
manifest_version: 1
substitute:
  - from: "xkitora"
    to: "xdesk"
    paths: ["**"]
`)
	raw := []byte{0x00, 'x', 'k', 'i', 't', 'o', 'r', 'a'}
	fsys := fstest.MapFS{"a.bin": &fstest.MapFile{Data: raw}}
	entries, _, err := Paths(fsys, c, Vars{})
	if err != nil {
		t.Fatalf("Paths: %v", err)
	}
	got, _, _, err := Content(fsys, c, Vars{}, entries[0])
	if err != nil {
		t.Fatalf("Content: %v", err)
	}
	if string(got) != string(raw) {
		t.Errorf("二进制应原样透传: %v", got)
	}
}

func TestContentPreservesCRLF(t *testing.T) {
	// 谓词窗口是「本行 [end, 行尾)」，且 lineEnd 的既有语义是**不含 '\n'**
	// （Task 9 已锁定）——所以 when_followed_by: "\n" 永远看不到换行符、
	// 恒不命中，这种写法测不出任何东西。
	//
	// 窗口里唯一能区分「归一化前/后」的字节是行尾的 '\r'：
	//   CRLF 原文：匹配后窗口 = "\r"；归一化后：窗口 = ""（end 即 '\n'）。
	// 编译后的 FollowedBy 是 "^(?:pat)"，取 pat = "$"：
	//   - 空窗口命中（位置 0 就是文本末尾）；
	//   - "\r" 不命中（位置 0 不是末尾）。
	// 于是这条测试真正判定「先归一化再匹配」：拿掉 ToLF 就不命中
	// （'\r' 让 $ 失败，输出保持原文），拿掉 FromLF 输出会变成 LF、
	// 断言失败。
	c := compile(t, `
manifest_version: 1
substitute:
  - from: "xkitora"
    to: "xdesk"
    when_followed_by: "$"
    paths: ["**"]
`)
	fsys := fstest.MapFS{"a.txt": &fstest.MapFile{Data: []byte("xkitora\r\nnext\r\n")}}
	entries, _, err := Paths(fsys, c, Vars{})
	if err != nil {
		t.Fatalf("Paths: %v", err)
	}
	got, _, _, err := Content(fsys, c, Vars{}, entries[0])
	if err != nil {
		t.Fatalf("Content: %v", err)
	}
	if string(got) != "xdesk\r\nnext\r\n" {
		t.Errorf("EOL 必须保持（先归一化命中、写出还原 CRLF）: %q", got)
	}
}

func TestContentMixedEOLUntouched(t *testing.T) {
	// 混合 EOL 不做归一化再还原 —— 那会改坏未被替换的字节
	c := compile(t, `
manifest_version: 1
substitute:
  - from: "xkitora"
    to: "xdesk"
    paths: ["**"]
`)
	raw := []byte("xkitora\r\nnext\n")
	fsys := fstest.MapFS{"a.txt": &fstest.MapFile{Data: raw}}
	entries, _, err := Paths(fsys, c, Vars{})
	if err != nil {
		t.Fatalf("Paths: %v", err)
	}
	got, _, _, err := Content(fsys, c, Vars{}, entries[0])
	if err != nil {
		t.Fatalf("Content: %v", err)
	}
	if string(got) != "xdesk\r\nnext\n" {
		t.Errorf("混合 EOL 的未替换字节必须原样保留: %q", got)
	}
}

func TestContentReturnsRuleHitsWithManifestIndex(t *testing.T) {
	// Index 必须是**清单里的**序号：buildRules 会过滤掉不作用于本文件的
	// 规则，用过滤后的下标会让调用方的零命中告警指错规则。
	c := compile(t, `
manifest_version: 1
substitute:
  - from: "NOT_IN_THIS_FILE"
    to: "x"
    paths: ["go.mod"]
  - from: "xkitora"
    to: "xdesk"
    paths: ["**"]
`)
	fsys := fstest.MapFS{"a.txt": &fstest.MapFile{Data: []byte("xkitora\n")}}
	entries, _, err := Paths(fsys, c, Vars{})
	if err != nil {
		t.Fatalf("Paths: %v", err)
	}
	_, hits, _, err := Content(fsys, c, Vars{}, entries[0])
	if err != nil {
		t.Fatalf("Content: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("只应返回作用于本文件的规则，实际 %+v", hits)
	}
	if hits[0].Index != 1 {
		t.Errorf("Index 应是清单序号 1（序号 0 不作用于本文件），实际 %d", hits[0].Index)
	}
	if hits[0].Count != 1 {
		t.Errorf("命中数 = %d, want 1", hits[0].Count)
	}
}

func TestContentReportsZeroCountWithoutWarning(t *testing.T) {
	// 规则命中 0 次时 Content **不**逐文件告警（paths: ["**"] 的规则会刷出
	// 与文件数同量的噪音），而是把 Count=0 交给调用方汇总后按规则告警一次。
	c := compile(t, `
manifest_version: 1
substitute:
  - from: "ABSENT"
    to: "x"
    paths: ["**"]
`)
	fsys := fstest.MapFS{"a.txt": &fstest.MapFile{Data: []byte("nothing\n")}}
	entries, _, err := Paths(fsys, c, Vars{})
	if err != nil {
		t.Fatalf("Paths: %v", err)
	}
	_, hits, warns, err := Content(fsys, c, Vars{}, entries[0])
	if err != nil {
		t.Fatalf("Content: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("Content 不应逐文件发零命中告警: %+v", warns)
	}
	if len(hits) != 1 || hits[0].Index != 0 || hits[0].Count != 0 {
		t.Fatalf("应返回 Count=0 的统计: %+v", hits)
	}
}

func TestContentProtectedLiterals(t *testing.T) {
	c := compile(t, `
manifest_version: 1
protected_literals:
  - "@xbcio/xkitora"
substitute:
  - from: "xkitora"
    to: "xdesk"
    paths: ["**"]
`)
	fsys := fstest.MapFS{"a.txt": &fstest.MapFile{Data: []byte("@xbcio/xkitora and xkitora\n")}}
	entries, _, err := Paths(fsys, c, Vars{})
	if err != nil {
		t.Fatalf("Paths: %v", err)
	}
	got, _, _, err := Content(fsys, c, Vars{}, entries[0])
	if err != nil {
		t.Fatalf("Content: %v", err)
	}
	if string(got) != "@xbcio/xkitora and xdesk\n" {
		t.Errorf("@ 开头的包名必须被保护: %q", got)
	}
}

func TestContentRejectsUndeclaredVarInRuleValue(t *testing.T) {
	// to 里引用了未声明的变量时必须报错。若吞掉错误，规则会保留原始
	// 占位符文本，把 "{{ .nope }}" 原样写进派生项目。
	c := compile(t, `
manifest_version: 1
vars:
  - name: app_slug
    default: "xdesk"
substitute:
  - from: "xkitora"
    to: "{{ .nope }}"
    paths: ["**"]
`)
	fsys := fstest.MapFS{"a.txt": &fstest.MapFile{Data: []byte("xkitora\n")}}
	entries, _, err := Paths(fsys, c, Vars{"app_slug": "xdesk"})
	if err != nil {
		t.Fatalf("Paths: %v", err)
	}
	if _, _, _, err := Content(fsys, c, Vars{"app_slug": "xdesk"}, entries[0]); err == nil {
		t.Fatal("to 里引用未声明变量应报错，而不是把占位符原样写出")
	}
}
