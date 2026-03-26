package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shirvan/praxis/internal/core/orchestrator"
)

func newNotificationsCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "notifications",
		Short: "Manage notification sinks",
	}

	cmd.AddCommand(
		newNotificationAddSinkCmd(flags),
		newNotificationListSinksCmd(flags),
		newNotificationHealthCmd(flags),
		newNotificationGetSinkCmd(flags),
		newNotificationRemoveSinkCmd(flags),
		newNotificationTestSinkCmd(flags),
	)
	return cmd
}

func newNotificationAddSinkCmd(flags *rootFlags) *cobra.Command {
	var (
		name             string
		sinkType         string
		url              string
		typeFilters      string
		categoryFilters  string
		severityFilters  string
		workspaceFilters string
		deploymentFilter string
		headers          []string
		maxRetries       int
		backoffMs        int
		fromFile         string
		contentMode      string
	)

	cmd := &cobra.Command{
		Use:   "add-sink",
		Short: "Create or update a notification sink",
		RunE: func(cmd *cobra.Command, args []string) error {
			sink, err := buildNotificationSink(fromFile, name, sinkType, url, typeFilters, categoryFilters, severityFilters, workspaceFilters, deploymentFilter, headers, maxRetries, backoffMs, contentMode)
			if err != nil {
				return err
			}
			if err := flags.newClient().UpsertNotificationSink(context.Background(), sink); err != nil {
				return err
			}
			if flags.outputFormat() == OutputJSON {
				return printJSON(sink)
			}
			flags.renderer().successLine(fmt.Sprintf("Notification sink %q saved.", sink.Name))
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Sink name")
	cmd.Flags().StringVar(&sinkType, "type", "", "Sink type: webhook, structured_log, cloudevents_http")
	cmd.Flags().StringVar(&url, "url", "", "Endpoint URL for webhook or cloudevents_http sinks")
	cmd.Flags().StringVar(&typeFilters, "filter-types", "", "Comma-separated event type prefixes")
	cmd.Flags().StringVar(&categoryFilters, "filter-categories", "", "Comma-separated event categories")
	cmd.Flags().StringVar(&severityFilters, "filter-severities", "", "Comma-separated severities")
	cmd.Flags().StringVar(&workspaceFilters, "filter-workspaces", "", "Comma-separated workspace names")
	cmd.Flags().StringVar(&deploymentFilter, "filter-deployments", "", "Comma-separated deployment globs")
	cmd.Flags().StringArrayVar(&headers, "header", nil, "HTTP header in key=value form")
	cmd.Flags().IntVar(&maxRetries, "max-retries", 3, "Maximum delivery retry attempts")
	cmd.Flags().IntVar(&backoffMs, "backoff-ms", 1000, "Initial delivery retry backoff in milliseconds")
	cmd.Flags().StringVar(&fromFile, "from-file", "", "Read sink config from JSON file or - for stdin")
	cmd.Flags().StringVar(&contentMode, "content-mode", "structured", "CloudEvents HTTP content mode")
	return cmd
}

func newNotificationListSinksCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "list-sinks",
		Short: "List notification sinks",
		RunE: func(cmd *cobra.Command, args []string) error {
			sinks, err := flags.newClient().ListNotificationSinks(context.Background())
			if err != nil {
				return err
			}
			if flags.outputFormat() == OutputJSON {
				return printJSON(sinks)
			}
			rows := make([][]string, 0, len(sinks))
			for i := range sinks {
				rows = append(rows, []string{sinks[i].Name, sinks[i].Type, sinkStateLabel(sinks[i]), fmt.Sprintf("%d", sinks[i].ConsecutiveFailures), sinks[i].URL})
			}
			printTable(flags.renderer(), []string{"NAME", "TYPE", "STATE", "FAILURES", "URL"}, rows)
			return nil
		},
	}
}

func newNotificationHealthCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "health",
		Short: "Show aggregate notification sink health",
		RunE: func(cmd *cobra.Command, args []string) error {
			health, err := flags.newClient().GetNotificationSinkHealth(context.Background())
			if err != nil {
				return err
			}
			if flags.outputFormat() == OutputJSON {
				return printJSON(health)
			}
			printTable(flags.renderer(), []string{"TOTAL", "HEALTHY", "DEGRADED", "OPEN", "LAST DELIVERY"}, [][]string{{
				fmt.Sprintf("%d", health.Total),
				fmt.Sprintf("%d", health.Healthy),
				fmt.Sprintf("%d", health.Degraded),
				fmt.Sprintf("%d", health.Open),
				health.LastDeliveryAt,
			}})
			return nil
		},
	}
}

func newNotificationGetSinkCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "get-sink <name>",
		Short: "Show one notification sink",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sink, err := flags.newClient().GetNotificationSink(context.Background(), args[0])
			if err != nil {
				return err
			}
			if sink == nil {
				return fmt.Errorf("notification sink %q not found", args[0])
			}
			return printJSON(sink)
		},
	}
}

func newNotificationRemoveSinkCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "remove-sink <name>",
		Short: "Remove a notification sink",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := flags.newClient().RemoveNotificationSink(context.Background(), args[0]); err != nil {
				return err
			}
			if flags.outputFormat() == OutputJSON {
				return printJSON(map[string]string{"removed": args[0]})
			}
			flags.renderer().successLine(fmt.Sprintf("Notification sink %q removed.", args[0]))
			return nil
		},
	}
}

func newNotificationTestSinkCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "test-sink <name>",
		Short: "Send a synthetic event to a sink",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := flags.newClient().TestNotificationSink(context.Background(), args[0]); err != nil {
				return err
			}
			if flags.outputFormat() == OutputJSON {
				return printJSON(map[string]string{"tested": args[0]})
			}
			flags.renderer().successLine(fmt.Sprintf("Notification sink %q accepted a synthetic event.", args[0]))
			return nil
		},
	}
}

func buildNotificationSink(fromFile, name, sinkType, url, typeFilters, categoryFilters, severityFilters, workspaceFilters, deploymentFilters string, headers []string, maxRetries, backoffMs int, contentMode string) (orchestrator.NotificationSink, error) {
	if strings.TrimSpace(fromFile) != "" {
		return loadNotificationSink(fromFile)
	}
	headersMap, err := parseHeaders(headers)
	if err != nil {
		return orchestrator.NotificationSink{}, err
	}
	return orchestrator.NotificationSink{
		Name: strings.TrimSpace(name),
		Type: strings.TrimSpace(sinkType),
		URL:  strings.TrimSpace(url),
		Filter: orchestrator.SinkFilter{
			Types:       splitCSV(typeFilters),
			Categories:  splitCSV(categoryFilters),
			Severities:  splitCSV(severityFilters),
			Workspaces:  splitCSV(workspaceFilters),
			Deployments: splitCSV(deploymentFilters),
		},
		Headers:     headersMap,
		Retry:       orchestrator.RetryPolicy{MaxAttempts: maxRetries, BackoffMs: backoffMs},
		ContentMode: strings.TrimSpace(contentMode),
	}, nil
}

func loadNotificationSink(path string) (orchestrator.NotificationSink, error) {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(path) //nolint:gosec // path is user-provided CLI argument
	}
	if err != nil {
		return orchestrator.NotificationSink{}, err
	}
	var sink orchestrator.NotificationSink
	if err := json.Unmarshal(data, &sink); err != nil {
		return orchestrator.NotificationSink{}, fmt.Errorf("decode sink config: %w", err)
	}
	return sink, nil
}

func parseHeaders(values []string) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	headers := make(map[string]string, len(values))
	for _, value := range values {
		parts := strings.SplitN(value, "=", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
			return nil, fmt.Errorf("invalid header %q", value)
		}
		headers[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
	}
	return headers, nil
}

func splitCSV(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func sinkStateLabel(sink orchestrator.NotificationSink) string {
	if strings.TrimSpace(sink.DeliveryState) != "" {
		return sink.DeliveryState
	}
	if sink.ConsecutiveFailures > 0 {
		return orchestrator.SinkDeliveryStateDegraded
	}
	return orchestrator.SinkDeliveryStateHealthy
}
