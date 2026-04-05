// events.go contains shared helpers for event operations.
//
// Event commands are now accessed through top-level verbs:
//   - `praxis list events`    (list.go)
//   - `praxis get events`     (get.go — per-deployment event list is via list)
package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/shirvan/praxis/internal/core/orchestrator"
)

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
