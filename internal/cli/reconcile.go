package cli

import (
	"context"

	"github.com/spf13/cobra"
)

// newReconcileCmd builds the `praxis reconcile` subcommand.
//
// Reconcile triggers an on-demand drift detection and correction cycle for a
// single resource. Normally reconciliation runs automatically every 5 minutes;
// this command lets operators check immediately.
//
// Usage:
//
//	praxis reconcile S3Bucket/my-bucket
//	praxis reconcile EC2Instance/us-east-1~web-server
//	praxis reconcile SecurityGroup/vpc-123~web-sg -o json
func newReconcileCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reconcile <Kind>/<Key>",
		Short: "Trigger on-demand drift detection and correction for a resource",
		Long: `Reconcile checks whether a managed resource has drifted from its desired
state by querying the cloud provider and comparing the actual configuration
against the stored spec.

In Managed mode, any detected drift is automatically corrected. In Observed
mode, drift is reported but not corrected.

Normally, reconciliation runs automatically every 5 minutes via durable
timers. Use this command to check drift on-demand without waiting for the
next scheduled cycle — for example, after a manual change in the AWS
console, or to diagnose why a resource is in Error status.

    praxis reconcile S3Bucket/my-bucket
    praxis reconcile SecurityGroup/vpc-123~web-sg
    praxis reconcile EC2Instance/us-east-1~web-server
    praxis reconcile S3Bucket/my-bucket -o json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind, key, err := parseKindKey(args[0])
			if err != nil {
				return err
			}

			key = flags.resolveResourceKey(kind, key)

			client := flags.newClient()
			ctx := context.Background()

			result, err := client.ReconcileResource(ctx, kind, key)
			if err != nil {
				return err
			}

			if flags.outputFormat() == OutputJSON {
				return printJSON(result)
			}

			renderer := flags.renderer()
			renderer.writeLabelValue("Resource", 11, kind+"/"+key)

			if result.Drift {
				renderer.writeLabelStyledValue("Drift", 11, renderer.renderStatus("Failed")+" — resource has drifted")
			} else {
				renderer.writeLabelStyledValue("Drift", 11, renderer.renderStatus("Ready")+" — no drift")
			}
			if result.Correcting {
				renderer.writeLabelStyledValue("Correcting", 11, renderer.renderStatus("Applying"))
			} else {
				renderer.writeLabelValue("Correcting", 11, "false")
			}

			if result.Error != "" {
				renderer.writeLabelValue("Error", 11, result.Error)
			}
			if !result.Drift && result.Error == "" {
				renderer.successLine("Resource is in sync — no drift detected.")
			}
			return nil
		},
	}

	return cmd
}
