package cli

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"

	"github.com/spf13/cobra"
)

// gitVersionRe 容忍厂商后缀：2.50.1 (Apple Git-155)、2.44.0.windows.1
var gitVersionRe = regexp.MustCompile(`git version (\d+)\.(\d+)`)

const minGitMajor, minGitMinor = 2, 38

// RequireGit 检查 git 是否存在且不低于 2.38。
func RequireGit() error {
	out, err := exec.Command("git", "--version").Output()
	if err != nil {
		return fmt.Errorf("未找到 git，xinit 依赖 git ≥ %d.%d: %w", minGitMajor, minGitMinor, err)
	}
	m := gitVersionRe.FindSubmatch(out)
	if m == nil {
		return fmt.Errorf("无法解析 git 版本: %q", string(out))
	}
	major, _ := strconv.Atoi(string(m[1]))
	minor, _ := strconv.Atoi(string(m[2]))
	if major < minGitMajor || (major == minGitMajor && minor < minGitMinor) {
		return fmt.Errorf("git 版本过低: 需要 ≥ %d.%d，实际 %s", minGitMajor, minGitMinor, string(out))
	}
	return nil
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "xinit",
		Short:         "根据模板项目初始化新项目",
		Version:       versionString(),
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newInitCmd())
	root.AddCommand(newUpdateCmd())
	root.AddCommand(newDiffCmd())
	root.AddCommand(newStatusCmd())
	root.AddCommand(newCheckCmd())
	root.AddCommand(newAdoptCmd())
	root.AddCommand(newRestoreCmd())
	root.AddCommand(newBackflowCmd())
	root.AddCommand(newBackupCmd())
	root.AddCommand(newTemplateCmd())
	root.AddCommand(newChangelogCmd())
	return root
}

// annoNoGit 标在不碰 git 的命令上（如 changelog）：Execute 对它们跳过 RequireGit，
// 没装 git 的机器上也能用。
const annoNoGit = "xinit/no-git"

// Execute 是 main 的唯一入口。
func Execute() error {
	root := newRootCmd()
	if needsGit(root, os.Args[1:]) {
		if err := RequireGit(); err != nil {
			return err
		}
	}
	return root.Execute()
}

// needsGit 判断这次调用是否需要先检查 git：只有能确定落到带 annoNoGit 的
// 命令时才跳过，解析不了的一律按需要处理（保守）。
func needsGit(root *cobra.Command, args []string) bool {
	cmd, _, err := root.Find(args)
	if err != nil || cmd == nil {
		return true
	}
	_, skip := cmd.Annotations[annoNoGit]
	return !skip
}
