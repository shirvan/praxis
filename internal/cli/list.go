package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

// newListCmd builds the `praxis list` subcommand.
//
// List is currently scoped to deployments. It queries the global deployment
// index and displays a summary table.
//
// Usage:
//
//	praxis list deployments
//	praxis list deployments -o json
func newListCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list <resource-type>",
		Short: "List active deployments",
		Long: `List queries Praxis Core for known resources of the specified type.

Currently supported resource types:

    praxis list deployments    — List all known deployments with status summary

The output includes deployment key, status, resource count, and timestamps.
Use -o json for machine-readable output.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resourceType := args[0]
			switch resourceType {
			case "deployments", "deployment", "deploy":
				return listDeployments(flags)
			default:
				return fmt.Errorf("unsupported resource type %q (supported: deployments)", resourceType)
			}
		},
	}

	return cmd
}

// listDeployments queries the global deployment index and renders the results.
func listDeployments(flags *rootFlags) error {
	client := flags.newClient()
	ctx := context.Background()

	summaries, err := client.ListDeployments(ctx)
	if err != nil {
		return err
	}

	if flags.outputFormat() == OutputJSON {
		return printJSON(summaries)
	}

	printDeploymentSummaries(summaries)
	return nil
}
