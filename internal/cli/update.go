package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/xbcio/xinit/internal/update"
)

// UpdateOptions 是 update 的全部输入。R13：作用于 cwd（无位置参数）。
type UpdateOptions struct {
	ProjectDir  string
	Revision    string
	ConfigPath  string
	Fetch       bool
	Force       bool
	DryRun      bool
	NoHooks     bool
	Yes         bool
	Interactive bool     // --interactive：即使 stdin 不是终端也按可交互处理（§19.2.2）
	Accept      []string // --accept <路径>...：声明这些冲突已解决（§10.3）

	mode update.Mode // diff 入口专用（非 flag）：报告头派生的 Mode 来源
}

func newUpdateCmd() *cobra.Command {
	var o UpdateOptions
	cmd := &cobra.Command{
		Use:   "update",
		Short: "把派生项目同步到模板的新版本",
		Long: "按模板的 xinit.yml 与 .xinit/ 基线（base）做三方比较，把模板变更同步到当前目录。\n\n" +
			"冲突路径写入 .xinit/conflicts/，解决后重跑即收敛；\n" +
			"计划内路径的原文备份在 .xinit/backup/<时间戳>/。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			o.ProjectDir = "."
			return Update(o)
		},
	}
	cmd.Flags().StringVar(&o.Revision, "revision", "", "目标版本（默认取模板最新的 semver tag）")
	cmd.Flags().StringVar(&o.ConfigPath, "config", "", "变量文件（扁平 YAML；补齐新增或失效的 vars）")
	cmd.Flags().BoolVar(&o.Fetch, "fetch", false, "联网更新模板镜像缓存")
	cmd.Flags().BoolVar(&o.Force, "force", false, "跳过脏工作树检查")
	cmd.Flags().BoolVar(&o.NoHooks, "no-hooks", false, "跳过 post_update 执行（仍记录本次内容，下次带钩子运行仍需确认）")
	cmd.Flags().BoolVar(&o.Yes, "yes", false, "跳过钩子信任确认（等同于在交互终端回答 y）")
	cmd.Flags().BoolVar(&o.Interactive, "interactive", false, "即使 stdin 不是终端也按可交互处理：交互提问（含信任确认）从 stdin 逐行读取（供脚本与 CI 驱动，§19.2）")
	cmd.Flags().BoolVar(&o.DryRun, "dry-run", false, "只输出动作列表，不落盘、不改状态")
	cmd.Flags().StringArrayVar(&o.Accept, "accept", nil,
		"声明这些冲突路径已手工解决：把 .xinit/conflicts/<路径> 的内容记为 base（工具无法验证，§10.3）")
	return cmd
}

// Update 执行一次 update，打印报告，并把非零退出码包进 ExitError。
func Update(o UpdateOptions) error {
	o.mode = update.ModeUpdate // 显式置位，不依赖 Mode 零值（账本口径②）
	rep, err := runReport(o)
	if err != nil {
		return err
	}
	if code := codeOf(rep.Verdict); code == CodeNeedTrust {
		// 信任通知已由第 12 步直接打印；本轮没有执行任何动作，不打印动作报告。
		return &ExitError{Code: code, Silent: true, msg: "update 需要信任确认（exit 4）"}
	}
	rep.Print(os.Stdout)
	if code := codeOf(rep.Verdict); code != CodeOK {
		return &ExitError{
			Code:   code,
			Silent: true, // 报告已写全，main 不再补行
			msg:    fmt.Sprintf("update 以退出码 %d 结束", code),
		}
	}
	return nil
}

// inputOf 把 cli 的 flag 载体映射为 update.Input（§2.7 #3）。
func inputOf(o UpdateOptions) update.Input {
	in := update.Input{
		ProjectDir:    o.ProjectDir,
		Revision:      o.Revision,
		ConfigPath:    o.ConfigPath,
		Fetch:         o.Fetch,
		Force:         o.Force,
		DryRun:        o.DryRun,
		NoHooks:       o.NoHooks,
		Yes:           o.Yes,
		Accept:        o.Accept,
		Mode:          o.mode,
		EngineVersion: EngineVersion, // §2.7 #13：cli 为唯一真源，经此注入
	}
	// 交互会话（§19.2.2/§19.2.3）：入口创建一次；创建只做一次 ioctl 判定
	//（不读 stdin、不打印）。注意 typed-nil：nil 会话不得装入接口——否则
	// 管线会把「不可交互」看成「可交互」。
	if ps := interactiveSession(o.Interactive); ps != nil {
		in.Interactive = ps
	}
	return in
}

// runReport 是 update/diff 与 cli 测试的共同入口：一行转发，不含任何逻辑（§2.3）。
func runReport(o UpdateOptions) (*update.Report, error) { return update.Run(inputOf(o)) }

// codeOf 是 Verdict → §11.5 退出码的单点映射（§2.3 的 5 行表）。
func codeOf(v update.Verdict) int {
	switch v {
	case update.VerdictPending:
		return CodePending
	case update.VerdictHookFail:
		return CodeHookFail
	case update.VerdictNeedTrust:
		return CodeNeedTrust
	case update.VerdictChanged:
		return CodeChanged
	}
	return CodeOK
}
