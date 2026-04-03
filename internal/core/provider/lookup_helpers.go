package provider

import (
	"fmt"
	"maps"
	"strings"

	restate "github.com/restatedev/sdk-go"
)

// lookupTags builds the tag filter map for data source lookups.
// If the filter includes a Name, it is injected as the "Name" tag because
// most AWS resource types store their display name in a tag rather than a
// dedicated API field. The original Tag map is cloned to avoid mutation.
func lookupTags(filter LookupFilter) map[string]string {
	tags := cloneLookupTags(filter.Tag)
	if strings.TrimSpace(filter.Name) != "" {
		if tags == nil {
			tags = make(map[string]string, 1)
		}
		tags["Name"] = strings.TrimSpace(filter.Name)
	}
	return tags
}

// cloneLookupTags creates a defensive copy of the tag map from a LookupFilter.
// Returns nil if the input is empty, allowing callers to distinguish "no tags"
// from "empty map" without extra nil checks.
func cloneLookupTags(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(input))
	maps.Copy(cloned, input)
	return cloned
}

// classifyLookupError converts a driver-level lookup error into a Restate
// TerminalError with an appropriate HTTP status code. Terminal errors stop
// Restate's automatic retry loop because lookup failures are deterministic:
//   - 404: resource not found (custom notFound predicate or message heuristic)
//   - 409: ambiguous match (multiple results for a filter)
//   - 500: unexpected failure
//
// The notFound callback allows each driver to supply service-specific detection
// logic (e.g. checking for a typed AWS "NotFoundException").
func classifyLookupError(err error, notFound func(error) bool) error {
	if err == nil {
		return nil
	}
	message := strings.ToLower(err.Error())
	if notFound != nil && notFound(err) {
		return restate.TerminalError(err, 404)
	}
	if strings.Contains(message, "not found") {
		return restate.TerminalError(err, 404)
	}
	if strings.Contains(message, "ambiguous") || strings.Contains(message, "multiple") {
		return restate.TerminalError(err, 409)
	}
	return restate.TerminalError(fmt.Errorf("data source lookup failed: %w", err), 500)
}
