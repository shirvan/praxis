package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
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

			err = observeDeployment(ctx, client, key, pollInterval, flags.outputFormat())
			if isTimeoutError(ctx, err) {
				printTimeoutError(timeout, key)
				os.Exit(2)
			}
			return err
		},
	}

	cmd.Flags().DurationVar(&pollInterval, "poll-interval", 1*time.Second, "How frequently to poll for new events")
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Minute, "Maximum time to observe (0 for no limit)")

	return cmd
}

// observeDeployment polls the event stream and deployment status, displaying
// incremental progress until a terminal state is reached.
func observeDeployment(ctx context.Context, client *Client, key string, interval time.Duration, format OutputFormat) error {
	fmt.Printf("Observing deployment %s...\n\n", key)

	var lastSeq int64

	for {
		// Try to get new events since the last seen sequence number.
		events, err := client.GetDeploymentEvents(ctx, key, lastSeq)
		if err != nil {
			// If events aren't available, fall back to status polling.
			detail, statusErr := client.GetDeployment(ctx, key)
			if statusErr != nil {
				return fmt.Errorf("observe: events unavailable (%v) and status query failed: %w", err, statusErr)
			}
			if detail == nil {
				return fmt.Errorf("deployment %q not found", key)
			}

			if format == OutputJSON {
				if err := printJSON(detail); err != nil {
					return err
				}
			} else {
				fmt.Printf("Status: %s (event stream unavailable, polling status)\n", detail.Status)
			}

			if isTerminalStatus(detail.Status) {
				if format != OutputJSON {
					fmt.Println()
					printDeploymentDetail(detail)
				}
				return nil
			}
		} else if len(events) > 0 {
			// Display new events.
			if format == OutputJSON {
				if err := printJSON(events); err != nil {
					return err
				}
			} else {
				printEvents(events)
			}

			// Update the cursor to the last event's sequence.
			lastSeq = events[len(events)-1].Sequence

			// Check if any event indicates a terminal deployment status.
			for _, e := range events {
				if isTerminalStatus(e.Status) {
					// Fetch and display the final state.
					detail, err := client.GetDeployment(ctx, key)
					if err != nil {
						return fmt.Errorf("observe: fetch final state: %w", err)
					}
					if detail != nil && format != OutputJSON {
						fmt.Println()
						printDeploymentDetail(detail)
					}
					return nil
				}
			}
		}

		// Wait before the next poll.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
			// Continue polling.
		}
	}
}
