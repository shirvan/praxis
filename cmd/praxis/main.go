package main

import (
	"os"

	"github.com/shirvan/praxis/internal/cli"
)

func main() {
	if err := cli.NewRootCmd().Execute(); err != nil {
		msg := err.Error()
		if cli.IsAuthErrorMessage(msg) {
			cli.FormatAuthError(msg)
		} else {
			cli.PrintError(msg)
		}
		os.Exit(1)
	}
}
