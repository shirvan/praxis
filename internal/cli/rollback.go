// rollback.go implements `praxis rollback` — point-in-time rollback of a
// deployment to a previous known-good generation. Praxis snapshots every
// generation's plan; rolling back replays the stored plan, so specs that
// changed are converged back, resources added since are deleted, and
// resources removed since are re-provisioned. Use `praxis list generations
// <key>` to see the available targets. (Distinct from `praxis delete
// --rollback`, which cleans up a failed deployment by deleting its
// provisioned resources.)
package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/shirvan/praxis/pkg/types"
)

func newRollbackCmd(flags *rootFlags) *cobra.Command {
	var (
		toGeneration int64
		wait         bool
		pollInterval time.Duration
	)

	cmd := &cobra.Command{
		Use:   "rollback <deployment-key> --to <generation>",
		Short: "Revert a deployment to a previous known-good generation",
		Long: `Rollback replays the plan recorded for an earlier generation of the
deployment: specs that changed since are reverted, resources added since are
deleted, and resources removed since are re-provisioned.

Only generations that finished Complete are valid targets, and only the most
recent generations are retained — run 'praxis list generations <key>' to see
what is available. The rollback itself becomes a new generation, so it can be
rolled back too. Deployments in protected workspaces pass through the
approval gate as usual.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if toGeneration <= 0 {
				return fmt.Errorf("--to <generation> is required (run 'praxis list generations %s' to see targets)", args[0])
			}
			client := flags.newClient()
			ctx := context.Background()
			resp, err := client.RollbackTo(ctx, types.RollbackToRequest{
				DeploymentKey: strings.TrimSpace(args[0]),
				ToGeneration:  toGeneration,
			})
			if err != nil {
				return err
			}

			renderer := flags.renderer()
			if flags.outputFormat() != OutputJSON {
				renderer.successLine(fmt.Sprintf("Rollback of %q to generation %d submitted.", resp.DeploymentKey, toGeneration))
			}
			if wait {
				return pollDeployment(ctx, client, resp.DeploymentKey, pollInterval, flags.outputFormat(), renderer)
			}
			if flags.outputFormat() == OutputJSON {
				return printJSON(resp)
			}
			renderer.successLine(fmt.Sprintf("Track progress with: praxis observe Deployment/%s", resp.DeploymentKey))
			return nil
		},
	}

	cmd.Flags().Int64Var(&toGeneration, "to", 0, "Target generation to roll back to (required)")
	cmd.Flags().BoolVar(&wait, "wait", false, "Wait for the rollback deployment to finish")
	cmd.Flags().DurationVar(&pollInterval, "poll-interval", 2*time.Second, "Polling interval when --wait is set")
	return cmd
}
