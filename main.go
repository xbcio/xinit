package main

import (
	"fmt"
	"os"

	"github.com/xbcio/xinit/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "xinit:", err)
		os.Exit(1)
	}
}
