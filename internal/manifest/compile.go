package manifest

import (
	"fmt"
	"regexp"

	"github.com/xbcio/xinit/internal/fsutil"
)

// RenderCase 是替换值的字母大小写变换。
type RenderCase uint8

const (
	CaseNone RenderCase = iota
	CaseLower
	CaseUpper
)

func (c RenderCase) String() string {
	switch c {
	case CaseLower:
		return "lower"
	case CaseUpper:
		return "upper"
	default:
		return "none"
	}
}

// CompiledSubst 是一条编译好的替换规则。
//
// PrecededBy / FollowedBy 只保留模式串，不回填捕获组 —— 规范 §7.3 明确
// 不支持 \1 反向引用与 lookbehind，值只能用 {{ .var }} 占位符构造。
//
// 这两个字段里存的是**已锚定**的编译结果，render 侧直接使用、不再二次
// 锚定。锚定放在这里是因为它要与「先单独校验」成对出现。
type CompiledSubst struct {
	Spec       SubstRule
	Matcher    *fsutil.Matcher
	PrecededBy *regexp.Regexp // nil = 无约束；已锚定为「窗口以 pat 结尾」
	FollowedBy *regexp.Regexp // nil = 无约束；已锚定为「窗口以 pat 开头」
	Case       RenderCase
}

// CompiledRender 是一条编译好的渲染规则。
type CompiledRender struct {
	Spec    RenderRule
	Matcher *fsutil.Matcher
}

// CompiledRename 是一条编译好的改名规则。
type CompiledRename struct {
	Spec RenameRule
}

// Compiled 是 Manifest 的可执行形式。matcher 与正则都构建好，
// 运行期不再做任何解析或编译。
type Compiled struct {
	Raw       *Manifest
	Exclude   *fsutil.Matcher
	Generated *fsutil.Matcher
	Subst     []CompiledSubst
	Render    []CompiledRender
	Rename    []CompiledRename
}

// Compile 构建 matcher 并编译全部正则。
//
// 「编译所有正则以在启动阶段报错」是不变量 5 的一部分：清单里一个打错的
// 正则如果拖到运行期才炸，可能已经写了一半文件。
//
// 结果缓存在 m.compiled：Parse 末端已经算过一遍，后续调用直接命中缓存，
// 不会重复编译。错误不缓存 —— 失败时每次重算，报错保持一致。
func (m *Manifest) Compile() (*Compiled, error) {
	if m.compiled != nil {
		return m.compiled, nil
	}
	if err := m.validateStructure(); err != nil {
		return nil, err
	}

	c := &Compiled{Raw: m}

	var err error
	if c.Exclude, err = fsutil.CompileGlob(m.Exclude.Paths); err != nil {
		return nil, fmt.Errorf("exclude.paths: %w", err)
	}
	if c.Generated, err = fsutil.CompileGlob(m.Generated.Paths); err != nil {
		return nil, fmt.Errorf("generated.paths: %w", err)
	}

	for i, r := range m.Substitute {
		where := fmt.Sprintf("substitute[%d]", i)
		cs := CompiledSubst{Spec: r}

		if cs.Matcher, err = fsutil.CompileGlob(r.Paths); err != nil {
			return nil, fmt.Errorf("%s.paths: %w", where, err)
		}
		if cs.PrecededBy, err = compilePrecededBy(r.WhenPrecededBy, where+".when_preceded_by"); err != nil {
			return nil, err
		}
		if cs.FollowedBy, err = compileFollowedBy(r.WhenFollowedBy, where+".when_followed_by"); err != nil {
			return nil, err
		}
		switch r.TargetCase {
		case "", "none":
			cs.Case = CaseNone
		case "lower":
			cs.Case = CaseLower
		case "upper":
			cs.Case = CaseUpper
		default:
			return nil, fmt.Errorf("%s.target_case: 只接受 none / lower / upper，实际 %q",
				where, r.TargetCase)
		}
		c.Subst = append(c.Subst, cs)
	}

	for i, r := range m.Render {
		where := fmt.Sprintf("render[%d]", i)
		cr := CompiledRender{Spec: r}
		if cr.Matcher, err = fsutil.CompileGlob(r.Paths); err != nil {
			return nil, fmt.Errorf("%s.paths: %w", where, err)
		}
		c.Render = append(c.Render, cr)
	}

	for _, r := range m.Rename {
		c.Rename = append(c.Rename, CompiledRename{Spec: r})
	}

	m.compiled = c
	return c, nil
}

// compilePrecededBy 编译前置谓词，表达「必须紧贴匹配之前结束」。
//
// RE2 没有 lookbehind，用 ^(?:.*)(?:pat)$ 表达「窗口以 pat 结尾」。已实测：
// 对含顶层交替的合法模式不产生语义泄漏（'a|b' 对窗口 "aZ" 为 false、对
// "Za" 为 true），贪婪 .* 也会正确回溯（'x+' 对 "aaaxxx" 为 true）。
//
// **必须先单独编译 pat，再编译包装后的形式，顺序不能反。** 实测
// 'x)|(?:y' 单独编译报错，但包装成 '^(?:.*)(?:x)|(?:y)$' 后 err==nil，
// 且对 "x and more" 匹配为 true —— 一个打错的谓词会静默匹配任意内容。
//
// $ 的语义可靠：Go 的 $ 在不带 (?m) 时等同 \z（文本末尾），不是 \Z。
func compilePrecededBy(pat, where string) (*regexp.Regexp, error) {
	if pat == "" {
		return nil, nil
	}
	if _, err := regexp.Compile(pat); err != nil {
		return nil, fmt.Errorf("%s: %w", where, err)
	}
	re, err := regexp.Compile("^(?:.*)(?:" + pat + ")$")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", where, err)
	}
	return re, nil
}

// compileFollowedBy 编译后置谓词，表达「必须从匹配之后立刻开始」。
//
// 同样先单独校验：^(?:pat) 这种包装也会让坏模式编译通过（实测 'x)|(?:y'
// 包装成 '^(?:x)|(?:y)' 后 err==nil）。两个函数的「先校验」不是防御性
// 冗余，是这两条包装路径唯一的拦截点。
func compileFollowedBy(pat, where string) (*regexp.Regexp, error) {
	if pat == "" {
		return nil, nil
	}
	if _, err := regexp.Compile(pat); err != nil {
		return nil, fmt.Errorf("%s: %w", where, err)
	}
	re, err := regexp.Compile("^(?:" + pat + ")")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", where, err)
	}
	return re, nil
}

// validateStructure 补 Strict 模式覆盖不到的结构约束。
//
// Strict 只保证键名已知、类型匹配。缩进错位后若结构仍合法、键名也都是
// 已知的，Strict 发现不了 —— 例如 paths 落进了相邻规则。
func (m *Manifest) validateStructure() error {
	seenVars := make(map[string]bool, len(m.Vars))
	for i, v := range m.Vars {
		where := fmt.Sprintf("vars[%d]", i)
		if !isGoIdentifier(v.Name) {
			return fmt.Errorf(
				"%s.name: %q 不是合法 Go 标识符；变量名要作为 text/template 的字段名使用，"+
					"只能是 [A-Za-z_][A-Za-z0-9_]*", where, v.Name)
		}
		if seenVars[v.Name] {
			return fmt.Errorf("%s.name: 变量 %q 重复声明", where, v.Name)
		}
		seenVars[v.Name] = true

		if v.Pattern != "" {
			if _, err := regexp.Compile(v.Pattern); err != nil {
				return fmt.Errorf("%s.pattern: %w", where, err)
			}
		}
	}

	for i, r := range m.Substitute {
		where := fmt.Sprintf("substitute[%d]", i)
		if r.From == "" {
			return fmt.Errorf(
				"%s.from: 不能为空 —— 空字面量会匹配到每个位置，产生无意义的全局改写", where)
		}
		if r.To == "" {
			return fmt.Errorf("%s.to: 不能为空（要删除内容请用 render 或单独的规则表达）", where)
		}
		if len(r.Paths) == 0 {
			return fmt.Errorf("%s.paths: 不能为空 —— 无路径范围的替换规则会改写整棵树", where)
		}
	}

	for i, r := range m.Render {
		if len(r.Paths) == 0 {
			return fmt.Errorf("render[%d].paths: 不能为空", i)
		}
	}

	// upstream.paths 非空校验（spec §20.5 清单点位）。
	//
	// 判据落在「键在场」上，而不是「列表为空」：未声明 upstream 键与
	// `upstream: {}` 都解出 nil 切片（合法缺省 —— 缺键是运行期判定，§20.5
	// 句 4 的 rc=1），只有显式 `paths: []` 解出**非 nil 空切片**（goccy
	// 实测）。声明了 upstream 却给空域会让域判定恒为空集、把全部改动判成
	// 域外，属结构错误。
	if m.Upstream.Paths != nil && len(m.Upstream.Paths) == 0 {
		return fmt.Errorf(
			"upstream.paths: 不能为空 —— 声明了 upstream 却没有路径模式，域判定会恒为空集（请列出共享面模式，或整个删掉 upstream 键）")
	}

	for i, r := range m.Rename {
		where := fmt.Sprintf("rename[%d]", i)
		if r.From == "" {
			return fmt.Errorf("%s.from: 不能为空", where)
		}
		if r.To == "" {
			return fmt.Errorf("%s.to: 不能为空", where)
		}
	}

	return nil
}

// isGoIdentifier 判定变量名是否是合法 Go 标识符。
func isGoIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_':
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}
