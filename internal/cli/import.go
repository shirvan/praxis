package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/praxiscloud/praxis/pkg/types"
)

// newImportCmd builds the `praxis import` subcommand.
//
// Import adopts an existing cloud resource under Praxis management without
// recreating it. The resource is discovered through the cloud provider API
// and brought into the driver's state machine.
//
// Usage:
//
//	praxis import S3Bucket --id my-existing-bucket --region us-east-1
//	praxis import EC2Instance --id i-0abc123 --region us-east-1 --observe
//	praxis import SecurityGroup --id sg-0abc123 --region us-east-1
//	praxis import S3Bucket --id my-bucket --region us-west-2 --observe
//	praxis import S3Bucket --id my-bucket --region us-east-1 -o json
func newImportCmd(flags *rootFlags) *cobra.Command {
	var (
		resourceID string
		region     string
		observe    bool
		account    string
	)
	account = flags.account

	cmd := &cobra.Command{
		Use:   "import <Kind>",
		Short: "Adopt an existing cloud resource under Praxis management",
		Long: `Import discovers an existing cloud resource by its provider-native identifier
and brings it under Praxis management.

The resource must already exist in the cloud provider. Import reads its current
state and creates a corresponding Restate Virtual Object entry, but does not
modify the resource itself.

Required flags:

    --id       The cloud-provider-native resource identifier
			   (S3: bucket name, EC2: instance ID, SecurityGroup: group ID)
    --region   The AWS region where the resource lives

Optional flags:

    --observe  Import in observed mode — Praxis tracks the resource but never
               modifies it. Drift is reported but not corrected. This is useful
               for monitoring resources managed by another system.

Examples:

    praxis import S3Bucket --id my-bucket --region us-east-1
	praxis import EC2Instance --id i-0abc123 --region us-east-1 --observe
    praxis import SecurityGroup --id sg-0abc123 --region us-east-1 --observe`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind := args[0]

			if resourceID == "" {
				return fmt.Errorf("--id is required")
			}
			// Fall back to the global --region / PRAXIS_REGION if --region is not set locally.
			if region == "" {
				region = flags.region
			}
			if region == "" {
				return fmt.Errorf("--region is required (or set PRAXIS_REGION)")
			}

			mode := types.ModeManaged
			if observe {
				mode = types.ModeObserved
			}

			client := flags.newClient()
			ctx := context.Background()

			resp, err := client.ImportResource(ctx, types.ImportRequest{
				Kind:       kind,
				ResourceID: resourceID,
				Region:     region,
				Mode:       mode,
				Account:    account,
			})
			if err != nil {
				return err
			}

			if flags.outputFormat() == OutputJSON {
				return printJSON(resp)
			}

			printImportResult(resp)
			return nil
		},
	}

	cmd.Flags().StringVar(&resourceID, "id", "", "Cloud-provider-native resource identifier (required)")
	cmd.Flags().StringVar(&region, "region", "", "AWS region (falls back to --region flag or PRAXIS_REGION)")
	cmd.Flags().StringVar(&account, "account", account, "AWS account name to use (env: PRAXIS_ACCOUNT)")
	cmd.Flags().BoolVar(&observe, "observe", false, "Import in observed mode (track but never modify)")

	// Mark required flags so cobra reports a clear error if missing.
	_ = cmd.MarkFlagRequired("id")

	return cmd
}
