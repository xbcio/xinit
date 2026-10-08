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
	for _, e := range entries {
		got, _, err := Content(fsys, c, v, e, true)
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
	for _, e := range entries {
		got, _, err := Content(fsys, c, v, e, true)
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
	got, _, err := Content(fsys, c, v, entries[0], true)
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
	got, _, err := Content(fsys, c, Vars{}, entries[0], true)
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
	got, _, err := Content(fsys, c, Vars{}, entries[0], true)
	if err != nil {
		t.Fatalf("Content: %v", err)
	}
	if string(got) != string(raw) {
		t.Errorf("二进制应原样透传: %v", got)
	}
}

func TestContentPreservesCRLF(t *testing.T) {
	c := compile(t, `
manifest_version: 1
substitute:
  - from: "xkitora"
    to: "xdesk"
    paths: ["**"]
`)
	fsys := fstest.MapFS{"a.txt": &fstest.MapFile{Data: []byte("xkitora\r\nnext\r\n")}}
	entries, _, err := Paths(fsys, c, Vars{})
	if err != nil {
		t.Fatalf("Paths: %v", err)
	}
	got, _, err := Content(fsys, c, Vars{}, entries[0], true)
	if err != nil {
		t.Fatalf("Content: %v", err)
	}
	if string(got) != "xdesk\r\nnext\r\n" {
		t.Errorf("EOL 必须保持: %q", got)
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
	got, _, err := Content(fsys, c, Vars{}, entries[0], true)
	if err != nil {
		t.Fatalf("Content: %v", err)
	}
	if string(got) != "xdesk\r\nnext\n" {
		t.Errorf("混合 EOL 的未替换字节必须原样保留: %q", got)
	}
}

func TestContentZeroHitWarningOnlyWhenRequested(t *testing.T) {
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

	_, warns, err := Content(fsys, c, Vars{}, entries[0], true)
	if err != nil {
		t.Fatalf("Content: %v", err)
	}
	if len(warns) == 0 {
		t.Error("reportZeroHits=true 时应给出零命中警告")
	}

	_, warns, err = Content(fsys, c, Vars{}, entries[0], false)
	if err != nil {
		t.Fatalf("Content: %v", err)
	}
	if len(warns) != 0 {
		t.Error("reportZeroHits=false 时不应告警（重建 base 时会合法地产生噪音）")
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
	got, _, err := Content(fsys, c, Vars{}, entries[0], true)
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
	if _, _, err := Content(fsys, c, Vars{"app_slug": "xdesk"}, entries[0], true); err == nil {
		t.Fatal("to 里引用未声明变量应报错，而不是把占位符原样写出")
	}
}
