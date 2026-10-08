package cli

import (
	"errors"

	"github.com/spf13/cobra"
)

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "从模板生成新项目",
		RunE: func(cmd *cobra.Command, args []string) error {
			return errors.New("init 尚未实现")
		},
	}
}
