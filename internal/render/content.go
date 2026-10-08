package render

import (
	"fmt"
	"io/fs"

	"github.com/xbcio/xinit/internal/fsutil"
	"github.com/xbcio/xinit/internal/manifest"
)

// Content 渲染单个 Entry 的内容（规范 §7.1 阶段 9）。
//
// 三条管线：
//   - 二进制 → 原样透传（不替换、不改 EOL）；
//   - ModeTemplate → Go template；
//   - ModeSubst → 两遍 span 替换。
//
// reportZeroHits 由调用方声明本次渲染是 theirs 还是 base 重建：零替换
// 告警只对 theirs 有意义，重建 base 时会合法地产生噪音。
//
// 不保证流式 —— span 法需要完整 []byte。
func Content(
	fsys fs.FS,
	c *manifest.Compiled,
	v Vars,
	e Entry,
	reportZeroHits bool,
) ([]byte, []Warning, error) {
	raw, err := fs.ReadFile(fsys, e.Src)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", e.Src, err)
	}

	if e.Binary {
		return raw, nil, nil
	}

	if e.Mode == ModeTemplate {
		out, err := RenderTemplate(e.Src, raw, v)
		if err != nil {
			return nil, nil, err
		}
		return out, nil, nil
	}

	// ModeSubst：先归一化，替换，再还原。
	//
	// 混合 EOL 的文件 ToLF 会原样返回、FromLF 也会原样返回，于是未替换的
	// 字节不受影响 —— 这正是把混合 EOL 单独归类的意义。
	eol := fsutil.DetectEOL(raw)
	text := fsutil.ToLF(raw)

	rules, err := buildRules(c, e.Src, v)
	if err != nil {
		return nil, nil, err
	}
	out, hits, warns := Apply(text, rules, c.Raw.ProtectedLiterals)

	var warnings []Warning
	for _, w := range warns {
		warnings = append(warnings, Warning{Path: e.Dst, Msg: w})
	}
	if reportZeroHits {
		for i, n := range hits {
			if n == 0 {
				warnings = append(warnings, Warning{
					Path: e.Dst,
					Msg: fmt.Sprintf("替换规则 %d (%q) 命中 0 次 —— 模板里可能已无此字面量",
						i, rules[i].From),
				})
			}
		}
	}

	return fsutil.FromLF(out, eol), warnings, nil
}

// buildRules 挑出作用于该路径的替换规则，并求值替换值。
//
// 替换值在这里就由 {{ .var }} 渲染好 —— 规范 §7.3 明确不支持捕获组与
// lookbehind，值只能用变量占位符构造。
//
// 渲染失败必须上抛：to 里写了 {{ .no_such_var }} 时若吞掉错误，规则会
// 保留原始占位符文本，于是把 "{{ .no_such_var }}" 原样写进派生项目。
func buildRules(c *manifest.Compiled, src string, v Vars) ([]Rule, error) {
	var rules []Rule
	for _, cs := range c.Subst {
		if !cs.Matcher.Match(src) {
			continue
		}
		to := cs.Spec.To
		if containsTemplateAction(to) {
			rendered, err := RenderTemplate(src, []byte(to), v)
			if err != nil {
				return nil, fmt.Errorf("substitute 规则的 to 渲染失败: %w", err)
			}
			to = string(rendered)
		}
		rules = append(rules, Rule{
			From:       cs.Spec.From,
			To:         to,
			PrecededBy: cs.PrecededBy,
			FollowedBy: cs.FollowedBy,
			Case:       cs.Case,
		})
	}
	return rules, nil
}
