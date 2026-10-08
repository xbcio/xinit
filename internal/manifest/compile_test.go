package manifest

import (
	"strings"
	"testing"
)

func compileFrom(t *testing.T, src string) *Compiled {
	t.Helper()
	m, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return c
}

func TestCompileBuildsMatchers(t *testing.T) {
	c := compileFrom(t, `
manifest_version: 1
exclude:
  paths: ["dist/**"]
`)
	if !c.Exclude.Match("dist/a.js") {
		t.Error("exclude matcher 未生效")
	}
	if c.Exclude.Match("src/a.go") {
		t.Error("exclude 不应匹配 src")
	}
	if !c.Generated.IsEmpty() {
		t.Error("未声明 generated 时应为空 matcher")
	}
}

// TestCompileRejectsInvalidExcludeGlob 钉住 F2 在 manifest 包的接线：
// exclude.paths 里语法非法的 glob 必须在 Parse 阶段就报错（Parse 末端
// 调 Compile，启动期拦截——不变量 5 精神），不能拖到渲染期才静默
// 永不命中。
func TestCompileRejectsInvalidExcludeGlob(t *testing.T) {
	_, err := Parse([]byte(`
manifest_version: 1
exclude:
  paths: ["[bad"]
`))
	if err == nil {
		t.Fatal("exclude.paths 含语法非法的 glob 应在 Parse 阶段报错")
	}
}

// TestCompileRejectsInvalidSubstituteGlob 同上，覆盖 substitute[].paths
// 这条独立的编译路径。
func TestCompileRejectsInvalidSubstituteGlob(t *testing.T) {
	_, err := Parse([]byte(`
manifest_version: 1
substitute:
  - from: "x"
    to: "y"
    paths: ["[bad"]
`))
	if err == nil {
		t.Fatal("substitute[].paths 含语法非法的 glob 应在 Parse 阶段报错")
	}
}

func TestCompileRejectsEmptyFrom(t *testing.T) {
	// 空字面量会匹配到每个位置，产生无意义的全局改写
	_, err := Parse([]byte("manifest_version: 1\nsubstitute:\n  - from: \"\"\n    to: x\n"))
	if err == nil {
		t.Fatal("空的 from 应报错")
	}
}

func TestCompileRejectsInvalidVariableName(t *testing.T) {
	tests := []struct{ name, varName string }{
		{"含连字符", "app-slug"},
		{"以数字开头", "1app"},
		{"含点", "app.slug"},
		{"空", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := "manifest_version: 1\nvars:\n  - name: \"" + tt.varName + "\"\n"
			_, err := Parse([]byte(src))
			if err == nil {
				t.Fatalf("变量名 %q 不是合法 Go 标识符，应报错", tt.varName)
			}
			if !strings.Contains(err.Error(), "标识符") {
				t.Errorf("错误信息应说明标识符要求: %v", err)
			}
		})
	}
}

func TestCompileRejectsInvalidRegexWithPosition(t *testing.T) {
	src := `
manifest_version: 1
vars:
  - name: a
    pattern: '[unclosed'
`
	_, err := Parse([]byte(src))
	if err == nil {
		t.Fatal("非法正则应报错")
	}
	if !strings.Contains(err.Error(), "vars[0]") {
		t.Errorf("错误信息应带位置，实际: %v", err)
	}
}

func TestCompileValidatesPredicateBeforeWrapping(t *testing.T) {
	// 'x)|(?:y' 单独编译失败，但包装成 '(?:x)|(?:y)\z' 后能通过 ——
	// 不先单独校验就会静默匹配任意内容
	src := `
manifest_version: 1
substitute:
  - from: a
    to: b
    paths: ["**"]
    when_preceded_by: 'x)|(?:y'
`
	_, err := Parse([]byte(src))
	if err == nil {
		t.Fatal("语法错误的谓词必须报错，哪怕包装后能编译")
	}
	if !strings.Contains(err.Error(), "substitute[0]") {
		t.Errorf("错误信息应带位置: %v", err)
	}
}

func TestCompileRejectsInvalidTargetCase(t *testing.T) {
	src := `
manifest_version: 1
substitute:
  - from: a
    to: b
    paths: ["**"]
    target_case: shouty
`
	_, err := Parse([]byte(src))
	if err == nil {
		t.Fatal("未知 target_case 应报错")
	}
}

func TestCompileRejectsNegativeLookbehind(t *testing.T) {
	// Go RE2 不支持 lookbehind —— 要在启动阶段报错，不能到运行期才发现
	src := `
manifest_version: 1
substitute:
  - from: a
    to: b
    paths: ["**"]
    when_preceded_by: '(?<!x)y'
`
	_, err := Parse([]byte(src))
	if err == nil {
		t.Fatal("lookbehind 在 RE2 里不合法，应报错")
	}
}

func TestCompileRejectsRuleWithoutFromOrTo(t *testing.T) {
	_, err := Parse([]byte("manifest_version: 1\nsubstitute:\n  - paths: [\"**\"]\n"))
	if err == nil {
		t.Fatal("缺 from 的规则应报错")
	}
}
