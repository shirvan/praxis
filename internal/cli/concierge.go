// concierge.go contains helpers and the admin-only `praxis concierge`
// command group for Slack gateway management.
//
// The Concierge is an AI-powered infrastructure assistant backed by an LLM.
// User-facing operations are exposed as top-level verbs:
//   - `praxis ask`     — Send a prompt (ask.go)
//   - `praxis approve` — Approve or reject a pending action (approve_cmd.go)
//   - `praxis get concierge`  — Show session status (get.go)
//   - `praxis list concierge` — Show conversation history (list.go)
//   - `praxis set concierge`  — Configure the LLM provider (set.go)
//   - `praxis delete concierge` — Clear a session (delete.go)
//
// The concierge service is optional — if not deployed, all commands print a
// friendly unavailable message.
package cli

import (
	_ "embed"
	"strings"

	"github.com/spf13/cobra"
)

// conciergeUnavailableMsg is an embedded text file shown when the concierge
// service is not registered with Restate (connection refused or service not found).
//
//go:embed prompts/concierge_unavailable.txt
var conciergeUnavailableMsg string

// newConciergeAdminCmd builds the `praxis concierge` parent command.
// After the verb-first migration, this only contains the Slack gateway
// subcommand group — all other operations are top-level verbs.
func newConciergeAdminCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "concierge",
		Short: "Concierge admin operations",
		Long: `Admin operations for the Praxis Concierge AI assistant.

The Slack gateway integration is managed here:

    praxis concierge slack configure [flags]
    praxis concierge slack get-config
    praxis concierge slack allowed-users list|add|remove|set
    praxis concierge slack watch add|list|remove|update

For user-facing operations, use the top-level verbs:

    praxis ask <prompt>          Send a prompt
    praxis approve               Approve or reject a pending action
    praxis get concierge         Show session status
    praxis list concierge        Show conversation history
    praxis set concierge         Configure the LLM provider
    praxis delete concierge      Clear a session`,
	}

	cmd.AddCommand(
		newConciergeSlackCmd(flags),
	)

	return cmd
}

// isConciergeUnavailable checks whether the error indicates the concierge
// service is not registered with Restate.
func isConciergeUnavailable(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "service not found") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "no such host")
}
