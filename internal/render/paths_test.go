package render

import (
	"io/fs"
	"strings"
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

func TestEvalVarsConfigValueIsLiteral(t *testing.T) {
	// --config 的值不再过模板：{{ }} 原样保留
	c := compile(t, `
manifest_version: 1
vars:
  - name: a
    default: "x"
  - name: b
`)
	v, err := EvalVars(c, map[string]string{"b": "{{ .a }}"})
	if err != nil {
		t.Fatalf("字面量含 {{ 不应报错: %v", err)
	}
	if v["b"] != "{{ .a }}" {
		t.Errorf("配置值被当模板执行了: %q", v["b"])
	}
}

func TestEvalVarsRejectsUndeclaredConfigKey(t *testing.T) {
	c := compile(t, "manifest_version: 1\nvars:\n  - name: a\n")
	_, err := EvalVars(c, map[string]string{"nope": "x"})
	if err == nil {
		t.Fatal("配置里出现清单未声明的变量应报错，而不是静默忽略")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("错误信息应指认变量名: %v", err)
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
	// rename 让 Src ≠ Dst：exclude 必须匹配**原路径**。
	// 若实现拿 Dst 匹配，"dist/**" 抓不到 Dst 为 "out/a.js" 的文件。
	c := compile(t, `
manifest_version: 1
exclude:
  paths: ["dist/**"]
rename:
  - from: "dist"
    to: "out"
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
		t.Errorf("exclude 必须匹配原 Src: %v", dsts(entries))
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
	// 链接必须指向**存在**的文件：否则删掉检查后 MapFS 也会
	// 因 "file does not exist" 报错，测试照样绿（空转夹具）。
	c := compile(t, "manifest_version: 1\n")
	fsys := fstest.MapFS{
		"link": {Mode: fs.ModeSymlink, Data: []byte("a.go")},
		"a.go": {Data: []byte("x")},
	}
	_, _, err := Paths(fsys, c, Vars{})
	if err == nil {
		t.Fatal("symlink 必须报错拒绝，绝不跟随")
	}
	if !strings.Contains(err.Error(), "link") {
		t.Errorf("错误必须指认这条 symlink，否则分不清是拒绝还是别的失败: %v", err)
	}
}

func TestPathsExcludedDirBlocksNegation(t *testing.T) {
	// 规范 §3.2：一旦目录被排除，其下的 ! 无法重新包含它
	c := compile(t, `
manifest_version: 1
exclude:
  paths: ["dist/**", "!dist/keep.js"]
`)
	fsys := fstest.MapFS{
		"a.go":         &fstest.MapFile{Data: []byte("x")},
		"dist/keep.js": &fstest.MapFile{Data: []byte("x")},
	}
	entries, _, err := Paths(fsys, c, Vars{})
	if err != nil {
		t.Fatalf("Paths: %v", err)
	}
	if len(entries) != 1 || entries[0].Dst != "a.go" {
		t.Errorf("被排除目录下的文件不得被 ! 重新包含: %v", dsts(entries))
	}
}

func TestPathsSymlinkInExcludedDirIsSkipped(t *testing.T) {
	// node_modules/.bin 里全是指向依赖包的链接；exclude 了它的模板
	// 不该因此整个失败 —— 被排除的路径根本不进入派生项目
	c := compile(t, `
manifest_version: 1
exclude:
  paths: ["node_modules/**"]
`)
	fsys := fstest.MapFS{
		"a.go":                  &fstest.MapFile{Data: []byte("x")},
		"node_modules/.bin/tsc": &fstest.MapFile{Mode: fs.ModeSymlink, Data: []byte("../tsc/bin")},
	}
	entries, _, err := Paths(fsys, c, Vars{})
	if err != nil {
		t.Fatalf("被排除目录里的 symlink 应被跳过而不是失败: %v", err)
	}
	if len(entries) != 1 || entries[0].Dst != "a.go" {
		t.Errorf("exclude 未生效: %v", dsts(entries))
	}
}

func TestPathsSymlinkedXinitYmlSkipped(t *testing.T) {
	// 隐式排除先于 symlink 检查：xinit.yml 从不输出，
	// 它的 symlink 变体应被跳过而不是让整个运行失败
	c := compile(t, "manifest_version: 1\n")
	fsys := fstest.MapFS{
		"xinit.yml": &fstest.MapFile{Mode: fs.ModeSymlink, Data: []byte("real.yml")},
		"a.go":      &fstest.MapFile{Data: []byte("x")},
	}
	entries, _, err := Paths(fsys, c, Vars{})
	if err != nil {
		t.Fatalf("xinit.yml 是 symlink 时应跳过而不是报错: %v", err)
	}
	if len(entries) != 1 || entries[0].Dst != "a.go" {
		t.Errorf("= %v", dsts(entries))
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
	// rename 让 Src ≠ Dst：generated 必须匹配**最终 Dst**。
	c := compile(t, `
manifest_version: 1
rename:
  - from: "frontend"
    to: "web"
generated:
  paths: ["web/bindings/**"]
`)
	fsys := fstest.MapFS{
		"frontend/bindings/a.js": &fstest.MapFile{Data: []byte("x")},
		"frontend/src/a.js":      &fstest.MapFile{Data: []byte("x")},
	}
	entries, _, err := Paths(fsys, c, Vars{})
	if err != nil {
		t.Fatalf("Paths: %v", err)
	}
	if !find(t, entries, "web/bindings/a.js").Generated {
		t.Error("generated 必须匹配最终 Dst")
	}
	if find(t, entries, "web/src/a.js").Generated {
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

func TestReplaceTokenRightLetterBoundary(t *testing.T) {
	// 右侧字母边界："xkitoraX" 不该改
	if got := replaceToken("xkitoraX", "xkitora", "xdesk"); got != "xkitoraX" {
		t.Errorf("= %q, want 原串（右侧是字母）", got)
	}
	// 同一串里两种情形并存，防止只测了整体
	if got := replaceToken("xkitora/xkitoraX", "xkitora", "xdesk"); got != "xdesk/xkitoraX" {
		t.Errorf("= %q, want %q", got, "xdesk/xkitoraX")
	}
}

func TestPathsVarsRenderBeforeRename(t *testing.T) {
	// 路径 vars 渲染在 rename **之前**：变量值里含 from 记号时也要被改名
	c := compile(t, `
manifest_version: 1
vars:
  - name: dir
rename:
  - from: "xkitora"
    to: "xdesk"
`)
	fsys := fstest.MapFS{"{{ .dir }}/main.go": &fstest.MapFile{Data: []byte("x")}}
	entries, _, err := Paths(fsys, c, Vars{"dir": "xkitora"})
	if err != nil {
		t.Fatalf("Paths: %v", err)
	}
	if len(entries) != 1 || entries[0].Dst != "xdesk/main.go" {
		t.Errorf("变量渲染必须先于 rename: %v", dsts(entries))
	}
}

func TestPathsRenderMatcherMatchesRawSrc(t *testing.T) {
	// render 判定用**原 Src**（strip 前）：rename 把 xkitora → xdesk 后，
	// 若拿改名后的路径匹配，"xkitora*" 就抓不到这个模板文件
	c := compile(t, `
manifest_version: 1
rename:
  - from: "xkitora"
    to: "xdesk"
render:
  - paths: ["xkitora*"]
    strip_suffix: ".tmpl"
`)
	fsys := fstest.MapFS{"xkitora.go.tmpl": &fstest.MapFile{Data: []byte("package {{ .p }}\n")}}
	entries, _, err := Paths(fsys, c, Vars{"p": "main"})
	if err != nil {
		t.Fatalf("Paths: %v", err)
	}
	if len(entries) != 1 || entries[0].Dst != "xdesk.go" || entries[0].Mode != ModeTemplate {
		t.Errorf("render 必须匹配原 Src: %+v", entries)
	}
}

func TestPathsRejectsFileDirCollision(t *testing.T) {
	// config.tmpl → config（文件），而 config/a.go 要求 config 是目录
	c := compile(t, `
manifest_version: 1
render:
  - paths: ["*.tmpl"]
    strip_suffix: ".tmpl"
`)
	fsys := fstest.MapFS{
		"config.tmpl": &fstest.MapFile{Data: []byte("x")},
		"config/a.go": &fstest.MapFile{Data: []byte("x")},
	}
	if _, _, err := Paths(fsys, c, Vars{}); err == nil {
		t.Fatal("文件与目录同名必须在渲染阶段报冲突，而不是写盘时 I/O 失败")
	}
}

func TestPathsSortsByDst(t *testing.T) {
	// 必须有 rename 让 Dst 序 ≠ WalkDir 的 Src 序：
	// Src 序是 xkitora.txt < y.txt，Dst 序是 y.txt < z.txt —— 相反
	c := compile(t, `
manifest_version: 1
rename:
  - from: "xkitora"
    to: "z"
`)
	fsys := fstest.MapFS{
		"xkitora.txt": &fstest.MapFile{Data: []byte("x")},
		"y.txt":       &fstest.MapFile{Data: []byte("x")},
	}
	entries, _, err := Paths(fsys, c, Vars{})
	if err != nil {
		t.Fatalf("Paths: %v", err)
	}
	if got := dsts(entries); len(got) != 2 || got[0] != "y.txt" || got[1] != "z.txt" {
		t.Fatalf("输出必须按 Dst 字节序排序: %v", got)
	}
}
