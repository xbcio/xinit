package render

import (
	"fmt"
	"io/fs"

	"github.com/xbcio/xinit/internal/fsutil"
	"github.com/xbcio/xinit/internal/manifest"
)

// RuleHit 是一条替换规则在单个文件上的命中统计。
//
// Index 是规则在清单 substitute 里的序号（0 起），**不是过滤后的下标**：
// Content 只对作用于该文件的规则做替换，用过滤后的下标会让调用方的
// 零命中告警指错规则。
//
// 汇总与告警由调用方做 —— 规范 §7.4 的零替换告警是**按规则**的：一条
// 规则在全部文件上命中 0 次才值得告警；逐文件告警会在 paths: ["**"] 的
// 规则上刷出与文件数同量的噪音。
type RuleHit struct {
	Index int
	From  string
	Count int
}

// Content 渲染单个 Entry 的内容（规范 §7.1 阶段 9）。
//
// 三条管线：
//   - 二进制 → 原样透传（不替换、不改 EOL）；
//   - ModeTemplate → Go template；
//   - ModeSubst → 两遍 span 替换。
//
// 返回的 []RuleHit 供调用方跨文件汇总（零命中告警）；[]Warning 是 Apply
// 自身的告警（谓词窗口截断等）。
//
// 不保证流式 —— span 法需要完整 []byte。
func Content(
	fsys fs.FS,
	c *manifest.Compiled,
	v Vars,
	e Entry,
) ([]byte, []RuleHit, []Warning, error) {
	raw, err := fs.ReadFile(fsys, e.Src)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%s: %w", e.Src, err)
	}

	if e.Binary {
		return raw, nil, nil, nil
	}

	if e.Mode == ModeTemplate {
		out, err := RenderTemplate(e.Src, raw, v)
		if err != nil {
			return nil, nil, nil, err
		}
		return out, nil, nil, nil
	}

	// ModeSubst：先归一化，替换，再还原。
	//
	// 混合 EOL 的文件 ToLF 会原样返回、FromLF 也会原样返回，于是未替换的
	// 字节不受影响 —— 这正是把混合 EOL 单独归类的意义。
	eol := fsutil.DetectEOL(raw)
	text := fsutil.ToLF(raw)

	scoped, err := buildRules(c, e.Src, v)
	if err != nil {
		return nil, nil, nil, err
	}
	plain := make([]Rule, len(scoped))
	for i, s := range scoped {
		plain[i] = s.Rule
	}

	out, hits, warns := Apply(text, plain, c.Raw.ProtectedLiterals)

	var warnings []Warning
	for _, w := range warns {
		warnings = append(warnings, Warning{Path: e.Dst, Msg: w})
	}
	stats := make([]RuleHit, len(hits))
	for i, n := range hits {
		stats[i] = RuleHit{Index: scoped[i].Index, From: scoped[i].Rule.From, Count: n}
	}

	return fsutil.FromLF(out, eol), stats, warnings, nil
}

// scopedRule 是一条作用于当前文件的规则，外加它在清单里的序号。
type scopedRule struct {
	Rule  Rule
	Index int // 清单 substitute 里的序号
}

// buildRules 挑出作用于该路径的替换规则，并求值替换值。
//
// 替换值在这里就由 {{ .var }} 渲染好 —— 规范 §7.3 明确不支持捕获组与
// lookbehind，值只能用变量占位符构造。
//
// 渲染失败必须上抛：to 里写了 {{ .no_such_var }} 时若吞掉错误，规则会
// 保留原始占位符文本，于是把 "{{ .no_such_var }}" 原样写进派生项目。
func buildRules(c *manifest.Compiled, src string, v Vars) ([]scopedRule, error) {
	var rules []scopedRule
	for i, cs := range c.Subst {
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
		rules = append(rules, scopedRule{
			Index: i,
			Rule: Rule{
				From:       cs.Spec.From,
				To:         to,
				PrecededBy: cs.PrecededBy,
				FollowedBy: cs.FollowedBy,
				Case:       cs.Case,
			},
		})
	}
	return rules, nil
}
