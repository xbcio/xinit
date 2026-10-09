package cli

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/xbcio/xinit/internal/manifest"
	"github.com/xbcio/xinit/internal/render"
)

// ResolveUpdateVars 求 update 的变量集合（§3.4 的演进规则、裁定 R15）：
//
//	--config > state.vars 冻结值 > 清单 default（default:"" 视为无）
//
// 与 init 的 render.EvalVars 的差异都是刻意的：
//   - 冻结值只要有键就算「有值」（含空串）——state.vars 里有记录
//     就说明变量不是本版新增的，新的 default 不该覆盖它；
//   - 新声明且三无 → 报错（EvalVars 在 init 时给空串——那时没有历史可依，
//     update 时静默取空串会在 base 重渲染里产生与上轮不一致的内容）；
//   - frozen 里清单已删除的变量静默忽略（§3.4：保留在 state.vars 中不报错，
//     保留动作由调用方在写 state 时做）。
func ResolveUpdateVars(c *manifest.Compiled, frozen, config map[string]string) (render.Vars, error) {
	vars := make(render.Vars, len(c.Raw.Vars))

	for _, spec := range c.Raw.Vars {
		val, have := config[spec.Name]
		if !have {
			if f, ok := frozen[spec.Name]; ok {
				val, have = f, true
			}
		}
		if !have && spec.Default != "" {
			val, have = spec.Default, true
			// default 可以引用先前声明的变量（与 EvalVars 同规则）；
			// --config/冻结值是字面量，不走渲染。
			if strings.Contains(val, "{{") {
				rendered, err := render.RenderTemplate("vars."+spec.Name, []byte(val), vars)
				if err != nil {
					return nil, fmt.Errorf("vars.%s.default: %w", spec.Name, err)
				}
				val = string(rendered)
			}
		}
		if !have {
			return nil, fmt.Errorf(
				"变量 %q 是本版模板新增的、无默认值；请用 --config 指定", spec.Name)
		}

		if spec.Required && val == "" {
			return nil, fmt.Errorf(
				"变量 %q 是必填项，但当前值为空；请用 --config 指定", spec.Name)
		}
		if spec.Pattern != "" && val != "" {
			re, err := regexp.Compile(spec.Pattern)
			if err != nil {
				return nil, fmt.Errorf("vars.%s.pattern: %w", spec.Name, err)
			}
			if !re.MatchString(val) {
				return nil, fmt.Errorf(
					"变量 %q 的当前值 %q 不匹配新版 pattern %q；请用 --config 修正",
					spec.Name, val, spec.Pattern)
			}
		}

		vars[spec.Name] = val
	}

	// --config 里给了清单未声明的变量：静默忽略会让用户以为生效了（同 EvalVars）
	for k := range config {
		if _, ok := vars[k]; !ok {
			return nil, fmt.Errorf("--config 里出现了清单未声明的变量 %q", k)
		}
	}

	return vars, nil
}
