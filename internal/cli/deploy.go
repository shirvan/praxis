package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/praxiscloud/praxis/pkg/types"
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
	)
	account = flags.account

	cmd := &cobra.Command{
		Use:   "deploy <template-name>",
		Short: "Deploy infrastructure from a registered template",
		Long: `Deploy provisions resources using a pre-registered CUE template.
The template must have been registered by an operator using 'praxis template register'.

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

			variables, err := mergeVariables(vars, varsFile)
			if err != nil {
				return err
			}

			client := flags.newClient()
			ctx := context.Background()

			if dryRun {
				resp, err := client.PlanDeploy(ctx, types.PlanDeployRequest{
					Template:  templateName,
					Variables: variables,
					Account:   account,
				})
				if err != nil {
					return err
				}

				if flags.outputFormat() == OutputJSON {
					return printJSON(resp)
				}

				printPlan(resp.Plan)

				if showRendered && resp.Rendered != "" {
					fmt.Println()
					fmt.Println("Rendered template:")
					fmt.Println(resp.Rendered)
				}
				return nil
			}

			resp, err := client.Deploy(ctx, types.DeployRequest{
				Template:      templateName,
				Variables:     variables,
				DeploymentKey: deploymentKey,
				Account:       account,
			})
			if err != nil {
				return err
			}

			if flags.outputFormat() == OutputJSON {
				return printJSON(resp)
			}

			fmt.Printf("Deployment: %s\n", resp.DeploymentKey)
			fmt.Printf("Status:     %s\n", resp.Status)

			if !wait {
				fmt.Println("\nUse 'praxis get Deployment/" + resp.DeploymentKey + "' to check progress.")
				return nil
			}

			if timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}

			err = pollDeployment(ctx, client, resp.DeploymentKey, pollInterval, flags.outputFormat())
			if isTimeoutError(ctx, err) {
				printTimeoutError(timeout, resp.DeploymentKey)
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

	return cmd
}

// mergeVariables combines --var flags and -f JSON file. Flag values override file.
func mergeVariables(vars []string, varsFile string) (map[string]any, error) {
	result := make(map[string]any)

	if varsFile != "" {
		content, err := os.ReadFile(varsFile)
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
	for k, v := range flagVars {
		result[k] = v
	}

	if len(result) == 0 {
		return nil, nil
	}
	return result, nil
}
