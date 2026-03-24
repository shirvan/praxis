package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shirvan/praxis/pkg/types"
)

// newStateCmd builds the `praxis state` command group.
func newStateCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "state",
		Short: "Manage deployment state",
		Long: `State commands operate on the durable deployment record without touching
cloud resources. Use these to fix name mismatches after template refactoring
or to move resources between deployments.`,
	}

	cmd.AddCommand(newStateMvCmd(flags))

	return cmd
}

// newStateMvCmd builds the `praxis state mv` subcommand.
//
// Usage:
//
//	praxis state mv Deployment/web-app/myBucket newBucketName
//	praxis state mv Deployment/web-app/myBucket Deployment/data-stack/myBucket
//	praxis state mv Deployment/web-app/myBucket Deployment/data-stack/newName
func newStateMvCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mv <source> <destination>",
		Short: "Rename or move a resource between deployments",
		Long: `Move renames a resource within a deployment or relocates it to another
deployment. No cloud resources are created, modified, or deleted — only the
deployment state mapping is updated.

Source format:  Deployment/<key>/<resource-name>
Destination:    <new-name>                          (rename within same deployment)
                Deployment/<key>/<resource-name>     (move to another deployment)

Examples:

    # Rename a resource within the same deployment
    praxis state mv Deployment/web-app/myBucket newBucketName

    # Move a resource to another deployment, keeping its name
    praxis state mv Deployment/web-app/myBucket Deployment/data-stack/myBucket

    # Move and rename in one step
    praxis state mv Deployment/web-app/myBucket Deployment/data-stack/dataBucket`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
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
				fmt.Printf("Renamed %s → %s in deployment %s\n",
					resp.OldName, resp.NewName, resp.SourceDeployment)
			} else {
				fmt.Printf("Moved %s from %s to %s as %s\n",
					resp.OldName, resp.SourceDeployment, resp.DestDeployment, resp.NewName)
			}
			return nil
		},
	}

	return cmd
}

// parseStatePath splits "Deployment/<key>/<resource>" into deployment key and
// resource name.
func parseStatePath(arg string) (deploymentKey, resourceName string, err error) {
	parts := strings.SplitN(arg, "/", 3)
	if len(parts) != 3 || parts[0] != "Deployment" {
		return "", "", fmt.Errorf("expected Deployment/<key>/<resource>, got %q", arg)
	}
	if parts[1] == "" {
		return "", "", fmt.Errorf("deployment key cannot be empty in %q", arg)
	}
	if parts[2] == "" {
		return "", "", fmt.Errorf("resource name cannot be empty in %q", arg)
	}
	return parts[1], parts[2], nil
}

// parseDestination interprets the destination argument. It can be either:
//   - A bare name (rename within the same deployment)
//   - Deployment/<key>/<resource> (move to another deployment)
func parseDestination(arg, srcDeployment, srcResource string) (deploymentKey, resourceName string, err error) {
	if strings.HasPrefix(arg, "Deployment/") {
		return parseStatePath(arg)
	}
	// Bare name → rename within source deployment.
	if arg == "" {
		return "", "", fmt.Errorf("destination name cannot be empty")
	}
	if strings.Contains(arg, "/") {
		return "", "", fmt.Errorf("destination %q contains '/' but does not start with 'Deployment/'; use Deployment/<key>/<resource> for cross-deployment moves", arg)
	}
	return srcDeployment, arg, nil
}
