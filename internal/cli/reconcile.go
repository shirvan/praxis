package cli

import (
	"context"
	"fmt"
	"strings"

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
	var force bool
	cmd := &cobra.Command{
		Use:   "reconcile <Kind>/<Key> | <deployment>",
		Short: "Trigger on-demand drift detection and correction",
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
    praxis reconcile my-deployment
    praxis reconcile Deployment/my-deployment
    praxis reconcile S3Bucket/my-bucket -o json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client := flags.newClient()
			ctx := context.Background()

			if !strings.Contains(args[0], "/") {
				return reconcileDeployment(flags, client, ctx, args[0], force)
			}

			kind, key, err := parseKindKey(args[0])
			if err != nil {
				return err
			}
			if kind == "Deployment" {
				return reconcileDeployment(flags, client, ctx, key, force)
			}

			key = flags.resolveResourceKey(kind, key)

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
	cmd.Flags().BoolVar(&force, "force", false, "Include pending, skipped, deleted, or orphaned resources when reconciling a deployment")

	return cmd
}

func reconcileDeployment(flags *rootFlags, client *Client, ctx context.Context, deploymentKey string, force bool) error {
	result, err := client.ReconcileDeployment(ctx, deploymentKey, force)
	if err != nil {
		return err
	}
	if flags.outputFormat() == OutputJSON {
		return printJSON(result)
	}
	renderer := flags.renderer()
	renderer.writeLabelValue("Deployment", 11, deploymentKey)
	renderer.writeLabelValue("Triggered", 11, fmt.Sprintf("%d", result.Triggered))
	if len(result.Skipped) > 0 {
		renderer.writeLabelValue("Skipped", 11, strings.Join(result.Skipped, ", "))
	}
	if result.Triggered == 0 {
		renderer.writeLabelValue("Result", 11, "No eligible resources to reconcile")
	}
	return nil
}
