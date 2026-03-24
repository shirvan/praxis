package provider

import (
	"fmt"
	"maps"
	"strings"

	restate "github.com/restatedev/sdk-go"
)

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

func cloneLookupTags(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(input))
	maps.Copy(cloned, input)
	return cloned
}

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
