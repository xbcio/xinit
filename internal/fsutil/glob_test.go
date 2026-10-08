package fsutil

import (
	"strings"
	"testing"
)

// 这一组逐行对应规范 §3.2 的实测表格。
func TestNormalizePatternAnchoring(t *testing.T) {
	tests := []struct {
		name string // 原始写法
		in   string
		want string
	}{
		{"不含斜杠 → 任意深度", "*.go", "**/*.go"},
		{"不含斜杠 → 任意深度（go.mod）", "go.mod", "**/go.mod"},
		{"含斜杠 → 锚定在根", "docs/**", "docs/**"},
		{"含斜杠 → 锚定在根（build）", "build/**", "build/**"},
		{"含斜杠 → 锚定在根（node_modules）", "node_modules/**", "node_modules/**"},
		{"已是任意深度", "**/*.go", "**/*.go"},
		{"末尾斜杠 → X/**", "dist/", "dist/**"},
		{"前导斜杠 → 去掉后锚定", "/abs/x", "abs/x"},
		{"全匹配保持", "**", "**"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizePattern(tt.in); got != tt.want {
				t.Errorf("NormalizePattern(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestMatcherGitignoreAnchoring(t *testing.T) {
	tests := []struct {
		pattern string
		path    string
		want    bool
	}{
		{"*.go", "main.go", true},
		{"*.go", "a/main.go", true},
		{"go.mod", "sub/go.mod", true},
		{"docs/**", "docs", true},
		{"docs/**", "docs/a.md", true},
		{"node_modules/**", "a/node_modules/x", false},
		{"node_modules/**", "node_modules/x", true},
		{"**/*.go", "main.go", true},
		{"build/**", "frontend/build/x", false},
		{"dist/", "dist/a", true},
		{"dist/", "a/dist/x", false},
		{"/abs/x", "abs/x", true},
		{"**", "a/b/c", true},
	}
	for _, tt := range tests {
		t.Run(tt.pattern+"→"+tt.path, func(t *testing.T) {
			m, err := CompileGlob([]string{tt.pattern})
			if err != nil {
				t.Fatalf("CompileGlob: %v", err)
			}
			if got := m.Match(tt.path); got != tt.want {
				t.Errorf("%q.Match(%q) = %v, want %v", tt.pattern, tt.path, got, tt.want)
			}
		})
	}
}

func TestMatcherTemplatePathBraces(t *testing.T) {
	// 骨架模板的 Src 含 {{ }}。模板作者会直觉地写这种模式。
	m, err := CompileGlob([]string{"{{ .app_slug }}/**"})
	if err != nil {
		t.Fatalf("CompileGlob: %v", err)
	}
	if !m.Match("{{ .app_slug }}/x.go") {
		t.Error("含花括号的模式必须能匹配同名路径（转义作用于模式）")
	}
	if !m.Match("{{ .app_slug }}/deep/x.go") {
		t.Error("** 应跨目录")
	}
	if m.Match("other/x.go") {
		t.Error("不应匹配无关路径")
	}
}

func TestMatcherKeepsBraceAlternation(t *testing.T) {
	// 不含 {{/}} 的模式保留 doublestar 的选择语法
	m, err := CompileGlob([]string{"*.{go,ts}"})
	if err != nil {
		t.Fatalf("CompileGlob: %v", err)
	}
	if !m.Match("a/x.go") || !m.Match("a/x.ts") {
		t.Error("{go,ts} 选择语法应保留")
	}
	if m.Match("a/x.py") {
		t.Error("不应匹配 .py")
	}
}

func TestMatcherNegationOrder(t *testing.T) {
	m, err := CompileGlob([]string{"**/*.go", "!**/*_test.go"})
	if err != nil {
		t.Fatalf("CompileGlob: %v", err)
	}
	if !m.Match("a/x.go") {
		t.Error("应匹配普通 .go")
	}
	if m.Match("a/x_test.go") {
		t.Error("! 应排除测试文件（后者生效）")
	}
}

func TestMatcherReInclusion(t *testing.T) {
	// 后一条 ! 之后还能再包含
	m, err := CompileGlob([]string{"**/*.go", "!**/*_test.go", "a/x_test.go"})
	if err != nil {
		t.Fatalf("CompileGlob: %v", err)
	}
	if !m.Match("a/x_test.go") {
		t.Error("最后一条包含规则应生效")
	}
	if m.Match("b/x_test.go") {
		t.Error("其它目录的测试文件仍应被排除")
	}
}

func TestMatcherEmpty(t *testing.T) {
	m, err := CompileGlob(nil)
	if err != nil {
		t.Fatalf("CompileGlob(nil): %v", err)
	}
	if !m.IsEmpty() {
		t.Error("空模式列表应报告 IsEmpty")
	}
	if m.Match("anything") {
		t.Error("空 matcher 不应匹配任何东西")
	}
}

func TestCompileGlobRejectsEmptyPattern(t *testing.T) {
	if _, err := CompileGlob([]string{""}); err == nil {
		t.Error("空模式应报错")
	}
	if _, err := CompileGlob([]string{"!"}); err == nil {
		t.Error("只有 ! 的模式应报错")
	}
}

// TestCompileGlobRejectsInvalidPattern 钉住 F2：语法非法的 glob 模式
// 必须在编译期报错，而不是静默编译成一个永不命中的 matcher
// （doublestar.Match 对非法模式返回 err != nil，被 Match() 的
// err == nil && ok 吞掉）。
func TestCompileGlobRejectsInvalidPattern(t *testing.T) {
	invalid := []string{"[a", "{a,b", "a/[/b"}
	for _, p := range invalid {
		t.Run(p, func(t *testing.T) {
			if _, err := CompileGlob([]string{p}); err == nil {
				t.Errorf("非法模式 %q 应在编译期报错", p)
			}
		})
	}

	valid := []string{"{a,b}", "**/*.go", "{{ .v }}/**", "!pat"}
	for _, p := range valid {
		t.Run(p, func(t *testing.T) {
			if _, err := CompileGlob([]string{p}); err != nil {
				t.Errorf("合法模式 %q 不应报错: %v", p, err)
			}
		})
	}

	// 修复轮 1：! 取反前缀的非法模式报错时必须保留作者原文（含 !），
	// 不能打印剥离 ! 之后的形式 —— 否则作者看到的报错和自己写的不一致。
	t.Run("![a", func(t *testing.T) {
		_, err := CompileGlob([]string{"![a"})
		if err == nil {
			t.Fatal("非法模式 \"![a\" 应在编译期报错")
		}
		if !strings.Contains(err.Error(), "![a") {
			t.Errorf("错误信息应保留作者原文 \"![a\"（含 ! 前缀），实际: %v", err)
		}
	})
}
