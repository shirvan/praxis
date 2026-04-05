package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// newAskCmd builds the `praxis ask` top-level verb.
// Sends a natural language prompt to the Concierge AI assistant.
func newAskCmd(flags *rootFlags) *cobra.Command {
	var (
		session   string
		account   string
		workspace string
	)

	cmd := &cobra.Command{
		Use:   "ask <prompt>",
		Short: "Send a prompt to the concierge AI assistant",
		Long: `Send a natural language prompt to the Concierge AI assistant.

    praxis ask how do I deploy a VPC
    praxis ask "what's the status of my-app"
    praxis ask "convert this terraform to praxis"`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			prompt := strings.Join(args, " ")
			if account == "" {
				account = flags.account
			}

			client := flags.newClient()
			r := flags.renderer()
			isJSON := flags.outputFormat() == OutputJSON

			resp, err := runConciergeAsk(context.Background(), conciergeAskOpts{
				Client:    client,
				Renderer:  r,
				Session:   session,
				Prompt:    prompt,
				Account:   account,
				Workspace: workspace,
				JSON:      isJSON,
			})
			if err != nil {
				if isConciergeUnavailable(err) {
					fmt.Fprint(os.Stderr, conciergeUnavailableMsg)
					return nil
				}
				return fmt.Errorf("ask: %w", err)
			}

			if isJSON {
				return json.NewEncoder(os.Stdout).Encode(resp)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&session, "session", "", "Session ID (env: PRAXIS_SESSION, omit to start new)")
	cmd.Flags().StringVar(&account, "account", "", "AWS account name")
	cmd.Flags().StringVarP(&workspace, "workspace", "w", "", "Workspace name")
	return cmd
}
