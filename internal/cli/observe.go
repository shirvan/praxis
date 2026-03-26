package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/shirvan/praxis/internal/core/orchestrator"
)

// newObserveCmd builds the `praxis observe` subcommand.
//
// Observe streams deployment progress events in real time by polling the
// DeploymentEvents virtual object. It displays an incremental timeline until
// the deployment reaches a terminal status.
//
// Usage:
//
//	praxis observe Deployment/my-webapp
//	praxis observe Deployment/my-webapp --poll-interval 1s
//	praxis observe Deployment/my-webapp -o json
func newObserveCmd(flags *rootFlags) *cobra.Command {
	var (
		pollInterval time.Duration
		timeout      time.Duration
		severity     string
		resource     string
		typePrefix   string
	)

	cmd := &cobra.Command{
		Use:   "observe Deployment/<key>",
		Short: "Watch deployment progress in real time",
		Long: `Observe polls the deployment event stream and displays progress updates
as they happen. This is useful for watching apply or delete operations
progress through their resource DAG.

Events include status transitions, resource dispatches, and completion/failure
notifications. The command exits when the deployment reaches a terminal state
(Complete, Failed, Deleted, or Cancelled).

    praxis observe Deployment/my-webapp

Control the polling speed with --poll-interval:

    praxis observe Deployment/my-webapp --poll-interval 500ms`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			renderer := flags.renderer()
			kind, key, err := parseKindKey(args[0])
			if err != nil {
				return err
			}
			if kind != "Deployment" {
				return fmt.Errorf("observe only supports Deployment resources, got %q", kind)
			}

			client := flags.newClient()
			ctx := context.Background()

			// Apply a timeout to the observe context if set.
			if timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}

			query := orchestrator.EventQuery{
				TypePrefix: typePrefix,
				Severity:   severity,
				Resource:   resource,
			}
			err = observeDeployment(ctx, client, key, pollInterval, query, flags.outputFormat(), renderer)
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
				detail, detailErr := client.GetDeployment(ctx, key)
				if detailErr != nil {
					return fmt.Errorf("observe: fetch final state: %w", detailErr)
				}
				if detail != nil && format != OutputJSON {
					_, _ = fmt.Fprintln(renderer.out)
					printDeploymentDetail(renderer, detail)
				}
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
