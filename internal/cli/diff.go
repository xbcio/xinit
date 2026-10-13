package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/xbcio/xinit/internal/update"
)

func newDiffCmd() *cobra.Command {
	var o UpdateOptions
	cmd := &cobra.Command{
		Use:   "diff",
		Short: "预览 update 会做什么（不落盘、不改状态）",
		Long: "走与 update 完全相同的代码路径，但不写任何文件、不推进任何状态。\n\n" +
			"退出码：有非 Noop 动作（含冲突、折叠拒绝/障碍物的预测）→ 5；全部 Noop → 0。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			o.ProjectDir = "."
			return Diff(o)
		},
	}
	cmd.Flags().StringVar(&o.Revision, "revision", "", "目标版本（默认取模板最新的 semver tag）")
	cmd.Flags().StringVar(&o.ConfigPath, "config", "", "变量文件（扁平 YAML）")
	cmd.Flags().BoolVar(&o.Fetch, "fetch", false, "联网更新模板镜像缓存")
	return cmd
}

// Diff 是 update 的只读镜像：同一条管线、同一份报告，只换模式（报告头由
// Mode 派生，§2.4——cli 不再事后覆盖 Header）。
// 只读 ⇒ 不加锁（R12）；退出码 5/0（§11.5，不模拟 2）。
func Diff(o UpdateOptions) error {
	o.DryRun = true
	o.mode = update.ModeDiff
	rep, err := runReport(o)
	if err != nil {
		return err
	}
	rep.Print(os.Stdout)
	if code := codeOf(rep.Verdict); code != CodeOK {
		return &ExitError{
			Code:   code,
			Silent: true,
			msg:    fmt.Sprintf("diff 以退出码 %d 结束", code),
		}
	}
	return nil
}
