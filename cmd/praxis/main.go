// Package main is the entry point for the `praxis` CLI binary.
//
// The CLI is the primary human interface for Praxis. It connects to the
// Restate-backed Core service over HTTP (the Restate ingress) to execute
// commands like apply, plan, deploy, delete, and get.
//
// Architecture:
//
//	┌─────────┐       HTTP/JSON        ┌──────────────┐
//	│ praxis  │ ──────────────────────▷│ Restate      │
//	│  CLI    │    (ingress client)    │ ──▷ Core     │
//	└─────────┘                        │ ──▷ Drivers  │
//	                                   └──────────────┘
//
// The binary itself is thin: it constructs a Cobra root command from the cli
// package and delegates all work to sub-command handlers. Error formatting is
// handled here at the top level so sub-commands can simply return errors.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/shirvan/praxis/internal/cli"
)

// main constructs the Cobra command tree and executes the user's sub-command.
// The root context is cancelled on SIGINT/SIGTERM so long-running commands
// (--wait, observe) shut down cleanly. If the command fails, cli.HandleError
// renders it (styled text, or a JSON envelope when -o json is active) and maps
// it to a stable exit code: 1 general, 3 not found, 4 validation, 5 conflict,
// 6 auth.
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := cli.NewRootCmd().ExecuteContext(ctx)
	stop()
	if err != nil {
		os.Exit(cli.HandleError(err))
	}
}
