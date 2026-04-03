// config_cmd.go implements the `praxis config` command group.
//
// Config commands read and write workspace-scoped settings stored in the
// WorkspaceService Restate Virtual Object. Currently the only supported
// config path is "events.retention", which controls the event retention
// policy (max age, sweep interval, drain sink, etc.).
//
// Each config field has its own subcommand under `praxis config set` so
// users can update individual fields without replacing the entire policy.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shirvan/praxis/internal/core/workspace"
)

// newConfigCmd builds the `praxis config` parent command.
// Subcommands: get, set.
func newConfigCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage workspace-scoped Praxis configuration",
	}
	cmd.AddCommand(
		newConfigGetCmd(flags),
		newConfigSetCmd(flags),
	)
	return cmd
}

// newConfigGetCmd builds `praxis config get <path>`. Reads a workspace-scoped
// configuration value. The workspace is resolved from --workspace or the
// active workspace in ~/.praxis/config.json.
func newConfigGetCmd(flags *rootFlags) *cobra.Command {
	var workspaceName string
	cmd := &cobra.Command{
		Use:   "get <path>",
		Short: "Read configuration for the active workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolvedWorkspace, err := resolveWorkspaceName(workspaceName)
			if err != nil {
				return err
			}
			switch args[0] {
			case "events.retention":
				policy, err := flags.newClient().GetWorkspaceEventRetention(context.Background(), resolvedWorkspace)
				if err != nil {
					return err
				}
				if flags.outputFormat() == OutputJSON {
					return printJSON(policy)
				}
				printEventRetentionPolicy(flags.renderer(), policy)
				return nil
			default:
				return fmt.Errorf("unsupported config path %q", args[0])
			}
		},
	}
	cmd.Flags().StringVarP(&workspaceName, "workspace", "w", "", "Workspace name (defaults to active workspace)")
	return cmd
}

// newConfigSetCmd builds the `praxis config set` parent. Individual retention
// fields are registered as subcommands (e.g. `praxis config set events.retention.max-age 180d`).
func newConfigSetCmd(flags *rootFlags) *cobra.Command {
	var workspaceName string
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Update configuration for the active workspace",
	}
	cmd.PersistentFlags().StringVarP(&workspaceName, "workspace", "w", "", "Workspace name (defaults to active workspace)")
	cmd.AddCommand(
		newConfigSetRetentionCmd(flags, &workspaceName),
		newConfigSetRetentionFieldCmd(flags, &workspaceName, "events.retention.max-age", func(policy *workspace.EventRetentionPolicy, value string) error {
			policy.MaxAge = value
			return nil
		}),
		newConfigSetRetentionFieldCmd(flags, &workspaceName, "events.retention.max-events-per-deployment", func(policy *workspace.EventRetentionPolicy, value string) error {
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid integer %q", value)
			}
			policy.MaxEventsPerDeployment = parsed
			return nil
		}),
		newConfigSetRetentionFieldCmd(flags, &workspaceName, "events.retention.max-index-entries", func(policy *workspace.EventRetentionPolicy, value string) error {
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid integer %q", value)
			}
			policy.MaxIndexEntries = parsed
			return nil
		}),
		newConfigSetRetentionFieldCmd(flags, &workspaceName, "events.retention.sweep-interval", func(policy *workspace.EventRetentionPolicy, value string) error {
			policy.SweepInterval = value
			return nil
		}),
		newConfigSetRetentionFieldCmd(flags, &workspaceName, "events.retention.ship-before-delete", func(policy *workspace.EventRetentionPolicy, value string) error {
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("invalid bool %q", value)
			}
			policy.ShipBeforeDelete = parsed
			return nil
		}),
		newConfigSetRetentionFieldCmd(flags, &workspaceName, "events.retention.drain-sink", func(policy *workspace.EventRetentionPolicy, value string) error {
			policy.DrainSink = value
			return nil
		}),
	)
	return cmd
}

// newConfigSetRetentionCmd builds `praxis config set events.retention`.
// Replaces the full retention policy from a JSON file (--from-file).
func newConfigSetRetentionCmd(flags *rootFlags, workspaceName *string) *cobra.Command {
	var fromFile string
	cmd := &cobra.Command{
		Use:   "events.retention",
		Short: "Replace the full event retention policy",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(fromFile) == "" {
				return fmt.Errorf("--from-file is required")
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
			flags.renderer().successLine(fmt.Sprintf("Updated %s for workspace %q.", cmd.Use, resolvedWorkspace))
			return nil
		},
	}
	cmd.Flags().StringVar(&fromFile, "from-file", "", "Read retention policy from JSON file or - for stdin")
	return cmd
}

// newConfigSetRetentionFieldCmd builds a subcommand that updates a single
// field on the event retention policy. It reads the current policy, applies
// the mutate function, and writes the updated policy back.
func newConfigSetRetentionFieldCmd(flags *rootFlags, workspaceName *string, use string, mutate func(*workspace.EventRetentionPolicy, string) error) *cobra.Command {
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

// resolveWorkspaceName returns the explicitly provided workspace name, or
// falls back to the active workspace from ~/.praxis/config.json.
func resolveWorkspaceName(explicit string) (string, error) {
	if trimmed := strings.TrimSpace(explicit); trimmed != "" {
		return trimmed, nil
	}
	cliCfg := LoadCLIConfig()
	if strings.TrimSpace(cliCfg.ActiveWorkspace) == "" {
		return "", fmt.Errorf("no workspace specified and no active workspace set")
	}
	return cliCfg.ActiveWorkspace, nil
}

// loadEventRetentionPolicy reads and deserialises a retention policy from
// a JSON file. Pass "-" to read from stdin.
func loadEventRetentionPolicy(path string) (workspace.EventRetentionPolicy, error) {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(path) //nolint:gosec // path is user-provided CLI argument
	}
	if err != nil {
		return workspace.EventRetentionPolicy{}, err
	}
	var policy workspace.EventRetentionPolicy
	if err := json.Unmarshal(data, &policy); err != nil {
		return workspace.EventRetentionPolicy{}, fmt.Errorf("decode retention policy: %w", err)
	}
	return policy, nil
}

// printEventRetentionPolicy renders a retention policy as a label/value list.
func printEventRetentionPolicy(r *Renderer, policy *workspace.EventRetentionPolicy) {
	if policy == nil {
		_, _ = fmt.Fprintln(r.out, r.renderMuted("No event retention policy configured."))
		return
	}
	r.writeLabelValue("Max Age", 28, policy.MaxAge)
	r.writeLabelValue("Max Events/Deployment", 28, fmt.Sprintf("%d", policy.MaxEventsPerDeployment))
	r.writeLabelValue("Max Index Entries", 28, fmt.Sprintf("%d", policy.MaxIndexEntries))
	r.writeLabelValue("Sweep Interval", 28, policy.SweepInterval)
	r.writeLabelValue("Ship Before Delete", 28, fmt.Sprintf("%t", policy.ShipBeforeDelete))
	if policy.DrainSink != "" {
		r.writeLabelValue("Drain Sink", 28, policy.DrainSink)
	}
}
