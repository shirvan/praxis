package provider

import (
	"maps"
	"strings"
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

func isLookupNotFound(err error, serviceNotFound func(error) bool) bool {
	if err == nil {
		return false
	}
	if serviceNotFound != nil && serviceNotFound(err) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "not found")
}

func nativeLookupIdentity(filter LookupFilter) string {
	if id := strings.TrimSpace(filter.ID); id != "" {
		return id
	}
	return strings.TrimSpace(filter.Name)
}

func matchesNativeLookupFilter(identity string, tags map[string]string, filter LookupFilter) bool {
	if id := strings.TrimSpace(filter.ID); id != "" && identity != id {
		return false
	}
	if name := strings.TrimSpace(filter.Name); name != "" && identity != name {
		return false
	}
	for key, value := range filter.Tag {
		if tags[key] != value {
			return false
		}
	}
	return true
}

func matchesLookupTags(tags map[string]string, filter LookupFilter) bool {
	if name := strings.TrimSpace(filter.Name); name != "" && tags["Name"] != name {
		return false
	}
	for key, value := range filter.Tag {
		if tags[key] != value {
			return false
		}
	}
	return true
}
