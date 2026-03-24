package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/shirvan/praxis/pkg/types"
)

// newDeployCmd builds the `praxis deploy` subcommand.
//
// Deploy provisions resources from a pre-registered CUE template. The user
// provides only template variables — no CUE source.
//
// Usage:
//
//	praxis deploy stack1 --var name=orders-api --var environment=prod
//	praxis deploy stack1 -f variables.json
//	praxis deploy stack1 --var name=orders-api --dry-run
//	praxis deploy stack1 --var name=orders-api --key orders-prod --wait
func newDeployCmd(flags *rootFlags) *cobra.Command {
	var (
		vars          []string
		varsFile      string
		deploymentKey string
		wait          bool
		dryRun        bool
		showRendered  bool
		pollInterval  time.Duration
		timeout       time.Duration
		account       string
		autoApprove   bool
		targets       []string
		replace       []string
	)
	account = flags.account

	cmd := &cobra.Command{
		Use:   "deploy <template-name>",
		Short: "Deploy infrastructure from a registered template",
		Long: `Deploy provisions resources using a pre-registered CUE template.
The template must have been registered by an operator using 'praxis template register'.

Before provisioning, a plan diff is displayed showing what would change.
You must confirm before changes are applied. Use --auto-approve to skip
the prompt (useful for CI and scripting).

Provide variables via --var flags or a JSON file with -f:

    praxis deploy stack1 --var name=orders-api --var env=prod
    praxis deploy stack1 -f variables.json

Flags and file can be combined — flag values take precedence:

    praxis deploy stack1 -f base.json --var env=prod

Use --dry-run to preview changes without provisioning:

    praxis deploy stack1 --var name=orders-api --dry-run`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			templateName := args[0]
			renderer := flags.renderer()

			variables, err := mergeVariables(vars, varsFile)
			if err != nil {
				return err
			}

			client := flags.newClient()
			ctx := context.Background()

			cliCfg := LoadCLIConfig()

			if dryRun {
				resp, err := client.PlanDeploy(ctx, types.PlanDeployRequest{
					Template:  templateName,
					Variables: variables,
					Account:   account,
					Workspace: cliCfg.ActiveWorkspace,
					Targets:   targets,
				})
				if err != nil {
					return err
				}

				if flags.outputFormat() == OutputJSON {
					return printJSON(resp)
				}

				printDataSources(renderer, resp.DataSources)
				printPlan(renderer, resp.Plan)

				if showRendered && resp.Rendered != "" {
					_, _ = fmt.Fprintln(renderer.out)
					_, _ = fmt.Fprintln(renderer.out, renderer.renderSection("Rendered template:"))
					_, _ = fmt.Fprintln(renderer.out, resp.Rendered)
				}
				return nil
			}

			// Run plan to show what would change before deploying.
			planResp, err := client.PlanDeploy(ctx, types.PlanDeployRequest{
				Template:  templateName,
				Variables: variables,
				Account:   account,
				Workspace: cliCfg.ActiveWorkspace,
				Targets:   targets,
			})
			if err != nil {
				return err
			}

			// Display the plan diff.
			if flags.outputFormat() != OutputJSON {
				printDataSources(renderer, planResp.DataSources)
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

			resp, err := client.Deploy(ctx, types.DeployRequest{
				Template:      templateName,
				Variables:     variables,
				DeploymentKey: deploymentKey,
				Account:       account,
				Workspace:     cliCfg.ActiveWorkspace,
				Targets:       targets,
				Replace:       replace,
			})
			if err != nil {
				return err
			}

			if flags.outputFormat() == OutputJSON {
				return printJSON(resp)
			}

			renderer.writeLabelValue("Deployment", 11, resp.DeploymentKey)
			renderer.writeLabelStyledValue("Status", 11, renderer.renderStatus(string(resp.Status)))

			if !wait {
				_, _ = fmt.Fprintln(renderer.out, "\n"+renderer.renderMuted("Use 'praxis get Deployment/"+resp.DeploymentKey+"' to check progress."))
				return nil
			}

			if timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}

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
	cmd.Flags().StringVar(&deploymentKey, "key", "", "Pin a stable deployment key for idempotent re-deploy")
	cmd.Flags().StringVar(&account, "account", account, "AWS account name to use (env: PRAXIS_ACCOUNT)")
	cmd.Flags().BoolVar(&wait, "wait", false, "Poll until deployment completes or fails")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview changes without provisioning (runs PlanDeploy)")
	cmd.Flags().BoolVar(&showRendered, "show-rendered", false, "Also display the fully-evaluated template JSON (with --dry-run)")
	cmd.Flags().DurationVar(&pollInterval, "poll-interval", 2*time.Second, "Polling interval when --wait is set")
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Minute, "Maximum time to wait for completion (0 for no limit)")
	cmd.Flags().BoolVar(&autoApprove, "auto-approve", false, "Skip the confirmation prompt")
	cmd.Flags().StringArrayVar(&targets, "target", nil, "Limit to named resource and its dependencies (repeatable)")
	cmd.Flags().StringArrayVar(&replace, "replace", nil, "Force delete and re-provision of named resource (repeatable)")

	return cmd
}

// mergeVariables combines --var flags and -f JSON file. Flag values override file.
func mergeVariables(vars []string, varsFile string) (map[string]any, error) {
	result := make(map[string]any)

	if varsFile != "" {
		content, err := os.ReadFile(varsFile) //nolint:gosec // G304: path is user-supplied CLI argument
		if err != nil {
			return nil, fmt.Errorf("read variables file %q: %w", varsFile, err)
		}
		if err := json.Unmarshal(content, &result); err != nil {
			return nil, fmt.Errorf("parse variables file %q: %w", varsFile, err)
		}
	}

	flagVars, err := parseVariables(vars)
	if err != nil {
		return nil, err
	}
	maps.Copy(result, flagVars)

	if len(result) == 0 {
		return nil, nil
	}
	return result, nil
}
