package manifest

import (
	"strings"
	"testing"
)

const minimal = `
manifest_version: 1
vars:
  - name: project_name
    prompt: "产品显示名"
    default: "MyApp"
  - name: app_slug
    default: "{{ .project_name | lower }}"
    pattern: '^[a-z][a-z0-9-]*$'
protected_literals:
  - "@xbcio/ui"
substitute:
  - from: "Xkitora"
    to: "{{ .project_name }}"
    paths: ["**/*.go"]
    target_case: none
  - from: "xkitora"
    to: "{{ .app_slug }}"
    paths: ["go.mod"]
    when_preceded_by: 'module[ \t]+'
render:
  - paths: ["**/*.tmpl"]
    strip_suffix: ".tmpl"
rename:
  - from: "xkitora"
    to: "{{ .app_slug }}"
exclude:
  paths: ["dist/**"]
generated:
  paths: ["frontend/bindings/**"]
hooks:
  post_init: |
    set -e
    echo hello
`

func TestParseMinimal(t *testing.T) {
	m, err := Parse([]byte(minimal))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.Version != 1 {
		t.Errorf("Version = %d, want 1", m.Version)
	}
	// vars 必须保序 —— map 不保序而 default 可以引用先前变量
	if len(m.Vars) != 2 || m.Vars[0].Name != "project_name" || m.Vars[1].Name != "app_slug" {
		t.Fatalf("vars 顺序错误: %+v", m.Vars)
	}
	if m.Vars[0].Prompt != "产品显示名" {
		t.Errorf("Prompt = %q", m.Vars[0].Prompt)
	}
	if got := m.Substitute[1].WhenPrecededBy; got != `module[ \t]+` {
		t.Errorf("WhenPrecededBy = %q", got)
	}
	if m.Hooks.PostInit != "set -e\necho hello\n" {
		t.Errorf("PostInit 块标量解析错误: %q", m.Hooks.PostInit)
	}
	if len(m.Render) != 1 || m.Render[0].StripSuffix != ".tmpl" {
		t.Errorf("Render = %+v", m.Render)
	}
}

func TestParseRejectsUnknownTopLevelKey(t *testing.T) {
	_, err := Parse([]byte("manifest_version: 1\nnope: 1\n"))
	if err == nil {
		t.Fatal("未知顶层键应报错（Strict 模式）")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("错误信息应包含键名，实际: %v", err)
	}
}

func TestParseRejectsUnknownNestedKey(t *testing.T) {
	_, err := Parse([]byte("manifest_version: 1\nvars:\n  - name: a\n    nope: 1\n"))
	if err == nil {
		t.Fatal("未知嵌套键应报错")
	}
}

func TestParseRejectsVarsAsMapping(t *testing.T) {
	// Go 类型系统直接拦下 —— 不需要额外检查
	_, err := Parse([]byte("manifest_version: 1\nvars:\n  a: 1\n"))
	if err == nil {
		t.Fatal("vars 写成映射应报错")
	}
	if !strings.Contains(err.Error(), "sequence") {
		t.Errorf("错误信息应提到 sequence，实际: %v", err)
	}
}

func TestDefinitionsIsAcceptedAndIgnored(t *testing.T) {
	src := `
manifest_version: 1
definitions:
  go_ts: &go_ts
    - "**/*.go"
substitute:
  - from: "A"
    to: "B"
    paths: *go_ts
`
	m, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(m.Substitute[0].Paths) != 1 || m.Substitute[0].Paths[0] != "**/*.go" {
		t.Errorf("锚点展开失败: %+v", m.Substitute[0].Paths)
	}
}

func TestParseRejectsMissingVersion(t *testing.T) {
	if _, err := Parse([]byte("vars: []\n")); err == nil {
		t.Fatal("缺少 manifest_version 应报错")
	}
}

func TestParseRejectsNewerVersion(t *testing.T) {
	_, err := Parse([]byte("manifest_version: 99\n"))
	if err == nil {
		t.Fatal("版本高于本工具支持时应报错，而不是尽力解析")
	}
	if !strings.Contains(err.Error(), "99") {
		t.Errorf("错误信息应包含版本号: %v", err)
	}
}
