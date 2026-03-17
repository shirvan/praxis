package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// newGetCmd builds the `praxis get` subcommand.
//
// Get retrieves the current state of a deployment or individual resource. The
// argument format is <Kind>/<Key>:
//
//   - Deployment/<key>  — Shows deployment status with per-resource breakdown
//   - S3Bucket/<key>    — Shows a single S3 bucket resource status
//   - SecurityGroup/<key> — Shows a single security group status
//
// Usage:
//
//	praxis get Deployment/my-webapp
//	praxis get Deployment/my-webapp -o json
//	praxis get S3Bucket/my-bucket
func newGetCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <Kind>/<Key>",
		Short: "Show deployment or resource details",
		Long: `Get retrieves the current state of a deployment or an individual resource.

For deployments, it shows the overall status plus a per-resource breakdown
with any outputs collected during provisioning:

    praxis get Deployment/my-webapp

For individual resources, it shows the driver-level status and outputs:

    praxis get S3Bucket/my-bucket
    praxis get SecurityGroup/vpc-123~web-sg

The argument must be in Kind/Key format. Use 'praxis list deployments' to
discover available deployment keys.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind, key, err := parseKindKey(args[0])
			if err != nil {
				return err
			}

			// Resolve scope-aware key (e.g. prepend region for region-scoped kinds).
			if kind != "Deployment" {
				key = flags.resolveResourceKey(kind, key)
			}

			client := flags.newClient()
			ctx := context.Background()

			// Route to the appropriate handler based on the kind.
			if kind == "Deployment" {
				return getDeployment(ctx, client, key, flags.outputFormat())
			}
			return getResource(ctx, client, kind, key, flags.outputFormat())
		},
	}

	return cmd
}

// --------------------------------------------------------------------------
// Kind/Key parsing
// --------------------------------------------------------------------------

// parseKindKey splits a "Kind/Key" argument into its two components. The key
// may itself contain "/" characters (though canonical keys use "~"), so only
// the first "/" is the separator.
func parseKindKey(arg string) (kind, key string, err error) {
	parts := strings.SplitN(arg, "/", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", fmt.Errorf("invalid argument %q: expected Kind/Key (e.g., Deployment/my-webapp)", arg)
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), nil
}

// --------------------------------------------------------------------------
// Deployment get
// --------------------------------------------------------------------------

// getDeployment retrieves and displays a full deployment detail record.
func getDeployment(ctx context.Context, client *Client, key string, format OutputFormat) error {
	detail, err := client.GetDeployment(ctx, key)
	if err != nil {
		return err
	}
	if detail == nil {
		return fmt.Errorf("deployment %q not found", key)
	}

	if format == OutputJSON {
		return printJSON(detail)
	}

	printDeploymentDetail(detail)
	return nil
}

// --------------------------------------------------------------------------
// Resource get
// --------------------------------------------------------------------------

// getResource retrieves a single resource's status and outputs from its driver.
func getResource(ctx context.Context, client *Client, kind, key string, format OutputFormat) error {
	status, err := client.GetResourceStatus(ctx, kind, key)
	if err != nil {
		return err
	}

	outputs, err := client.GetResourceOutputs(ctx, kind, key)
	if err != nil {
		// Non-fatal: outputs may not be available for all resources.
		outputs = nil
	}

	// Build a combined view for display.
	result := map[string]any{
		"kind":       kind,
		"key":        key,
		"status":     status.Status,
		"mode":       status.Mode,
		"generation": status.Generation,
	}
	if status.Error != "" {
		result["error"] = status.Error
	}
	if outputs != nil {
		result["outputs"] = outputs
	}

	if format == OutputJSON {
		return printJSON(result)
	}

	// Human-readable resource display.
	fmt.Printf("Resource:   %s/%s\n", kind, key)
	fmt.Printf("Status:     %s\n", status.Status)
	fmt.Printf("Mode:       %s\n", status.Mode)
	fmt.Printf("Generation: %d\n", status.Generation)
	if status.Error != "" {
		fmt.Printf("Error:      %s\n", status.Error)
	}
	if len(outputs) > 0 {
		fmt.Println("\nOutputs:")
		for k, v := range outputs {
			fmt.Printf("  %s = %v\n", k, v)
		}
	}
	return nil
}
