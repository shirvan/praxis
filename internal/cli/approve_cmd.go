package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// newApproveCmd builds the `praxis approve` top-level verb.
// Resolves a Restate Awakeable that the concierge is blocked on.
func newApproveCmd(flags *rootFlags) *cobra.Command {
	var (
		awakeableID string
		reject      bool
		reason      string
	)

	cmd := &cobra.Command{
		Use:   "approve",
		Short: "Approve or reject a pending concierge action",
		Long: `Approve or reject a pending action that the Concierge AI is waiting on.

    praxis approve --awakeable-id <id>
    praxis approve --awakeable-id <id> --reject --reason "too risky"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if awakeableID == "" {
				return fmt.Errorf("--awakeable-id is required")
			}

			client := flags.newClient()
			req := conciergeApprovalRequest{
				AwakeableID: awakeableID,
				Approved:    !reject,
				Reason:      reason,
				Actor:       "cli-user",
			}

			if err := client.ConciergeApprove(context.Background(), req); err != nil {
				if isConciergeUnavailable(err) {
					fmt.Fprint(os.Stderr, conciergeUnavailableMsg)
					return nil
				}
				return fmt.Errorf("approve: %w", err)
			}

			r := flags.renderer()
			if reject {
				r.successLine("Action rejected")
			} else {
				r.successLine("Action approved")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&awakeableID, "awakeable-id", "", "Awakeable ID from the pending approval (required)")
	cmd.Flags().BoolVar(&reject, "reject", false, "Reject the action instead of approving")
	cmd.Flags().StringVar(&reason, "reason", "", "Reason for approval or rejection")
	return cmd
}
