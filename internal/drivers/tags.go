package drivers

import "strings"

// FilterPraxisTags returns a copy of the tag map with all praxis:-prefixed
// keys removed. Drivers use this to compare user-managed tags while ignoring
// internal bookkeeping tags injected by the orchestrator.
func FilterPraxisTags(tags map[string]string) map[string]string {
	if len(tags) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(tags))
	for key, value := range tags {
		if !strings.HasPrefix(key, "praxis:") {
			out[key] = value
		}
	}
	return out
}

// TagsMatch compares two tag maps for equality after filtering out
// praxis:-prefixed internal tags.
func TagsMatch(a, b map[string]string) bool {
	fa := FilterPraxisTags(a)
	fb := FilterPraxisTags(b)
	if len(fa) != len(fb) {
		return false
	}
	for key, value := range fa {
		if other, ok := fb[key]; !ok || other != value {
			return false
		}
	}
	return true
}
