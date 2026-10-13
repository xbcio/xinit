package hooks

import (
	"fmt"
	"io"
)

// 来源：internal/cli/report.go 行 354-427（M0b 归位；I-2 实施批并入 T2）——
// 六个打印函数逐字搬迁，仅 printHookX → PrintX、包位 cli → hooks（§3-M0b
// 表指定目的地；§10.5/§10.6 文案单一来源）。切行复用本包 splitLines（与
// 原 hookBlockLines 同语义）。

// PrintNotice 打印信任通知的披露部分（§10.5）：钩子全文 + 可选的行级 diff。
// diff/oldVer 为空时省略 diff 段（首次出现、或旧清单不可得时，M3-R5）。
// 「操作指引 + 退出码 4 说明」拆到 PrintAsk：--yes 预先确认时只披露不指引
// （T4-R2——同一次运行里已确认，指引是失实信息）。
func PrintNotice(w io.Writer, name, block, oldVer, diff string) {
	fmt.Fprintf(w, "模板钩子 %s 需要确认（首次出现；内容变化时也会重新询问，§10.5）。\n", name)
	fmt.Fprint(w, "下列命令会以 sh -c 在项目根执行：\n\n")
	for _, ln := range splitLines(block) {
		fmt.Fprintf(w, "    %s\n", ln)
	}
	if diff != "" {
		fmt.Fprintf(w, "\n相对已记录版本 %s 的差异（- 已信任 / + 本次）：\n\n", oldVer)
		for _, ln := range splitLines(diff) {
			fmt.Fprintf(w, "    %s\n", ln)
		}
	}
}

// PrintAsk 打印未确认时的操作指引与退出码 4 说明（--yes 路径不打印）。
func PrintAsk(w io.Writer) {
	fmt.Fprint(w, "\n这是上游模板里的任意 shell 命令：确认前请检查上面每一行。\n"+
		"  · 信任并执行：在原命令后加 --yes（交互终端下也可回答 y）\n"+
		"  · 本轮跳过执行、且下次仍需确认：在原命令后加 --no-hooks\n"+
		"退出码 4 = 需要信任确认，本轮未做任何修改。\n")
}

// PrintRun 打印一次钩子执行的输出（无输出时不打印）。
func PrintRun(w io.Writer, name string, res Result) {
	if len(res.Output) == 0 {
		return
	}
	fmt.Fprintf(w, "%s 输出（退出码 %d）：\n", name, res.ExitCode)
	for _, ln := range splitLines(string(res.Output)) {
		fmt.Fprintf(w, "  %s\n", ln)
	}
}

// PrintOutput 打印报告侧的钩子输出行（update 路径专用；init 直接调
// PrintRun）。
func PrintOutput(w io.Writer, name string, lines []string, exitCode int) {
	if len(lines) == 0 {
		return
	}
	fmt.Fprintf(w, "%s 输出（退出码 %d）：\n", name, exitCode)
	for _, ln := range lines {
		fmt.Fprintf(w, "  %s\n", ln)
	}
}

// PrintFailure 打印失败行、失败命令全文与手工重跑方式（§10.2 :845）。
func PrintFailure(w io.Writer, name, block string, exitCode int) {
	fmt.Fprintf(w, "%s 失败（钩子退出码 %d；xinit 退出码 3）——本轮的文件与 state 已完成并保留（§10.2）。\n", name, exitCode)
	fmt.Fprint(w, "失败命令（sh -c，工作目录 = 项目根）：\n\n")
	for _, ln := range splitLines(block) {
		fmt.Fprintf(w, "    %s\n", ln)
	}
	fmt.Fprint(w, "\n修好问题后重跑即收敛；或手工执行上面的命令。\n")
}

// PrintSideEffects 打印副作用两节（§10.6，只报告、不回滚）。
func PrintSideEffects(w io.Writer, unplanned, modified []string) {
	if len(unplanned) > 0 {
		fmt.Fprintf(w, "计划外副作用（%d 条，钩子触碰了不属于计划内或 generated 的路径；§10.6）：\n", len(unplanned))
		for _, p := range unplanned {
			fmt.Fprintf(w, "  %-20s %s\n", "side-effect", p)
		}
	}
	if len(modified) > 0 {
		fmt.Fprintf(w, "计划内但被钩子改写（%d 条，下一轮可能被判「本地改过」的正是这些）：\n", len(modified))
		for _, p := range modified {
			fmt.Fprintf(w, "  %-20s %s\n", "hook-modified", p)
		}
	}
}
