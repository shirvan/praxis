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
		Use:   "plan <file.cue | template-name>",
		Short: "Preview what would change without provisioning",
		Long: `Plan evaluates either a local CUE template file or a registered
template and compares the desired state against current cloud state. It shows
what resources would be created, updated, or deleted without making changes.

This is the Praxis equivalent of a dry-run preview.

Template variables can be loaded from a JSON file with -f and/or passed
individually with --var. Flag values override file values:

    praxis plan webapp.cue -f variables.json
    praxis plan stack1 --var env=staging
    praxis plan webapp.cue --var env=staging
    praxis plan webapp.cue -f base.json --var env=prod

Use --show-rendered to also display the fully-evaluated template JSON,
which is useful for debugging variable resolution and output expressions.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			source := args[0]
			if isFilePath(source) {
				return planFromFile(flags, source, vars, varsFile, account, targets, showRendered, showGraph)
			}
			return planFromTemplate(flags, source, vars, varsFile, account, targets, showRendered, showGraph)
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

func planFromFile(flags *rootFlags, templatePath string, vars []string, varsFile, account string, targets []string, showRendered, showGraph bool) error {
	renderer := flags.renderer()
	workspace := flags.activeWorkspace()

	content, err := os.ReadFile(templatePath) //nolint:gosec // G304: path is user-supplied CLI argument
	if err != nil {
		return fmt.Errorf("read template %q: %w", templatePath, err)
	}

	variables, err := mergeVariables(vars, varsFile)
	if err != nil {
		return err
	}

	client := flags.newClient()
	ctx := context.Background()

	resp, err := client.Plan(ctx, types.PlanRequest{
		Template:     string(content),
		Variables:    variables,
		Account:      account,
		Workspace:    workspace,
		Targets:      targets,
		TemplatePath: templatePath,
	})
	if err != nil {
		return err
	}

	if flags.outputFormat() == OutputJSON {
		return printJSON(resp)
	}

	printDataSources(renderer, resp.DataSources)
	printPlan(renderer, resp.Plan)

	if showGraph && len(resp.Graph) > 0 {
		_, _ = fmt.Fprintln(renderer.out)
		_, _ = fmt.Fprintln(renderer.out, renderer.renderSection("Dependency graph:"))
		_, _ = fmt.Fprintln(renderer.out)
		printGraph(renderer, resp.Graph)
	}

	if showRendered && resp.Rendered != "" {
		_, _ = fmt.Fprintln(renderer.out)
		_, _ = fmt.Fprintln(renderer.out, renderer.renderSection("Rendered template:"))
		_, _ = fmt.Fprintln(renderer.out, resp.Rendered)
	}

	return nil
}

func planFromTemplate(flags *rootFlags, templateName string, vars []string, varsFile, account string, targets []string, showRendered, showGraph bool) error {
	renderer := flags.renderer()
	workspace := flags.activeWorkspace()

	variables, err := mergeVariables(vars, varsFile)
	if err != nil {
		return err
	}

	client := flags.newClient()
	ctx := context.Background()

	resp, err := client.PlanDeploy(ctx, types.PlanDeployRequest{
		Template:  templateName,
		Variables: variables,
		Account:   account,
		Workspace: workspace,
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

	if showGraph {
		_, _ = fmt.Fprintln(renderer.out)
		_, _ = fmt.Fprintln(renderer.out, renderer.renderSection("Dependency graph:"))
		_, _ = fmt.Fprintln(renderer.out, renderer.renderMuted("Dependency graph output is only available for inline template planning."))
	}

	if showRendered && resp.Rendered != "" {
		_, _ = fmt.Fprintln(renderer.out)
		_, _ = fmt.Fprintln(renderer.out, renderer.renderSection("Rendered template:"))
		_, _ = fmt.Fprintln(renderer.out, resp.Rendered)
	}

	return nil
}
