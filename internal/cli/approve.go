// approve.go implements `praxis approve` and `praxis reject` — the operator
// decision pair for deployments suspended at an approval gate. Deployments
// into a protected workspace park in AwaitingApproval until one of these
// commands resolves the gate; the decision (who, when, optional comment)
// lands in the deployment event stream as the audit record.
package cli

import (
	"context"
	"fmt"
	"os/user"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shirvan/praxis/pkg/types"
)

func newApproveCmd(flags *rootFlags) *cobra.Command {
	return newApprovalDecisionCmd(flags, true)
}

func newRejectCmd(flags *rootFlags) *cobra.Command {
	return newApprovalDecisionCmd(flags, false)
}

func newApprovalDecisionCmd(flags *rootFlags, approve bool) *cobra.Command {
	verb := "approve"
	short := "Approve a deployment that is awaiting approval"
	long := `Approve resumes a deployment suspended at its approval gate.

Deployments into a protected workspace stop in AwaitingApproval before any
resource is dispatched. Approving resumes the workflow exactly where it
suspended; the decision is recorded in the deployment event stream.`
	if !approve {
		verb = "reject"
		short = "Reject a deployment that is awaiting approval"
		long = `Reject terminates a deployment suspended at its approval gate.

The deployment finalizes as Cancelled without dispatching any resource; the
decision is recorded in the deployment event stream.`
	}

	var comment string
	var decidedBy string
	cmd := &cobra.Command{
		Use:   verb + " <deployment-key>",
		Short: short,
		Long:  long,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := types.ApprovalRequest{
				DeploymentKey: strings.TrimSpace(args[0]),
				DecidedBy:     decidedBy,
				Comment:       comment,
			}
			if req.DecidedBy == "" {
				if current, err := user.Current(); err == nil {
					req.DecidedBy = current.Username
				}
			}

			client := flags.newClient()
			ctx := context.Background()
			var resp *types.ApprovalResponse
			var err error
			if approve {
				resp, err = client.ApproveDeployment(ctx, req)
			} else {
				resp, err = client.RejectDeployment(ctx, req)
			}
			if err != nil {
				return err
			}

			if flags.outputFormat() == OutputJSON {
				return printJSON(resp)
			}
			renderer := flags.renderer()
			if approve {
				renderer.successLine(fmt.Sprintf("Deployment %q approved; resuming.", resp.DeploymentKey))
			} else {
				renderer.successLine(fmt.Sprintf("Deployment %q rejected; cancelling.", resp.DeploymentKey))
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&comment, "comment", "", "Optional rationale recorded in the audit event")
	cmd.Flags().StringVar(&decidedBy, "decided-by", "", "Identity recorded in the audit event (defaults to the local username)")
	return cmd
}
