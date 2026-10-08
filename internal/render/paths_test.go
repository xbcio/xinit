package render

import (
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/xbcio/xinit/internal/manifest"
)

func compile(t *testing.T, src string) *manifest.Compiled {
	t.Helper()
	m, err := manifest.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return c
}

func dsts(entries []Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Dst
	}
	return out
}

func find(t *testing.T, entries []Entry, dst string) Entry {
	t.Helper()
	for _, e := range entries {
		if e.Dst == dst {
			return e
		}
	}
	t.Fatalf("未找到 Dst=%q，实际有 %v", dst, dsts(entries))
	return Entry{}
}

func TestEvalVarsOrderAndDefaults(t *testing.T) {
	c := compile(t, `
manifest_version: 1
vars:
  - name: project_name
    default: "MyApp"
  - name: app_slug
    default: "{{ .project_name | lower }}"
`)
	v, err := EvalVars(c, nil)
	if err != nil {
		t.Fatalf("EvalVars: %v", err)
	}
	if v["project_name"] != "MyApp" {
		t.Errorf("project_name = %q", v["project_name"])
	}
	// default 可以引用先前声明的变量 —— 这要求 vars 保序
	if v["app_slug"] != "myapp" {
		t.Errorf("app_slug = %q, want %q（default 应能引用先前变量）", v["app_slug"], "myapp")
	}
}

func TestEvalVarsOverrideFromConfig(t *testing.T) {
	c := compile(t, `
manifest_version: 1
vars:
  - name: project_name
    default: "MyApp"
  - name: app_slug
    default: "{{ .project_name | lower }}"
`)
	v, err := EvalVars(c, map[string]string{"project_name": "Xdesk"})
	if err != nil {
		t.Fatalf("EvalVars: %v", err)
	}
	if v["project_name"] != "Xdesk" || v["app_slug"] != "xdesk" {
		t.Errorf("覆盖未生效: %+v", v)
	}
}

func TestEvalVarsPatternValidation(t *testing.T) {
	c := compile(t, `
manifest_version: 1
vars:
  - name: app_slug
    default: "BadSlug"
    pattern: '^[a-z][a-z0-9-]*$'
`)
	_, err := EvalVars(c, nil)
	if err == nil {
		t.Fatal("不匹配 pattern 的默认值应报错")
	}
}

func TestEvalVarsRequiredMissing(t *testing.T) {
	c := compile(t, `
manifest_version: 1
vars:
  - name: token
    required: true
`)
	if _, err := EvalVars(c, nil); err == nil {
		t.Fatal("required 且未提供应报错")
	}
	if _, err := EvalVars(c, map[string]string{"token": "x"}); err != nil {
		t.Fatalf("提供了值就不该报错: %v", err)
	}
}

func TestPathsDeclaredVariableAlwaysPresentInMap(t *testing.T) {
	// 未提供、无默认、非 required 的变量取值 ""，而不是缺席
	c := compile(t, `
manifest_version: 1
vars:
  - name: feature
`)
	v, err := EvalVars(c, nil)
	if err != nil {
		t.Fatalf("EvalVars: %v", err)
	}
	if got, ok := v["feature"]; !ok || got != "" {
		t.Errorf("feature = %q (present=%v), want 存在且为空串", got, ok)
	}
}

func TestPathsImplicitExcludes(t *testing.T) {
	c := compile(t, "manifest_version: 1\n")
	fsys := fstest.MapFS{
		"main.go":           &fstest.MapFile{Data: []byte("package main\n")},
		"xinit.yml":         &fstest.MapFile{Data: []byte("manifest_version: 1\n")},
		".git/config":       &fstest.MapFile{Data: []byte("[core]\n")},
		".xinit/state.json": &fstest.MapFile{Data: []byte("{}\n")},
	}
	entries, _, err := Paths(fsys, c, Vars{})
	if err != nil {
		t.Fatalf("Paths: %v", err)
	}
	if len(entries) != 1 || entries[0].Dst != "main.go" {
		t.Errorf("隐式排除失效，实际: %v", dsts(entries))
	}
}

func TestPathsExcludeMatchesRawSrc(t *testing.T) {
	c := compile(t, `
manifest_version: 1
exclude:
  paths: ["dist/**"]
`)
	fsys := fstest.MapFS{
		"a.go":      &fstest.MapFile{Data: []byte("x")},
		"dist/a.js": &fstest.MapFile{Data: []byte("x")},
	}
	entries, _, err := Paths(fsys, c, Vars{})
	if err != nil {
		t.Fatalf("Paths: %v", err)
	}
	if len(entries) != 1 || entries[0].Dst != "a.go" {
		t.Errorf("exclude 未生效: %v", dsts(entries))
	}
}

func TestPathsRenderModeAndStripSuffix(t *testing.T) {
	c := compile(t, `
manifest_version: 1
render:
  - paths: ["**/*.tmpl"]
    strip_suffix: ".tmpl"
`)
	fsys := fstest.MapFS{
		"a.go":      &fstest.MapFile{Data: []byte("x")},
		"b.go.tmpl": &fstest.MapFile{Data: []byte("{{ .v }}")},
	}
	entries, _, err := Paths(fsys, c, Vars{"v": "1"})
	if err != nil {
		t.Fatalf("Paths: %v", err)
	}
	e := find(t, entries, "b.go")
	if e.Mode != ModeTemplate {
		t.Error("strip_suffix 未生效或 Mode 未设为 Template")
	}
	if e.Src != "b.go.tmpl" {
		t.Errorf("Src 应保留原路径，实际 %q", e.Src)
	}
	if other := find(t, entries, "a.go"); other.Mode != ModeSubst {
		t.Error("未匹配 render 的路径应为 ModeSubst")
	}
}

func TestPathsRenderMatcherMatchesBeforeStrip(t *testing.T) {
	// paths 判定必须在 strip_suffix **之前** —— 否则 **/*.tmpl 永远匹配不到
	c := compile(t, `
manifest_version: 1
render:
  - paths: ["**/*.tmpl"]
    strip_suffix: ".tmpl"
`)
	fsys := fstest.MapFS{"b.go.tmpl": &fstest.MapFile{Data: []byte("x")}}
	entries, _, err := Paths(fsys, c, Vars{})
	if err != nil {
		t.Fatalf("Paths: %v", err)
	}
	if len(entries) != 1 || entries[0].Mode != ModeTemplate {
		t.Errorf("render 判定应在 strip_suffix 之前: %+v", entries)
	}
}

func TestPathsRenameAppliesPerSegment(t *testing.T) {
	c := compile(t, `
manifest_version: 1
rename:
  - from: "xkitora"
    to: "xdesk"
`)
	fsys := fstest.MapFS{
		"xkitora/main.go":   &fstest.MapFile{Data: []byte("x")},
		"deep/xkitora/a.go": &fstest.MapFile{Data: []byte("x")},
		"libxkitora/a.go":   &fstest.MapFile{Data: []byte("x")}, // 字母边界：不改
	}
	entries, _, err := Paths(fsys, c, Vars{})
	if err != nil {
		t.Fatalf("Paths: %v", err)
	}
	got := dsts(entries)
	want := map[string]bool{"xdesk/main.go": true, "deep/xdesk/a.go": true, "libxkitora/a.go": true}
	for _, d := range got {
		if !want[d] {
			t.Errorf("非预期的 Dst %q（全部: %v）", d, got)
		}
	}
	if len(got) != 3 {
		t.Errorf("条目数 = %d, want 3: %v", len(got), got)
	}
}

func TestPathsRenameToIsRendered(t *testing.T) {
	// to 里的变量必须被渲染。若直接用原始 Spec.To，
	// 占位符字面量 "{{ .app_slug }}" 会被原样写进路径。
	c := compile(t, `
manifest_version: 1
vars:
  - name: app_slug
    default: "xdesk"
rename:
  - from: "xkitora"
    to: "{{ .app_slug }}"
`)
	fsys := fstest.MapFS{
		"xkitora/main.go": &fstest.MapFile{Data: []byte("x")},
	}
	entries, _, err := Paths(fsys, c, Vars{"app_slug": "xdesk"})
	if err != nil {
		t.Fatalf("Paths: %v", err)
	}
	if len(entries) != 1 || entries[0].Dst != "xdesk/main.go" {
		t.Errorf("rename.to 未渲染: %v", dsts(entries))
	}
}

func TestPathsRenameToWithVariableErrorFailsEarly(t *testing.T) {
	// to 引用了未声明的变量：必须在遍历（乃至写盘）之前就报错，
	// 而不是每个文件报一次或静默写出字面量。
	c := compile(t, `
manifest_version: 1
vars:
  - name: app_slug
    default: "xdesk"
rename:
  - from: "xkitora"
    to: "{{ .nope }}"
`)
	fsys := fstest.MapFS{
		"xkitora/main.go": &fstest.MapFile{Data: []byte("x")},
	}
	_, _, err := Paths(fsys, c, Vars{"app_slug": "xdesk"})
	if err == nil {
		t.Fatal("rename.to 引用未声明变量应报错")
	}
}

func TestPathsRenameToContainingSlashRejected(t *testing.T) {
	// 值里含 '/' 会把一个路径段变成多级目录。事后的 validateDstPath
	// 抓不到（"x/a/b/y" 本身是干净路径），必须在替换后逐段检查。
	c := compile(t, `
manifest_version: 1
rename:
  - from: "xkitora"
    to: "a/b"
`)
	fsys := fstest.MapFS{
		"xkitora/main.go": &fstest.MapFile{Data: []byte("x")},
	}
	_, _, err := Paths(fsys, c, Vars{})
	if err == nil {
		t.Fatal("rename 替换值含 '/' 应报错，而不是把单段变成多级目录")
	}
}

func TestPathsPathVariableRendering(t *testing.T) {
	c := compile(t, `
manifest_version: 1
vars:
  - name: app_slug
    default: "xdesk"
`)
	fsys := fstest.MapFS{
		"{{ .app_slug }}/main.go": &fstest.MapFile{Data: []byte("x")},
	}
	entries, _, err := Paths(fsys, c, Vars{"app_slug": "xdesk"})
	if err != nil {
		t.Fatalf("Paths: %v", err)
	}
	if len(entries) != 1 || entries[0].Dst != "xdesk/main.go" {
		t.Errorf("路径变量未渲染: %v", dsts(entries))
	}
}

func TestPathsRejectsEmptyRenderedSegment(t *testing.T) {
	// 实测 {{ if .feat }}feat{{ end }}/main.go 在 feat 为空时得到 "/main.go"，
	// Clean 之后文件会静默挪到上一级
	c := compile(t, `
manifest_version: 1
vars:
  - name: feat
`)
	fsys := fstest.MapFS{
		"{{ if .feat }}feat{{ end }}/main.go": &fstest.MapFile{Data: []byte("x")},
	}
	_, _, err := Paths(fsys, c, Vars{"feat": ""})
	if err == nil {
		t.Fatal("渲染后为空路径段应报错，而不是静默把文件挪到上一级")
	}
}

func TestPathsRejectsSegmentContainingSlash(t *testing.T) {
	c := compile(t, `
manifest_version: 1
vars:
  - name: slug
`)
	fsys := fstest.MapFS{"{{ .slug }}/main.go": &fstest.MapFile{Data: []byte("x")}}
	_, _, err := Paths(fsys, c, Vars{"slug": "a/b"})
	if err == nil {
		t.Fatal("路径段含 / 应报错：一个段会变成多级目录")
	}
}

func TestPathsRejectsDotDotSegment(t *testing.T) {
	c := compile(t, "manifest_version: 1\nvars:\n  - name: slug\n")
	fsys := fstest.MapFS{"{{ .slug }}/main.go": &fstest.MapFile{Data: []byte("x")}}
	if _, _, err := Paths(fsys, c, Vars{"slug": ".."}); err == nil {
		t.Fatal(".. 段应报错")
	}
}

func TestPathsRejectsSymlink(t *testing.T) {
	c := compile(t, "manifest_version: 1\n")
	fsys := fstest.MapFS{
		"link": &fstest.MapFile{Mode: fs.ModeSymlink, Data: []byte("target")},
		"a.go": &fstest.MapFile{Data: []byte("x")},
	}
	_, _, err := Paths(fsys, c, Vars{})
	if err == nil {
		t.Fatal("symlink 必须报错拒绝，绝不跟随")
	}
}

func TestPathsBinaryAndExec(t *testing.T) {
	c := compile(t, "manifest_version: 1\n")
	fsys := fstest.MapFS{
		"bin.dat": &fstest.MapFile{Data: []byte{0x00, 0x01}},
		"run.sh":  &fstest.MapFile{Data: []byte("#!/bin/sh\n"), Mode: 0o755},
		"a.go":    &fstest.MapFile{Data: []byte("x"), Mode: 0o644},
	}
	entries, _, err := Paths(fsys, c, Vars{})
	if err != nil {
		t.Fatalf("Paths: %v", err)
	}
	if !find(t, entries, "bin.dat").Binary {
		t.Error("含 NUL 的文件应判为二进制")
	}
	if !find(t, entries, "run.sh").Exec {
		t.Error("0755 的文件应带 Exec")
	}
	if find(t, entries, "a.go").Exec {
		t.Error("0644 的文件不应带 Exec")
	}
}

func TestPathsGeneratedMatchesDst(t *testing.T) {
	c := compile(t, `
manifest_version: 1
generated:
  paths: ["frontend/bindings/**"]
`)
	fsys := fstest.MapFS{
		"frontend/bindings/a.js": &fstest.MapFile{Data: []byte("x")},
		"frontend/src/a.js":      &fstest.MapFile{Data: []byte("x")},
	}
	entries, _, err := Paths(fsys, c, Vars{})
	if err != nil {
		t.Fatalf("Paths: %v", err)
	}
	if !find(t, entries, "frontend/bindings/a.js").Generated {
		t.Error("generated 未标记")
	}
	if find(t, entries, "frontend/src/a.js").Generated {
		t.Error("非 generated 路径被误标")
	}
}

func TestPathsCollisionDetectionOnCaseFold(t *testing.T) {
	// Linux 上合法，macOS 上会静默互相覆盖
	c := compile(t, "manifest_version: 1\n")
	fsys := fstest.MapFS{
		"Makefile": &fstest.MapFile{Data: []byte("x")},
		"makefile": &fstest.MapFile{Data: []byte("y")},
	}
	_, _, err := Paths(fsys, c, Vars{})
	if err == nil {
		t.Fatal("折叠后同名的两条路径应报错（模板自身矛盾）")
	}
}

func TestPathsSortsByDst(t *testing.T) {
	c := compile(t, "manifest_version: 1\n")
	fsys := fstest.MapFS{
		"b.go": &fstest.MapFile{Data: []byte("x")},
		"a.go": &fstest.MapFile{Data: []byte("x")},
		"c.go": &fstest.MapFile{Data: []byte("x")},
	}
	entries, _, err := Paths(fsys, c, Vars{})
	if err != nil {
		t.Fatalf("Paths: %v", err)
	}
	got := dsts(entries)
	for i := 1; i < len(got); i++ {
		if got[i-1] > got[i] {
			t.Fatalf("输出未按 Dst 字节序排序: %v", got)
		}
	}
}
