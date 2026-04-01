// Package main is the entry point for the `praxis` CLI binary.
//
// The CLI is the primary human interface for Praxis. It connects to the
// Restate-backed Core service over HTTP (the Restate ingress) to execute
// commands like apply, plan, deploy, delete, and get.
//
// Architecture:
//
//	┌─────────┐       HTTP/JSON        ┌──────────────┐
//	│ praxis  │ ───────────────────────▷│ Restate      │
//	│  CLI    │    (ingress client)     │ ──▷ Core     │
//	└─────────┘                        │ ──▷ Drivers   │
//	                                   └──────────────┘
//
// The binary itself is thin: it constructs a Cobra root command from the cli
// package and delegates all work to sub-command handlers. Error formatting is
// handled here at the top level so sub-commands can simply return errors.
package main

import (
	"os"

	"github.com/shirvan/praxis/internal/cli"
)

// main constructs the Cobra command tree and executes the user's sub-command.
// If the command fails, it formats the error appropriately (special formatting
// for auth errors, generic formatting for everything else) and exits non-zero.
func main() {
	if err := cli.NewRootCmd().Execute(); err != nil {
		msg := err.Error()
		// Auth errors get special formatting with hints about credential configuration.
		if cli.IsAuthErrorMessage(msg) {
			cli.FormatAuthError(msg)
		} else {
			cli.PrintError(msg)
		}
		os.Exit(1)
	}
}
