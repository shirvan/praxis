package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// newSetCmd builds the `praxis set` verb command.
// Subcommands: workspace, config, concierge.
func newSetCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set <resource>",
		Short: "Update a setting or select a resource",
		Long: `Set changes a setting or selects an active resource.

Supported resources:

    praxis set workspace <name>        Set the active workspace
    praxis set config <path> <value>   Update workspace-scoped configuration
    praxis set concierge               Configure the concierge LLM provider`,
	}

	cmd.AddCommand(
		newSetWorkspaceCmd(flags),
		newSetConfigCmd(flags),
		newSetConciergeCmd(flags),
	)

	return cmd
}

// newSetWorkspaceCmd builds `praxis set workspace <name>`.
func newSetWorkspaceCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "workspace <name>",
		Short: "Set the active workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			renderer := flags.renderer()
			client := flags.newClient()
			ctx := context.Background()

			if _, err := client.GetWorkspace(ctx, name); err != nil {
				return fmt.Errorf("workspace %q: %w", name, err)
			}

			cliCfg := LoadCLIConfig()
			cliCfg.ActiveWorkspace = name
			if err := SaveCLIConfig(cliCfg); err != nil {
				return fmt.Errorf("save config: %w", err)
			}

			renderer.successLine(fmt.Sprintf("Switched to workspace %q.", name))
			return nil
		},
	}
}

// newSetConfigCmd builds the `praxis set config` command group.
func newSetConfigCmd(flags *rootFlags) *cobra.Command {
	var workspaceName string
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Update workspace-scoped configuration",
	}
	cmd.PersistentFlags().StringVarP(&workspaceName, "workspace", "w", "", "Workspace name (env: PRAXIS_WORKSPACE, defaults to active workspace)")
	cmd.AddCommand(
		newSetConfigRetentionCmd(flags, &workspaceName),
		newSetConfigRetentionFieldCmd(flags, &workspaceName, "events.retention.max-age", configMutateMaxAge),
		newSetConfigRetentionFieldCmd(flags, &workspaceName, "events.retention.max-events-per-deployment", configMutateMaxEvents),
		newSetConfigRetentionFieldCmd(flags, &workspaceName, "events.retention.max-index-entries", configMutateMaxIndex),
		newSetConfigRetentionFieldCmd(flags, &workspaceName, "events.retention.sweep-interval", configMutateSweepInterval),
		newSetConfigRetentionFieldCmd(flags, &workspaceName, "events.retention.ship-before-delete", configMutateShipBeforeDelete),
		newSetConfigRetentionFieldCmd(flags, &workspaceName, "events.retention.drain-sink", configMutateDrainSink),
	)
	return cmd
}

// newSetConfigRetentionCmd builds `praxis set config events.retention`.
func newSetConfigRetentionCmd(flags *rootFlags, workspaceName *string) *cobra.Command {
	var fromFile string
	cmd := &cobra.Command{
		Use:   "events.retention",
		Short: "Replace the full event retention policy",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(fromFile) == "" {
				return fmt.Errorf("--file is required")
			}
			resolvedWorkspace, err := resolveWorkspaceName(*workspaceName)
			if err != nil {
				return err
			}
			policy, err := loadEventRetentionPolicy(fromFile)
			if err != nil {
				return err
			}
			if err := flags.newClient().SetWorkspaceEventRetention(context.Background(), resolvedWorkspace, policy); err != nil {
				return err
			}
			if flags.outputFormat() == OutputJSON {
				return printJSON(policy)
			}
			flags.renderer().successLine(fmt.Sprintf("Updated events.retention for workspace %q.", resolvedWorkspace))
			return nil
		},
	}
	cmd.Flags().StringVarP(&fromFile, "file", "f", "", "Read retention policy from JSON file or - for stdin")
	return cmd
}

// newSetConfigRetentionFieldCmd builds a subcommand under `praxis set config`
// that updates a single field on the event retention policy.
func newSetConfigRetentionFieldCmd(flags *rootFlags, workspaceName *string, use string, mutate configFieldMutator) *cobra.Command {
	return &cobra.Command{
		Use:   use + " <value>",
		Short: "Update one retention policy field",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolvedWorkspace, err := resolveWorkspaceName(*workspaceName)
			if err != nil {
				return err
			}
			client := flags.newClient()
			policy, err := client.GetWorkspaceEventRetention(context.Background(), resolvedWorkspace)
			if err != nil {
				return err
			}
			updated := *policy
			if err := mutate(&updated, args[0]); err != nil {
				return err
			}
			if err := client.SetWorkspaceEventRetention(context.Background(), resolvedWorkspace, updated); err != nil {
				return err
			}
			if flags.outputFormat() == OutputJSON {
				return printJSON(updated)
			}
			flags.renderer().successLine(fmt.Sprintf("Updated %s for workspace %q.", use, resolvedWorkspace))
			return nil
		},
	}
}

// newSetConciergeCmd builds `praxis set concierge`.
func newSetConciergeCmd(flags *rootFlags) *cobra.Command {
	var (
		provider string
		model    string
		apiKey   string
		baseURL  string
	)

	cmd := &cobra.Command{
		Use:   "concierge",
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
				return fmt.Errorf("set concierge: %w", err)
			}

			flags.renderer().successLine("Concierge configured")
			return nil
		},
	}

	cmd.Flags().StringVar(&provider, "provider", "", "LLM provider: openai or claude (required)")
	cmd.Flags().StringVar(&model, "model", "", "Model name (e.g. gpt-4o, claude-sonnet-4-20250514)")
	cmd.Flags().StringVar(&apiKey, "api-key", "", "API key for the provider")
	cmd.Flags().StringVar(&baseURL, "base-url", "", "Custom API base URL")
	return cmd
}
