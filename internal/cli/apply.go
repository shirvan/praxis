package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/shirvan/praxis/pkg/types"
)

// newApplyCmd builds the `praxis apply` subcommand.
//
// Apply reads a CUE template from disk, sends it to the Praxis command service
// for evaluation and orchestration, and reports the deployment key. With --wait,
// it polls until the deployment reaches a terminal state.
//
// Usage:
//
//	praxis apply webapp.cue
//	praxis apply webapp.cue --var env=production --var region=us-west-2
//	praxis apply webapp.cue --key my-webapp --wait
//	praxis apply webapp.cue -o json
func newApplyCmd(flags *rootFlags) *cobra.Command {
	var (
		// vars collects --var key=value pairs for template variables.
		vars []string
		// varsFile is an optional JSON file containing template variables.
		varsFile string
		// deploymentKey lets the user pin a stable deployment identity.
		deploymentKey string
		// wait enables polling until the deployment reaches a terminal state.
		wait bool
		// pollInterval controls how frequently the CLI polls for status when
		// --wait is set.
		pollInterval time.Duration
		// timeout is the maximum time to wait for deployment completion when
		// --wait is set. Zero means no limit.
		timeout time.Duration
		// account selects which configured AWS account to use for this apply.
		account string
		// autoApprove skips the confirmation prompt.
		autoApprove bool
		// targets limits the apply to the named resources and their transitive
		// dependencies.
		targets []string
		// replace forces Delete→Provision on the named resources.
		replace []string
	)
	account = flags.account

	cmd := &cobra.Command{
		Use:   "apply <template.cue>",
		Short: "Provision resources from a CUE template",
		Long: `Apply evaluates a CUE template, resolves variables and SSM parameters,
builds the resource dependency graph, and submits the deployment to the
Praxis orchestrator.

Before provisioning, a plan diff is displayed showing what would change.
You must confirm before changes are applied. Use --auto-approve to skip
the prompt (useful for CI and scripting).

The command returns immediately with the deployment key unless --wait is set,
in which case it polls for completion.

Template variables can be loaded from a JSON file with -f and/or passed
individually with --var. Flag values override file values:

    praxis apply webapp.cue -f variables.json
    praxis apply webapp.cue --var env=production --var region=us-west-2
    praxis apply webapp.cue -f base.json --var env=prod

A stable deployment key can be pinned with --key to enable idempotent re-apply:

    praxis apply webapp.cue --key my-webapp`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			templatePath := args[0]
			renderer := flags.renderer()

			// Read the CUE template from disk.
			content, err := os.ReadFile(templatePath) //nolint:gosec // G304: path is user-supplied CLI argument
			if err != nil {
				return fmt.Errorf("read template %q: %w", templatePath, err)
			}

			// Merge -f JSON file with --var key=value overrides.
			variables, err := mergeVariables(vars, varsFile)
			if err != nil {
				return err
			}

			client := flags.newClient()
			ctx := context.Background()

			cliCfg := LoadCLIConfig()

			// Run plan first to show what would change.
			planResp, err := client.Plan(ctx, types.PlanRequest{
				Template:     string(content),
				Variables:    variables,
				Account:      account,
				Workspace:    cliCfg.ActiveWorkspace,
				Targets:      targets,
				TemplatePath: templatePath,
			})
			if err != nil {
				return err
			}

			// Display the plan diff.
			if flags.outputFormat() != OutputJSON {
				printPlan(renderer, planResp.Plan)
			}

			// If there are no changes, exit early.
			if planResp.Plan == nil || !planResp.Plan.Summary.HasChanges() {
				if flags.outputFormat() == OutputJSON {
					return printJSON(planResp)
				}
				return nil
			}

			// Confirm with the user unless --auto-approve is set.
			if !autoApprove {
				_, _ = fmt.Fprint(renderer.out, "\n"+renderer.renderPrompt("Do you want to apply these changes? (yes/no): "))
				var confirm string
				if _, err := fmt.Scanln(&confirm); err != nil || (confirm != "yes" && confirm != "y") {
					_, _ = fmt.Fprintln(renderer.out, renderer.renderMuted("Apply cancelled."))
					return nil
				}
			}

			resp, err := client.Apply(ctx, types.ApplyRequest{
				Template:      string(content),
				Variables:     variables,
				DeploymentKey: deploymentKey,
				Account:       account,
				Workspace:     cliCfg.ActiveWorkspace,
				Targets:       targets,
				Replace:       replace,
				TemplatePath:  templatePath,
			})
			if err != nil {
				return err
			}

			// JSON mode: emit the full response and exit.
			if flags.outputFormat() == OutputJSON {
				return printJSON(resp)
			}

			renderer.writeLabelValue("Deployment", 11, resp.DeploymentKey)
			renderer.writeLabelStyledValue("Status", 11, renderer.renderStatus(string(resp.Status)))

			// If --wait is not set, we're done.
			if !wait {
				_, _ = fmt.Fprintln(renderer.out, "\n"+renderer.renderMuted("Use 'praxis get Deployment/"+resp.DeploymentKey+"' to check progress."))
				return nil
			}

			// Apply a timeout to the polling context if set.
			if timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}

			// Poll until the deployment reaches a terminal state.
			err = pollDeployment(ctx, client, resp.DeploymentKey, pollInterval, flags.outputFormat(), renderer)
			if isTimeoutError(ctx, err) {
				printTimeoutError(renderer, timeout, resp.DeploymentKey)
				os.Exit(2)
			}
			return err
		},
	}

	cmd.Flags().StringArrayVar(&vars, "var", nil, "Template variable in key=value format (repeatable)")
	cmd.Flags().StringVarP(&varsFile, "file", "f", "", "JSON file containing template variables")
	cmd.Flags().StringVar(&deploymentKey, "key", "", "Pin a stable deployment key for idempotent re-apply")
	cmd.Flags().StringVar(&account, "account", account, "AWS account name to use (env: PRAXIS_ACCOUNT)")
	cmd.Flags().BoolVar(&wait, "wait", false, "Poll until deployment completes or fails")
	cmd.Flags().DurationVar(&pollInterval, "poll-interval", 2*time.Second, "Polling interval when --wait is set")
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Minute, "Maximum time to wait for completion (0 for no limit)")
	cmd.Flags().BoolVar(&autoApprove, "auto-approve", false, "Skip the confirmation prompt")
	cmd.Flags().StringArrayVar(&targets, "target", nil, "Limit to named resource and its dependencies (repeatable)")
	cmd.Flags().StringArrayVar(&replace, "replace", nil, "Force delete and re-provision of named resource (repeatable)")

	return cmd
}

// --------------------------------------------------------------------------
// Helpers
// --------------------------------------------------------------------------

// parseVariables converts a slice of "key=value" strings into a map. Returns
// an error if any entry is malformed.
func parseVariables(vars []string) (map[string]any, error) {
	if len(vars) == 0 {
		return nil, nil
	}
	result := make(map[string]any, len(vars))
	for _, v := range vars {
		parts := strings.SplitN(v, "=", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
			return nil, fmt.Errorf("invalid variable %q: expected key=value", v)
		}
		result[strings.TrimSpace(parts[0])] = parts[1]
	}
	return result, nil
}

// pollDeployment queries the deployment state at regular intervals until it
// reaches a terminal status. It prints incremental status updates for the user.
func pollDeployment(ctx context.Context, client *Client, key string, interval time.Duration, format OutputFormat, renderer *Renderer) error {
	_, _ = fmt.Fprintln(renderer.out, "\n"+renderer.renderSection("Waiting for deployment to complete..."))

	var lastStatus types.DeploymentStatus
	for {
		detail, err := client.GetDeployment(ctx, key)
		if err != nil {
			return fmt.Errorf("poll deployment: %w", err)
		}
		if detail == nil {
			return fmt.Errorf("deployment %q not found during polling", key)
		}

		// Print status changes as they happen.
		if detail.Status != lastStatus {
			renderer.writeLabelStyledValue("Status", 9, renderer.renderStatus(string(detail.Status)))
			lastStatus = detail.Status
		}

		// Check for terminal states.
		if isTerminalStatus(detail.Status) {
			_, _ = fmt.Fprintln(renderer.out)
			if format == OutputJSON {
				return printJSON(detail)
			}
			printDeploymentDetail(renderer, detail)
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
			// Continue polling.
		}
	}
}

// isTerminalStatus returns true if the deployment has reached a final state
// where no further transitions will occur.
func isTerminalStatus(s types.DeploymentStatus) bool {
	switch s {
	case types.DeploymentComplete, types.DeploymentFailed,
		types.DeploymentDeleted, types.DeploymentCancelled:
		return true
	default:
		return false
	}
}
