package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/xbcio/xinit/internal/cli"
)

func main() {
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
