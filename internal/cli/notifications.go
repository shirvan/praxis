// notifications.go contains shared helpers for notification sink operations.
//
// Sink commands are now accessed through top-level verbs:
//   - `praxis create sink`          (create.go)
//   - `praxis get sink/<name>`      (get.go)
//   - `praxis list sinks`           (list.go)
//   - `praxis delete sink/<name>`   (delete.go)
//   - `praxis test sink/<name>`     (test_cmd.go)
//   - `praxis get notifications`    (get.go)
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/shirvan/praxis/internal/core/orchestrator"
)

// buildNotificationSink assembles a NotificationSink from either a JSON file
// (--from-file) or individual flag values. File loading takes precedence
// when --from-file is set.
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

// loadNotificationSink reads and deserialises a NotificationSink from a JSON
// file. Pass "-" to read from stdin.
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

// parseHeaders converts "key=value" header strings into a map.
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

// splitCSV splits a comma-separated string into a trimmed slice. Returns nil
// for empty input.
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

// sinkStateLabel derives a human-readable delivery-state label for a sink.
// Falls back to the DeliveryState field, then heuristic based on failure count.
func sinkStateLabel(sink orchestrator.NotificationSink) string {
	if strings.TrimSpace(sink.DeliveryState) != "" {
		return sink.DeliveryState
	}
	if sink.ConsecutiveFailures > 0 {
		return orchestrator.SinkDeliveryStateDegraded
	}
	return orchestrator.SinkDeliveryStateHealthy
}
