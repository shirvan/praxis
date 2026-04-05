package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/shirvan/praxis/pkg/types"
)

// newDeleteCmd builds the `praxis delete` subcommand.
//
// Delete supports Deployment teardown (with --wait, --rollback), and deletion
// of meta-resources: workspaces, templates, sinks, and concierge sessions.
//
// Usage:
//
//	praxis delete Deployment/my-webapp --yes
//	praxis delete workspace/staging
//	praxis delete template/webapp
//	praxis delete sink/my-hook
//	praxis delete concierge [--session <id>]
func newDeleteCmd(flags *rootFlags) *cobra.Command {
	var (
		yes      bool
		wait     bool
		rollback bool
		timeout  time.Duration
		session  string
	)

	cmd := &cobra.Command{
		Use:   "delete <Kind>/<Key>",
		Short: "Delete a resource",
		Long: `Delete removes a resource from Praxis.

For deployments, it initiates an asynchronous teardown of all resources in
reverse dependency order:

    praxis delete Deployment/my-webapp --yes
    praxis delete Deployment/my-webapp --yes --wait

For individual cloud resources, it calls the driver's Delete handler:

    praxis delete S3Bucket/my-bucket --yes
    praxis delete EC2Instance/us-east-1~web-server --yes

For meta-resources, it removes the configuration:

    praxis delete workspace/staging
    praxis delete template/webapp
    praxis delete sink/my-hook
    praxis delete concierge            Delete the default concierge session

Use --yes / -y to skip the confirmation prompt.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			renderer := flags.renderer()
			kind, key, err := parseKindKey(args[0])
			if err != nil {
				return err
			}

			switch kind {
			case "Deployment":
				return deleteDeployment(flags, renderer, key, yes, wait, rollback, timeout)
			case "workspace":
				return deleteWorkspace(flags, key)
			case "template":
				return deleteTemplate(flags, key)
			case "sink":
				return deleteSink(flags, key)
			case "concierge":
				return deleteConciergeSession(flags, key)
			default:
				// Cloud resource deletion (e.g. S3Bucket/my-bucket)
				return deleteResource(flags, kind, key, yes)
			}
		},
	}

	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip confirmation prompt")
	cmd.Flags().BoolVar(&wait, "wait", false, "Wait for deletion to complete")
	cmd.Flags().BoolVar(&rollback, "rollback", false, "Delete only resources proven ready by the event store for a failed or cancelled deployment")
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Minute, "Maximum time to wait for completion (0 for no limit)")
	cmd.Flags().StringVar(&session, "session", "", "Concierge session ID for delete concierge (default: key value)")

	return cmd
}

// deleteDeployment handles the Deployment teardown flow.
func deleteDeployment(flags *rootFlags, renderer *Renderer, key string, yes, wait, rollback bool, timeout time.Duration) error {
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

	var (
		resp *types.DeleteDeploymentResponse
		err  error
	)
	if rollback {
		resp, err = client.RollbackDeployment(ctx, key)
	} else {
		resp, err = client.DeleteDeployment(ctx, key)
	}
	if err != nil {
		return err
	}

	if flags.outputFormat() == OutputJSON {
		return printJSON(resp)
	}

	renderer.writeLabelValue("Deployment", 11, resp.DeploymentKey)
	renderer.writeLabelStyledValue("Status", 11, renderer.renderStatus(string(resp.Status)))

	if !wait {
		message := "Deletion in progress. Use 'praxis get Deployment/" + key + "' to check progress."
		if rollback {
			message = "Rollback in progress. Use 'praxis get Deployment/" + key + "' to check progress."
		}
		_, _ = fmt.Fprintln(renderer.out, "\n"+renderer.renderMuted(message))
		return nil
	}

	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
	}

	err = pollDeployment(ctx, client, key, 2*time.Second, flags.outputFormat(), renderer)
	if cancel != nil {
		cancel()
	}
	if isTimeoutError(ctx, err) {
		printTimeoutError(renderer, timeout, key)
		os.Exit(2)
	}
	return err
}

// deleteWorkspace removes a workspace from the configuration.
func deleteWorkspace(flags *rootFlags, name string) error {
	renderer := flags.renderer()
	client := flags.newClient()
	ctx := context.Background()

	if err := client.DeleteWorkspace(ctx, name); err != nil {
		return err
	}

	cliCfg := LoadCLIConfig()
	if cliCfg.ActiveWorkspace == name {
		cliCfg.ActiveWorkspace = ""
		if err := SaveCLIConfig(cliCfg); err != nil {
			return fmt.Errorf("save config: %w", err)
		}
	}

	renderer.successLine(fmt.Sprintf("Workspace %q deleted.", name))
	return nil
}

// deleteTemplate removes a template from the registry.
func deleteTemplate(flags *rootFlags, name string) error {
	renderer := flags.renderer()
	client := flags.newClient()
	ctx := context.Background()

	if err := client.DeleteTemplate(ctx, name); err != nil {
		return err
	}

	if flags.outputFormat() == OutputJSON {
		return printJSON(map[string]string{"deleted": name})
	}

	renderer.successLine(fmt.Sprintf("Deleted template %q", name))
	return nil
}

// deleteSink removes a notification sink.
func deleteSink(flags *rootFlags, name string) error {
	if err := flags.newClient().RemoveNotificationSink(context.Background(), name); err != nil {
		return err
	}
	if flags.outputFormat() == OutputJSON {
		return printJSON(map[string]string{"removed": name})
	}
	flags.renderer().successLine(fmt.Sprintf("Notification sink %q removed.", name))
	return nil
}

// deleteConciergeSession clears a concierge session.
func deleteConciergeSession(flags *rootFlags, sessionID string) error {
	if sessionID == "" {
		sessionID = "default"
	}
	client := flags.newClient()
	if err := client.ConciergeReset(context.Background(), sessionID); err != nil {
		if isConciergeUnavailable(err) {
			fmt.Fprint(os.Stderr, conciergeUnavailableMsg)
			return nil
		}
		return fmt.Errorf("delete concierge: %w", err)
	}
	flags.renderer().successLine(fmt.Sprintf("Concierge session %q reset", sessionID))
	return nil
}

// deleteResource deletes an individual cloud resource by calling the driver's
// Delete handler on its Restate Virtual Object.
func deleteResource(flags *rootFlags, kind, key string, yes bool) error {
	renderer := flags.renderer()
	resolvedKey := flags.resolveResourceKey(kind, key)

	if !yes {
		_, _ = fmt.Fprintf(renderer.out, "%s ", renderer.renderPrompt(fmt.Sprintf("Delete %s/%s? This cannot be undone. [y/N]:", kind, resolvedKey)))
		var confirm string
		if _, err := fmt.Scanln(&confirm); err != nil || (confirm != "y" && confirm != "Y") {
			_, _ = fmt.Fprintln(renderer.out, renderer.renderMuted("Cancelled."))
			return nil
		}
	}

	client := flags.newClient()
	ctx := context.Background()

	if err := client.DeleteResource(ctx, kind, resolvedKey); err != nil {
		return err
	}

	if flags.outputFormat() == OutputJSON {
		return printJSON(map[string]string{"deleted": kind + "/" + resolvedKey})
	}

	renderer.successLine(fmt.Sprintf("Deleted %s/%s", kind, resolvedKey))
	return nil
}
