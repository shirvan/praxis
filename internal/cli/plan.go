package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/shirvan/praxis/pkg/types"
)

// newPlanCmd builds the `praxis plan` subcommand.
//
// Plan performs a dry-run evaluation of a CUE template. It runs the full
// template pipeline (CUE evaluation, SSM resolution, DAG
// construction) and then compares the desired state against current driver
// state to produce a diff.
//
// No resources are provisioned — this is a read-only operation.
//
// Usage:
//
//	praxis plan webapp.cue
//	praxis plan webapp.cue --var env=production
//	praxis plan webapp.cue -o json
//	praxis plan webapp.cue --show-rendered
func newPlanCmd(flags *rootFlags) *cobra.Command {
	var (
		vars         []string
		varsFile     string
		showRendered bool
		showGraph    bool
		account      string
		targets      []string
	)
	account = flags.account

	cmd := &cobra.Command{
		Use:   "plan <template.cue>",
		Short: "Preview what would change without provisioning",
		Long: `Plan evaluates a CUE template and compares the desired state against
current cloud state. It shows what resources would be created, updated,
or deleted — without actually making any changes.

This is the Praxis equivalent of a dry-run preview.

Template variables can be loaded from a JSON file with -f and/or passed
individually with --var. Flag values override file values:

    praxis plan webapp.cue -f variables.json
    praxis plan webapp.cue --var env=staging
    praxis plan webapp.cue -f base.json --var env=prod

Use --show-rendered to also display the fully-evaluated template JSON,
which is useful for debugging variable resolution and output expressions.`,
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

			resp, err := client.Plan(ctx, types.PlanRequest{
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

			// JSON mode: emit the full response and exit.
			if flags.outputFormat() == OutputJSON {
				return printJSON(resp)
			}

			// Human-readable plan output.
			printDataSources(renderer, resp.DataSources)
			printPlan(renderer, resp.Plan)

			// Optionally show the resource dependency graph.
			if showGraph && len(resp.Graph) > 0 {
				_, _ = fmt.Fprintln(renderer.out)
				_, _ = fmt.Fprintln(renderer.out, renderer.renderSection("Dependency graph:"))
				_, _ = fmt.Fprintln(renderer.out)
				printGraph(renderer, resp.Graph)
			}

			// Optionally show the fully-resolved template JSON.
			if showRendered && resp.Rendered != "" {
				_, _ = fmt.Fprintln(renderer.out)
				_, _ = fmt.Fprintln(renderer.out, renderer.renderSection("Rendered template:"))
				_, _ = fmt.Fprintln(renderer.out, resp.Rendered)
			}

			return nil
		},
	}

	cmd.Flags().StringArrayVar(&vars, "var", nil, "Template variable in key=value format (repeatable)")
	cmd.Flags().StringVarP(&varsFile, "file", "f", "", "JSON file containing template variables")
	cmd.Flags().StringVar(&account, "account", account, "AWS account name to use (env: PRAXIS_ACCOUNT)")
	cmd.Flags().BoolVar(&showRendered, "show-rendered", false, "Also display the fully-evaluated template JSON")
	cmd.Flags().BoolVar(&showGraph, "graph", false, "Display the resource dependency graph")
	cmd.Flags().StringArrayVar(&targets, "target", nil, "Limit to named resource and its dependencies (repeatable)")

	return cmd
}
