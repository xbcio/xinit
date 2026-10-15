package main

import (
	_ "embed"
	"errors"
	"fmt"
	"os"

	"github.com/xbcio/xinit/internal/cli"
)

// changelog 是随二进制发布的变更日志，供 xinit changelog 使用。
// 嵌入指令只能引用本包目录及其下的文件，所以在根包嵌入后注入 cli 包。
//
//go:embed CHANGELOG.md
var changelog string

func main() {
	cli.Changelog = changelog
	if err := cli.Execute(); err != nil {
		var ee *cli.ExitError
		if errors.As(err, &ee) {
			if !ee.Silent {
				fmt.Fprintln(os.Stderr, "xinit:", ee)
			}
			os.Exit(ee.Code)
		}
		fmt.Fprintln(os.Stderr, "xinit:", err)
		os.Exit(1)
	}
}
