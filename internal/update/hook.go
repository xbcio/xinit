package update

// 来源：internal/update/update.go 的 Run 块 31（M2-P11 runHook 抽取；I-2
// 实施批 T6/M2c）。阶段判据（§3-M2）：函数体与源区间逐字对应，差异仅「参数
// 接入」「返回值包装」与附加裁定类（R-M2c-1 +actions 窄参、R-M2c-2 恒非
// nil）——逐条见 task-6-report.md。

import (
	"fmt"

	"github.com/xbcio/xinit/internal/hooks"
	"github.com/xbcio/xinit/internal/plan"
)

// HookOutcome 是钩子执行结果（P11 写，report() 只读；§2.3）。
type HookOutcome struct {
	Failed    bool
	ExitCode  int
	Output    []string
	Unplanned []string
	Modified  []string
}

// runHook 是 P11（§2.2）：post_update 执行（块 31）。R-M2c-1：签名含
// actions 窄参——块 31 的 planned 循环消费 d.Actions，§2.2 三参签名闭包
// 不闭（缺口 5）；R-M2c-2：hooksRun == false 时返回零值 &HookOutcome{}
// （恒非 nil——骨架 verdictOf(w.Pending, h.Failed) 安全）。
func runHook(s *session, t *Template, actions []plan.Action, hooksRun bool) (*HookOutcome, error) {
	h := &HookOutcome{}
	if !hooksRun {
		return h, nil
	}
	// 18. post_update（§10.2 第 11 步）——在 state/base 落盘之后。文件已按
	// 本轮结果写完，钩子失败也只是退出码 3（文件与 state 保留，§10.2 :845）。
	before, err := hooks.Snapshot(s.dir)
	if err != nil {
		return nil, fmt.Errorf("钩子前快照: %w", err)
	}
	res, err := hooks.Run(s.dir, t.Manifest.Hooks.PostUpdate)
	if err != nil {
		return nil, err // sh 起不来：硬错误（§3.1 明确报错）
	}
	after, err := hooks.Snapshot(s.dir)
	if err != nil {
		return nil, fmt.Errorf("钩子后快照: %w", err)
	}
	h.ExitCode = res.ExitCode
	h.Output = hookBlockLines(string(res.Output))
	// planned = 本轮受管并集（Actions 全部路径；prune 与行 12 已被 Plan
	// 排除）；generated 取本轮模板侧位（M3-R6）。
	planned := make(map[string]bool, len(actions))
	for _, a := range actions {
		planned[a.Path] = true
	}
	generated := make(map[string]bool)
	for p, m := range t.Snap {
		if m.Generated {
			generated[p] = true
		}
	}
	h.Unplanned, h.Modified = hooks.Classify(before, after, planned, generated)
	if res.ExitCode != 0 {
		h.Failed = true
	}
	return h, nil
}
