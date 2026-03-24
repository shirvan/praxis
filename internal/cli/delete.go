package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
)

// newDeleteCmd builds the `praxis delete` subcommand.
//
// Delete starts an asynchronous teardown of all resources in a deployment,
// processing them in reverse dependency order. The command returns immediately
// unless --wait is set.
//
// Usage:
//
//	praxis delete Deployment/my-webapp
//	praxis delete Deployment/my-webapp --yes
//	praxis delete Deployment/my-webapp --yes --wait
//	praxis delete Deployment/my-webapp -o json
func newDeleteCmd(flags *rootFlags) *cobra.Command {
	var (
		yes     bool
		wait    bool
		timeout time.Duration
	)

	cmd := &cobra.Command{
		Use:   "delete Deployment/<key>",
		Short: "Tear down a deployment and all its resources",
		Long: `Delete initiates a deployment-wide resource teardown. Resources are deleted
in reverse dependency order — dependents are removed before the resources
they depend on.

By default, the command asks for confirmation before proceeding. Use --yes
to skip the prompt (useful for scripting and CI).

    praxis delete Deployment/my-webapp --yes

The deletion is asynchronous. Use --wait to block until all resources have
been deleted:

    praxis delete Deployment/my-webapp --yes --wait`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			renderer := flags.renderer()
			kind, key, err := parseKindKey(args[0])
			if err != nil {
				return err
			}
			if kind != "Deployment" {
				return fmt.Errorf("delete only supports Deployment resources, got %q", kind)
			}

			// Confirm with the user unless --yes is set.
			if !yes {
				_, _ = fmt.Fprintf(renderer.out, "%s ", renderer.renderPrompt(fmt.Sprintf("Delete deployment %q and all its resources? [y/N]:", key)))
				var confirm string
				if _, err := fmt.Scanln(&confirm); err != nil || (confirm != "y" && confirm != "Y") {
					_, _ = fmt.Fprintln(renderer.out, renderer.renderMuted("Cancelled."))
					return nil
				}
			}

			client := flags.newClient()
			ctx := context.Background()

			resp, err := client.DeleteDeployment(ctx, key)
			if err != nil {
				return err
			}

			// JSON mode: emit the response.
			if flags.outputFormat() == OutputJSON {
				return printJSON(resp)
			}

			renderer.writeLabelValue("Deployment", 11, resp.DeploymentKey)
			renderer.writeLabelStyledValue("Status", 11, renderer.renderStatus(string(resp.Status)))

			if !wait {
				_, _ = fmt.Fprintln(renderer.out, "\n"+renderer.renderMuted("Deletion in progress. Use 'praxis get Deployment/"+key+"' to check progress."))
				return nil
			}

			// Apply a timeout to the polling context if set.
			if timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}

			// Poll until deletion completes.
			err = pollDeployment(ctx, client, key, 2*time.Second, flags.outputFormat(), renderer)
			if isTimeoutError(ctx, err) {
				printTimeoutError(renderer, timeout, key)
				os.Exit(2)
			}
			return err
		},
	}

	cmd.Flags().BoolVar(&yes, "yes", false, "Skip confirmation prompt")
	cmd.Flags().BoolVar(&wait, "wait", false, "Wait for deletion to complete")
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Minute, "Maximum time to wait for completion (0 for no limit)")

	return cmd
}
