package dbsubnetgroup

import (
	"sort"
	"strings"
)

// FieldDiffEntry represents a single field difference between desired and observed state.
type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
}

// HasDrift compares desired spec against observed for mutable fields:
// description, subnetIds, and tags. GroupName is immutable and not checked.
func HasDrift(desired DBSubnetGroupSpec, observed ObservedState) bool {
	desired = applyDefaults(desired)
	if desired.Description != observed.Description {
		return true
	}
	if !stringSliceEqual(desired.SubnetIds, observed.SubnetIds) {
		return true
	}
	return !tagsMatch(desired.Tags, observed.Tags)
}

// ComputeFieldDiffs returns a structured list of differences for display.
// GroupName is annotated "(immutable, ignored)" when different.
func ComputeFieldDiffs(desired DBSubnetGroupSpec, observed ObservedState) []FieldDiffEntry {
	desired = applyDefaults(desired)
	var diffs []FieldDiffEntry

	if desired.GroupName != observed.GroupName && observed.GroupName != "" {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.groupName (immutable, ignored)", OldValue: observed.GroupName, NewValue: desired.GroupName})
	}
	if desired.Description != observed.Description {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.description", OldValue: observed.Description, NewValue: desired.Description})
	}
	if !stringSliceEqual(desired.SubnetIds, observed.SubnetIds) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.subnetIds", OldValue: normalizeStrings(observed.SubnetIds), NewValue: normalizeStrings(desired.SubnetIds)})
	}
	for key, value := range filterPraxisTags(desired.Tags) {
		if observedValue, ok := filterPraxisTags(observed.Tags)[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: nil, NewValue: value})
		} else if observedValue != value {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: observedValue, NewValue: value})
		}
	}
	for key, value := range filterPraxisTags(observed.Tags) {
		if _, ok := filterPraxisTags(desired.Tags)[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: value, NewValue: nil})
		}
	}

	return diffs
}

// applyDefaults normalizes nil tags to empty map and sorts subnet IDs.
func applyDefaults(spec DBSubnetGroupSpec) DBSubnetGroupSpec {
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	spec.SubnetIds = normalizeStrings(spec.SubnetIds)
	return spec
}

// tagsMatch compares two tag maps after filtering praxis: internal tags.
func tagsMatch(a, b map[string]string) bool {
	fa := filterPraxisTags(a)
	fb := filterPraxisTags(b)
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

// filterPraxisTags removes praxis:-prefixed tags used for internal bookkeeping.
func filterPraxisTags(tags map[string]string) map[string]string {
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

// stringSliceEqual normalizes both slices then compares element-by-element.
func stringSliceEqual(a, b []string) bool {
	na := normalizeStrings(a)
	nb := normalizeStrings(b)
	if len(na) != len(nb) {
		return false
	}
	for index := range na {
		if na[index] != nb[index] {
			return false
		}
	}
	return true
}

// normalizeStrings trims whitespace, removes empties, and sorts for deterministic comparison.
func normalizeStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	sort.Strings(out)
	return out
}
