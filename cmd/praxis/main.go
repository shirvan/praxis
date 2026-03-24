package main

import (
	"fmt"
	"os"

	"github.com/shirvan/praxis/internal/cli"
)

func main() {
	if err := cli.NewRootCmd().Execute(); err != nil {
		msg := err.Error()
		if cli.IsAuthErrorMessage(msg) {
			cli.FormatAuthError(msg)
		} else {
			fmt.Fprintln(os.Stderr, msg)
		}
		os.Exit(1)
	}
}
