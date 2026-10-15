package cli

import (
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

// Changelog 是 xinit 自身的变更日志全文（仓库根的 CHANGELOG.md）。
//
// 嵌入指令够不到上级目录，所以由根包 main.go 嵌入后在启动时注入；
// 为空表示本次构建没有内嵌（直接 go test 的非脚本路径等）。
var Changelog string

// changelogHeadingRe 匹配「## [版本]」二级标题，捕获方括号里的标签。
var changelogHeadingRe = regexp.MustCompile(`^## \[([^\]]+)\]`)

func newChangelogCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "changelog [版本]",
		Short: "显示 xinit 自身的变更日志",
		Long: "显示随本二进制内嵌的 xinit 变更日志；不联网、不读 git、不依赖当前目录。\n\n" +
			"不带参数输出全文；给出版本（1.0.0 或 v1.0.0，Unreleased 不区分大小写）\n" +
			"只输出该版本的章节。\n\n" +
			"模板自己的变更历史见 xinit template --changelog。",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			version := ""
			if len(args) == 1 {
				version = args[0]
			}
			text, err := changelogText(Changelog, version)
			if err != nil {
				return err
			}
			_, err = io.WriteString(cmd.OutOrStdout(), text)
			return err
		},
	}
}

// changelogText 返回 doc 全文（version 为空）或 version 对应的章节。
func changelogText(doc, version string) (string, error) {
	if doc == "" {
		return "", fmt.Errorf("本次构建未内嵌变更日志；请查看仓库里的 CHANGELOG.md")
	}
	if version == "" {
		return doc, nil
	}

	want := normalizeChangelogLabel(version)
	lines := strings.Split(doc, "\n")
	start := -1
	var labels []string
	for i, line := range lines {
		m := changelogHeadingRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		labels = append(labels, m[1])
		if start < 0 && normalizeChangelogLabel(m[1]) == want {
			start = i
		}
	}
	if start < 0 {
		return "", fmt.Errorf("变更日志里没有版本 %q；可用版本: %s", version, strings.Join(labels, ", "))
	}

	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			end = i
			break
		}
	}
	section := strings.TrimRight(strings.Join(lines[start:end], "\n"), "\n")
	return section + "\n", nil
}

// normalizeChangelogLabel 把版本标签归一：去空白与前导 v/V，转小写
// （1.0.0 / v1.0.0 / V1.0.0 等价，Unreleased / unreleased 等价）。
func normalizeChangelogLabel(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.TrimPrefix(s, "v")
}
