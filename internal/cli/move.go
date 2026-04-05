package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/shirvan/praxis/pkg/types"
)

// newMoveCmd builds the `praxis move` top-level verb.
// Renames a resource within a deployment or moves it to another deployment.
func newMoveCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "move <source> <destination>",
		Short: "Rename or move a resource between deployments",
		Long: `Move renames a resource within a deployment or relocates it to another
deployment. No cloud resources are created, modified, or deleted — only the
deployment state mapping is updated.

Source format:  Deployment/<key>/<resource-name>
Destination:    <new-name>                          (rename within same deployment)
                Deployment/<key>/<resource-name>     (move to another deployment)

Examples:

    praxis move Deployment/web-app/myBucket newBucketName
    praxis move Deployment/web-app/myBucket Deployment/data-stack/myBucket
    praxis move Deployment/web-app/myBucket Deployment/data-stack/dataBucket`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			renderer := flags.renderer()
			srcDeployment, srcResource, err := parseStatePath(args[0])
			if err != nil {
				return fmt.Errorf("invalid source: %w", err)
			}

			destDeployment, destResource, err := parseDestination(args[1], srcDeployment, srcResource)
			if err != nil {
				return fmt.Errorf("invalid destination: %w", err)
			}

			client := flags.newClient()
			ctx := context.Background()

			resp, err := client.StateMv(ctx, types.StateMvRequest{
				SourceDeployment: srcDeployment,
				ResourceName:     srcResource,
				DestDeployment:   destDeployment,
				NewName:          destResource,
			})
			if err != nil {
				return err
			}

			if flags.outputFormat() == OutputJSON {
				return printJSON(resp)
			}

			if resp.SourceDeployment == resp.DestDeployment {
				renderer.successLine(fmt.Sprintf("Renamed %s -> %s in deployment %s",
					resp.OldName, resp.NewName, resp.SourceDeployment))
			} else {
				renderer.successLine(fmt.Sprintf("Moved %s from %s to %s as %s",
					resp.OldName, resp.SourceDeployment, resp.DestDeployment, resp.NewName))
			}
			return nil
		},
	}

	return cmd
}
