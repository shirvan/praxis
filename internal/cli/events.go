// events.go implements the `praxis events` command group.
//
// Events are CloudEvents emitted by the orchestrator during deployment
// lifecycle transitions. They are stored in a per-deployment event store
// (DeploymentEventStore Virtual Object) and indexed globally (EventIndex).
//
// Two subcommands provide access:
//   - `praxis events list Deployment/<key>`  — list events for one deployment
//   - `praxis events query`                 — cross-deployment event search
package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/shirvan/praxis/internal/core/orchestrator"
)

// newEventsCmd builds the `praxis events` parent command.
// Subcommands: list, query.
func newEventsCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "events",
		Short: "Query deployment events",
	}

	cmd.AddCommand(
		newEventsListCmd(flags),
		newEventsQueryCmd(flags),
	)
	return cmd
}

// newEventsListCmd builds `praxis events list Deployment/<key>`.
// Fetches all events for one deployment from DeploymentEventStore.ListSince,
// applies client-side filters (--type, --severity, --resource, --since), and
// renders the filtered event timeline.
func newEventsListCmd(flags *rootFlags) *cobra.Command {
	var (
		sinceRaw   string
		typePrefix string
		severity   string
		resource   string
	)

	cmd := &cobra.Command{
		Use:   "list Deployment/<key>",
		Short: "List events for one deployment",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind, key, err := parseKindKey(args[0])
			if err != nil {
				return err
			}
			if kind != "Deployment" {
				return fmt.Errorf("events list only supports Deployment resources, got %q", kind)
			}
			since, err := parseLookback(sinceRaw)
			if err != nil {
				return err
			}
			query := orchestrator.EventQuery{
				DeploymentKey: key,
				TypePrefix:    typePrefix,
				Severity:      severity,
				Resource:      resource,
				Since:         since,
			}
			return listDeploymentEvents(context.Background(), flags.newClient(), key, query, flags.outputFormat(), flags.renderer())
		},
	}

	cmd.Flags().StringVar(&sinceRaw, "since", "", "Show events from the last duration (for example: 1h, 7d)")
	cmd.Flags().StringVar(&typePrefix, "type", "", "Filter by event type prefix")
	cmd.Flags().StringVar(&severity, "severity", "", "Filter by severity (info, warn, error)")
	cmd.Flags().StringVar(&resource, "resource", "", "Filter by resource name")
	return cmd
}

// newEventsQueryCmd builds `praxis events query`. Runs a cross-deployment
// search against EventIndex.Query with workspace, type, severity, resource,
// since, and limit filters.
func newEventsQueryCmd(flags *rootFlags) *cobra.Command {
	var (
		workspace  string
		typePrefix string
		severity   string
		resource   string
		sinceRaw   string
		limit      int
	)

	cmd := &cobra.Command{
		Use:   "query",
		Short: "Query events across deployments",
		RunE: func(cmd *cobra.Command, args []string) error {
			since, err := parseLookback(sinceRaw)
			if err != nil {
				return err
			}
			query := orchestrator.EventQuery{
				Workspace:  workspace,
				TypePrefix: typePrefix,
				Severity:   severity,
				Resource:   resource,
				Since:      since,
				Limit:      limit,
			}
			events, err := flags.newClient().QueryCloudEvents(context.Background(), query)
			if err != nil {
				return err
			}
			if flags.outputFormat() == OutputJSON {
				return printJSON(events)
			}
			printCloudEvents(flags.renderer(), events)
			return nil
		},
	}

	cmd.Flags().StringVarP(&workspace, "workspace", "w", "", "Filter by workspace")
	cmd.Flags().StringVar(&typePrefix, "type", "", "Filter by event type prefix")
	cmd.Flags().StringVar(&severity, "severity", "", "Filter by severity (info, warn, error)")
	cmd.Flags().StringVar(&resource, "resource", "", "Filter by resource name")
	cmd.Flags().StringVar(&sinceRaw, "since", "", "Show events from the last duration (for example: 1h, 7d)")
	cmd.Flags().IntVar(&limit, "limit", 100, "Maximum events to return")
	return cmd
}

// listDeploymentEvents fetches all events for a deployment (since sequence 0),
// applies the query filter client-side, and renders the result.
func listDeploymentEvents(ctx context.Context, client *Client, key string, query orchestrator.EventQuery, format OutputFormat, renderer *Renderer) error {
	events, err := client.GetDeploymentCloudEvents(ctx, key, 0)
	if err != nil {
		return err
	}
	filtered := filterCloudEvents(events, query)
	if format == OutputJSON {
		return printJSON(filtered)
	}
	printCloudEvents(renderer, filtered)
	return nil
}

// parseLookback converts a human-friendly duration string (e.g. "1h", "7d")
// into a UTC timestamp by subtracting it from now. Returns the zero time for
// empty input.
func parseLookback(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	dur, err := parseFlexibleDuration(raw)
	if err != nil {
		return time.Time{}, err
	}
	return time.Now().UTC().Add(-dur), nil
}

// parseFlexibleDuration extends Go's time.ParseDuration with support for
// the "d" (day) suffix. "7d" becomes 7 * 24h.
func parseFlexibleDuration(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if count, ok := strings.CutSuffix(raw, "d"); ok {
		parsed, err := time.ParseDuration(count + "h")
		if err == nil {
			return parsed * 24, nil
		}
	}
	dur, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q", raw)
	}
	return dur, nil
}
