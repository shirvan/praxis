package cli

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

//go:embed prompts/concierge_unavailable.txt
var conciergeUnavailableMsg string

func newConciergeCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "concierge",
		Short: "AI-powered infrastructure assistant",
		Long: `Interact with the Praxis Concierge — an AI assistant that can answer
questions about your infrastructure, plan changes, and execute operations
with human-in-the-loop approval for destructive actions.

The concierge requires a running praxis-concierge service and a configured
LLM provider. See 'praxis concierge configure --help' for setup.`,
	}

	cmd.AddCommand(
		newConciergeAskCmd(flags),
		newConciergeConfigureCmd(flags),
		newConciergeStatusCmd(flags),
		newConciergeHistoryCmd(flags),
		newConciergeResetCmd(flags),
		newConciergeApproveCmd(flags),
		newConciergeSlackCmd(flags),
	)

	return cmd
}

func newConciergeAskCmd(flags *rootFlags) *cobra.Command {
	var (
		session   string
		account   string
		workspace string
	)

	cmd := &cobra.Command{
		Use:   "ask <prompt>",
		Short: "Send a prompt to the concierge",
		Long:  `Send a natural language prompt to the concierge AI assistant.`,
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			prompt := strings.Join(args, " ")
			if session == "" {
				session = "default"
			}
			if account == "" {
				account = flags.account
			}

			client := flags.newClient()
			req := conciergeAskRequest{
				Prompt:    prompt,
				Account:   account,
				Workspace: workspace,
				Source:    "cli",
			}

			resp, err := client.ConciergeAsk(context.Background(), session, req)
			if err != nil {
				if isConciergeUnavailable(err) {
					fmt.Fprint(os.Stderr, conciergeUnavailableMsg)
					return nil
				}
				return fmt.Errorf("concierge ask: %w", err)
			}

			if flags.outputFormat() == OutputJSON {
				return json.NewEncoder(os.Stdout).Encode(resp)
			}

			fmt.Println(resp.Response)
			return nil
		},
	}

	cmd.Flags().StringVar(&session, "session", "", "Session ID (default: \"default\")")
	cmd.Flags().StringVar(&account, "account", "", "AWS account name")
	cmd.Flags().StringVarP(&workspace, "workspace", "w", "", "Workspace name")
	return cmd
}

func newConciergeConfigureCmd(flags *rootFlags) *cobra.Command {
	var (
		provider string
		model    string
		apiKey   string
		baseURL  string
	)

	cmd := &cobra.Command{
		Use:   "configure",
		Short: "Configure the concierge LLM provider",
		RunE: func(cmd *cobra.Command, args []string) error {
			if provider == "" {
				return fmt.Errorf("--provider is required (openai or claude)")
			}

			client := flags.newClient()
			req := conciergeConfigureRequest{
				Provider: provider,
				Model:    model,
				APIKey:   apiKey,
				BaseURL:  baseURL,
			}

			if err := client.ConciergeConfigure(context.Background(), req); err != nil {
				if isConciergeUnavailable(err) {
					fmt.Fprint(os.Stderr, conciergeUnavailableMsg)
					return nil
				}
				return fmt.Errorf("concierge configure: %w", err)
			}

			r := flags.renderer()
			r.successLine("Concierge configured")
			return nil
		},
	}

	cmd.Flags().StringVar(&provider, "provider", "", "LLM provider: openai or claude (required)")
	cmd.Flags().StringVar(&model, "model", "", "Model name (e.g. gpt-4o, claude-sonnet-4-20250514)")
	cmd.Flags().StringVar(&apiKey, "api-key", "", "API key for the provider")
	cmd.Flags().StringVar(&baseURL, "base-url", "", "Custom API base URL")
	return cmd
}

func newConciergeStatusCmd(flags *rootFlags) *cobra.Command {
	var session string

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show concierge session status",
		RunE: func(cmd *cobra.Command, args []string) error {
			if session == "" {
				session = "default"
			}

			client := flags.newClient()

			status, err := client.ConciergeGetStatus(context.Background(), session)
			if err != nil {
				if isConciergeUnavailable(err) {
					fmt.Fprint(os.Stderr, conciergeUnavailableMsg)
					return nil
				}
				return fmt.Errorf("concierge status: %w", err)
			}

			if flags.outputFormat() == OutputJSON {
				return json.NewEncoder(os.Stdout).Encode(status)
			}

			fmt.Printf("Session:      %s\n", session)
			fmt.Printf("Provider:     %s\n", status.Provider)
			fmt.Printf("Model:        %s\n", status.Model)
			fmt.Printf("Turns:        %d\n", status.TurnCount)
			fmt.Printf("Last Active:  %s\n", status.LastActiveAt)
			fmt.Printf("Expires:      %s\n", status.ExpiresAt)
			if status.PendingApproval != nil {
				fmt.Printf("\nPending Approval:\n")
				fmt.Printf("  Action:      %s\n", status.PendingApproval.Action)
				fmt.Printf("  Description: %s\n", status.PendingApproval.Description)
				fmt.Printf("  Requested:   %s\n", status.PendingApproval.RequestedAt)
				fmt.Printf("  Approve:     praxis concierge approve --awakeable-id %s\n", status.PendingApproval.AwakeableID)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&session, "session", "", "Session ID (default: \"default\")")
	return cmd
}

func newConciergeHistoryCmd(flags *rootFlags) *cobra.Command {
	var session string

	cmd := &cobra.Command{
		Use:   "history",
		Short: "Show concierge conversation history",
		RunE: func(cmd *cobra.Command, args []string) error {
			if session == "" {
				session = "default"
			}

			client := flags.newClient()

			messages, err := client.ConciergeGetHistory(context.Background(), session)
			if err != nil {
				if isConciergeUnavailable(err) {
					fmt.Fprint(os.Stderr, conciergeUnavailableMsg)
					return nil
				}
				return fmt.Errorf("concierge history: %w", err)
			}

			if flags.outputFormat() == OutputJSON {
				return json.NewEncoder(os.Stdout).Encode(messages)
			}

			if len(messages) == 0 {
				fmt.Println("No conversation history.")
				return nil
			}

			for _, msg := range messages {
				role := strings.ToUpper(msg.Role)
				fmt.Printf("[%s] %s\n%s\n\n", msg.Timestamp, role, msg.Content)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&session, "session", "", "Session ID (default: \"default\")")
	return cmd
}

func newConciergeResetCmd(flags *rootFlags) *cobra.Command {
	var session string

	cmd := &cobra.Command{
		Use:   "reset",
		Short: "Reset a concierge session",
		Long:  `Clear the conversation history and state for a concierge session.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if session == "" {
				session = "default"
			}

			client := flags.newClient()

			// Reset is an exclusive handler on the Virtual Object —
			// invoke it by sending a Void and expecting Void back.
			if err := client.ConciergeReset(context.Background(), session); err != nil {
				if isConciergeUnavailable(err) {
					fmt.Fprint(os.Stderr, conciergeUnavailableMsg)
					return nil
				}
				return fmt.Errorf("concierge reset: %w", err)
			}

			r := flags.renderer()
			r.successLine(fmt.Sprintf("Session %q reset", session))
			return nil
		},
	}

	cmd.Flags().StringVar(&session, "session", "", "Session ID (default: \"default\")")
	return cmd
}

func newConciergeApproveCmd(flags *rootFlags) *cobra.Command {
	var (
		awakeableID string
		reject      bool
		reason      string
	)

	cmd := &cobra.Command{
		Use:   "approve",
		Short: "Approve or reject a pending concierge action",
		RunE: func(cmd *cobra.Command, args []string) error {
			if awakeableID == "" {
				return fmt.Errorf("--awakeable-id is required")
			}

			client := flags.newClient()
			req := conciergeApprovalRequest{
				AwakeableID: awakeableID,
				Approved:    !reject,
				Reason:      reason,
				Actor:       "cli-user",
			}

			if err := client.ConciergeApprove(context.Background(), req); err != nil {
				if isConciergeUnavailable(err) {
					fmt.Fprint(os.Stderr, conciergeUnavailableMsg)
					return nil
				}
				return fmt.Errorf("concierge approve: %w", err)
			}

			r := flags.renderer()
			if reject {
				r.successLine("Action rejected")
			} else {
				r.successLine("Action approved")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&awakeableID, "awakeable-id", "", "Awakeable ID from the pending approval (required)")
	cmd.Flags().BoolVar(&reject, "reject", false, "Reject the action instead of approving")
	cmd.Flags().StringVar(&reason, "reason", "", "Reason for approval or rejection")
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
