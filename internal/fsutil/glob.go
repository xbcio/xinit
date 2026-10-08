package fsutil

import (
	"fmt"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// Matcher 是按序求值的 glob 列表，支持 ! 取反（gitignore 语义）。
type Matcher struct {
	rules []globRule
}

type globRule struct {
	pat string
	neg bool
}

// CompileGlob 编译模式列表。空列表得到不匹配任何东西的 matcher。
func CompileGlob(patterns []string) (*Matcher, error) {
	m := &Matcher{}
	for _, p := range patterns {
		neg := false
		if strings.HasPrefix(p, "!") {
			neg = true
			p = p[1:]
		}
		if strings.TrimSpace(p) == "" {
			return nil, fmt.Errorf("glob 模式不能为空")
		}
		pat := EscapePattern(NormalizePattern(p))
		if !doublestar.ValidatePattern(pat) {
			return nil, fmt.Errorf(
				"glob 模式 %q 语法错误（归一化后 %q 无法解析）", p, pat)
		}
		m.rules = append(m.rules, globRule{pat: pat, neg: neg})
	}
	return m, nil
}

// IsEmpty 报告是否没有任何规则。
func (m *Matcher) IsEmpty() bool { return m == nil || len(m.rules) == 0 }

// Match 按序求值：最后一条命中的规则决定结果。都不命中则为 false。
//
// 路径不做任何转义 —— doublestar 只解释模式。实测转义路径完全无效。
func (m *Matcher) Match(p string) bool {
	if m == nil {
		return false
	}
	matched := false
	for _, r := range m.rules {
		if ok, err := doublestar.Match(r.pat, p); err == nil && ok {
			matched = !r.neg
		}
	}
	return matched
}

// NormalizePattern 把 gitignore 风格的模式翻译成 doublestar 能正确处理的
// 形式。实测 doublestar 原生行为与 gitignore 有四处不同：
//
//	"*.go"            vs "a/main.go"          → false  （gitignore 是 true）
//	"go.mod"          vs "sub/go.mod"         → false  （gitignore 是 true）
//	"node_modules/**" vs "a/node_modules/x"   → false  （gitignore 是 true）
//	"docs/**"         vs "docs"               → true   （含同名普通文件）
//
// 规则：
//   - 末尾 / → 改写成 X/**；
//   - 不含 / → 前加 **/，匹配任意深度；
//   - 含 / → 去掉前导 / 后锚定在根。
//
// 最后一条是关键：若一律前加 **/，示例清单里的 paths: ["go.mod",
// "build/**"] 会把 frontend/build/** 和 sub/go.mod 也改写。
func NormalizePattern(p string) string {
	if strings.HasSuffix(p, "/") {
		p = strings.TrimSuffix(p, "/") + "/**"
	}
	if p == "**" {
		return p
	}
	if !strings.Contains(p, "/") {
		return "**/" + p
	}
	return strings.TrimPrefix(p, "/")
}

// EscapePattern 在模式含 "{{" 或 "}}" 时把整条模式的花括号转义为字面量。
//
// 必需，不是防御性的：doublestar 把模式里的 {{ 当花括号选择语法展开，于是
// "{{ .app_slug }}/**" 匹配不到 "{{ .app_slug }}/x.go"，返回 false 且不报错
// —— 而骨架模板的 Src 恰好含 {{ }}，模板作者会直觉地写这种模式。
//
// 只在模式含 {{/}} 时才整条转义，其余模式保留 {a,b} 选择语法。代价是两者
// 不能混用（"{a,b}/{{ .v }}" 会失去选择语法）—— 拆成两条规则即可。
func EscapePattern(p string) string {
	if !strings.Contains(p, "{{") && !strings.Contains(p, "}}") {
		return p
	}
	var b strings.Builder
	b.Grow(len(p) + 8)
	for _, r := range p {
		if r == '{' || r == '}' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
