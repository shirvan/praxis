package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/shirvan/praxis/internal/core/orchestrator"
	"github.com/shirvan/praxis/pkg/types"
)

// newObserveCmd builds the `praxis observe` subcommand.
//
// Observe streams progress events in real time by polling the event stream
// for Deployments or resource status for individual cloud resources.
//
// Usage:
//
//	praxis observe Deployment/my-webapp
//	praxis observe S3Bucket/my-bucket
//	praxis observe Deployment/my-webapp --poll-interval 1s
//	praxis observe EC2Instance/web-1 -o json
func newObserveCmd(flags *rootFlags) *cobra.Command {
	var (
		pollInterval time.Duration
		timeout      time.Duration
		severity     string
		resource     string
		typePrefix   string
	)

	cmd := &cobra.Command{
		Use:   "observe <Kind/Key>",
		Short: "Watch a resource in real time",
		Long: `Watch a resource's status changes in real time.

For Deployments, observe polls the event stream and displays progress updates
as they happen, including status transitions, resource dispatches, and
completion/failure notifications. The command exits when the deployment
reaches a terminal state.

For individual cloud resources, observe polls the resource status and displays
changes as they happen, exiting when the resource reaches a terminal state
(Ready, Error, or Deleted).

Examples:
    praxis observe Deployment/my-webapp        Watch entire deployment
    praxis observe S3Bucket/my-bucket          Watch individual resource
    praxis observe EC2Instance/web-1 --timeout 2m`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			renderer := flags.renderer()
			kind, key, err := parseKindKey(args[0])
			if err != nil {
				return err
			}

			client := flags.newClient()
			ctx := context.Background()

			// Apply a timeout to the observe context if set.
			if timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}

			switch kind {
			case "Deployment":
				query := orchestrator.EventQuery{
					TypePrefix: typePrefix,
					Severity:   severity,
					Resource:   resource,
				}
				err = observeDeployment(ctx, client, key, pollInterval, query, flags.outputFormat(), renderer)
			default:
				err = observeResource(ctx, client, kind, key, pollInterval, flags.outputFormat(), renderer)
			}
			if isTimeoutError(ctx, err) {
				printTimeoutError(renderer, timeout, key)
				os.Exit(2)
			}
			return err
		},
	}

	cmd.Flags().DurationVar(&pollInterval, "poll-interval", 1*time.Second, "How frequently to poll for new events")
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Minute, "Maximum time to observe (0 for no limit)")
	cmd.Flags().StringVar(&severity, "severity", "", "Filter by severity (info, warn, error)")
	cmd.Flags().StringVar(&resource, "resource", "", "Filter by resource name")
	cmd.Flags().StringVar(&typePrefix, "type", "", "Filter by event type prefix")

	return cmd
}

// observeDeployment polls the event stream and deployment status, displaying
// incremental progress until a terminal state is reached.
func observeDeployment(ctx context.Context, client *Client, key string, interval time.Duration, query orchestrator.EventQuery, format OutputFormat, renderer *Renderer) error {
	_, _ = fmt.Fprintf(renderer.out, "%s\n\n", renderer.renderSection(fmt.Sprintf("Observing deployment %s...", key)))

	var lastSeq int64

	for {
		events, err := client.GetDeploymentCloudEvents(ctx, key, lastSeq)
		if err != nil {
			return fmt.Errorf("observe: fetch events: %w", err)
		}
		filtered := filterCloudEvents(events, query)
		if len(filtered) > 0 {
			if format == OutputJSON {
				if err := printJSON(filtered); err != nil {
					return err
				}
			} else {
				printCloudEvents(renderer, filtered)
			}
		}
		if len(events) > 0 {
			lastSeq = events[len(events)-1].Sequence
		}
		for i := range events {
			if isTerminalCloudEvent(events[i]) {
				status := cloudEventStatus(events[i].Event)
				_, _ = fmt.Fprintln(renderer.out)
				_, _ = fmt.Fprintf(renderer.out, "%s %s\n",
					renderer.renderSection("Deployment reached terminal state:"),
					renderer.renderStatus(status))
				_, _ = fmt.Fprintf(renderer.out, "%s\n",
					renderer.renderMuted("Run 'praxis get Deployment/"+key+"' for full details."))
				return nil
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

// observeResource polls a single cloud resource's status and displays changes
// until it reaches a terminal state (Ready, Error, or Deleted).
func observeResource(ctx context.Context, client *Client, kind, key string, interval time.Duration, format OutputFormat, renderer *Renderer) error {
	_, _ = fmt.Fprintf(renderer.out, "%s\n\n", renderer.renderSection(fmt.Sprintf("Observing %s/%s...", kind, key)))

	var lastStatus types.ResourceStatus
	for {
		resp, err := client.GetResourceStatus(ctx, kind, key)
		if err != nil {
			return fmt.Errorf("observe: get status: %w", err)
		}
		if resp.Status != lastStatus {
			if format == OutputJSON {
				if err := printJSON(resp); err != nil {
					return err
				}
			} else {
				ts := renderer.renderMuted(time.Now().Format("15:04:05"))
				_, _ = fmt.Fprintf(renderer.out, "%s  %s/%s  status=%s  mode=%s  gen=%d\n",
					ts,
					kind, key,
					renderer.renderStatus(string(resp.Status)),
					string(resp.Mode),
					resp.Generation)
				if resp.Error != "" {
					_, _ = fmt.Fprintf(renderer.out, "  %s\n", renderer.renderMuted("error: "+resp.Error))
				}
			}
			lastStatus = resp.Status
		}
		if isTerminalResourceStatus(resp.Status) {
			_, _ = fmt.Fprintln(renderer.out)
			_, _ = fmt.Fprintf(renderer.out, "%s %s\n",
				renderer.renderSection(fmt.Sprintf("%s/%s reached terminal state:", kind, key)),
				renderer.renderStatus(string(resp.Status)))
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

// isTerminalResourceStatus returns true for resource statuses that indicate
// an operation is complete and no further transitions are expected.
func isTerminalResourceStatus(s types.ResourceStatus) bool {
	switch s {
	case types.StatusReady, types.StatusError, types.StatusDeleted:
		return true
	default:
		return false
	}
}
