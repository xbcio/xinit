package cli

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/xbcio/xinit/internal/manifest"
	"github.com/xbcio/xinit/internal/render"
)

// 本文件是 §19.3 的交互式 vars 采集：只消费已声明的 VarSpec（prompt / default /
// pattern / required），不引入任何新语义——回答并入 provided 后仍走既有
// render.EvalVars（render 保持纯核心，不变量 6）。

// renderDefault 渲染某个变量的提示值（§19.3「提示值来源」）：
// 与 EvalVars 同一条判断——值含 `{{` 才渲染，根数据是已确定的前序值。
// ok=false 表示「无默认值可显示」：空默认值，或渲染失败（失败不新开错误
// 路径——提示降级、判定留给循环结束后的 EvalVars，同一文案同一时机）。
func renderDefault(spec manifest.VarSpec, prior map[string]string) (string, bool) {
	val := spec.Default
	if val == "" {
		return "", false
	}
	if strings.Contains(val, "{{") {
		rendered, err := render.RenderTemplate("vars."+spec.Name, []byte(val), render.Vars(prior))
		if err != nil {
			return "", false
		}
		val = string(rendered)
	}
	return val, val != ""
}

// varPrompt 是 V1–V3 三态（互斥完备；V1 仅在默认值**非空**时使用）。
func varPrompt(spec manifest.VarSpec, hint string, hintOK bool) string {
	switch {
	case hintOK:
		return fmt.Sprintf("%s [%s]: ", spec.Prompt, hint)
	case spec.Required:
		return fmt.Sprintf("%s（必填）: ", spec.Prompt)
	default:
		return fmt.Sprintf("%s（可留空）: ", spec.Prompt)
	}
}

// effectiveVal 是 §19.3 采集循环里的取值规则：空答 ⇒ 取 default（即提示值；
// 无 default 时为空串），非空答 ⇒ 字面量（不渲染模板，与 --config 同口径）。
func effectiveVal(line, hint string) string {
	if line == "" {
		return hint
	}
	return line
}

// reaskReason 是 §19.3 的重问判据，与 EvalVars 逐字对齐：返回非空串即重问并
// 打印它（V4 / V5）。判据作用于**生效值**（空答时即 default）——故 default 不满足
// pattern 时同样重问，与 EvalVars 对 default 的校验口径一致。
func reaskReason(spec manifest.VarSpec, val string) string {
	if spec.Pattern != "" && val != "" {
		re, err := regexp.Compile(spec.Pattern)
		if err != nil {
			return "" // 编译失败由 EvalVars 报（vars.<name>.pattern，同一文案）
		}
		if !re.MatchString(val) {
			return fmt.Sprintf("变量 %q 的值 %q 不匹配 pattern %q；请重答（§19.3）\n", spec.Name, val, spec.Pattern)
		}
	}
	if spec.Required && val == "" {
		return fmt.Sprintf("变量 %q 是必填项且无默认值；请重答（§19.3）\n", spec.Name)
	}
	return ""
}

// askVar 对单个变量提问（含无上限重问循环，§19.3）。返回生效值与本次回答原文
// （空串 = 空答，调用方据此决定是否入 answers 表）。
// 不会死循环：每次重问消费一行；输入耗尽后 askLine 的 EOF 锁存使下一轮立即
// 返回 eof（且不再打印提示），转 V6 中止。
func askVar(spec manifest.VarSpec, hint string, hintOK bool, ps *promptSession) (string, string, error) {
	for {
		line, eof, err := ps.askLine(varPrompt(spec, hint, hintOK))
		if err != nil {
			return "", "", fmt.Errorf("变量 %q 的交互采集失败: %w", spec.Name, err) // V7
		}
		if line == "" && eof { // 半行内容有效（§19.2.3）；仅归一后为空才算 EOF
			return "", "", fmt.Errorf("变量 %q 的交互采集未完成：输入已结束（EOF）；本轮未做任何修改；非交互场景请用 --config 指定（§19.3）", spec.Name) // V6
		}
		val := effectiveVal(line, hint)
		if reason := reaskReason(spec, val); reason != "" {
			fmt.Fprint(ps.out, reason) // V4 / V5：打印理由后回到循环开头（再次打印提示行）
			continue
		}
		return val, line, nil
	}
}

// collectVars 按声明顺序对「声明了 prompt 且 --config 未提供」的变量提问；
// 返回要并入 provided 的回答（空答不入表 ⇒ EvalVars 走 default 分支）。
// ps == nil（非交互）时直接返回空表：零提问、零输出、零 stdin 读取。
func collectVars(c *manifest.Compiled, config map[string]string, ps *promptSession) (map[string]string, error) {
	answers := map[string]string{}
	if ps == nil {
		return answers, nil
	}
	collected := map[string]string{}
	for _, spec := range c.Raw.Vars {
		if v, ok := config[spec.Name]; ok { // --config 已给：不问、不重问（不变量 5）
			collected[spec.Name] = v
			continue
		}
		hint, hintOK := renderDefault(spec, collected)
		if spec.Prompt == "" { // 未声明 prompt：不问
			collected[spec.Name] = hint
			continue
		}
		val, line, err := askVar(spec, hint, hintOK, ps)
		if err != nil {
			return nil, err
		}
		collected[spec.Name] = val
		if line != "" {
			answers[spec.Name] = val
		}
	}
	return answers, nil
}
