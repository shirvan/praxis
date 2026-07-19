package dbsubnetgroup

import (
	"sort"
	"strings"

	"github.com/shirvan/praxis/internal/drivers"
)

// HasDrift compares identity and mutable fields. Identity differences are
// surfaced so Converge can return the approved replacement-required conflict.
func HasDrift(desired DBSubnetGroupSpec, observed ObservedState) bool {
	desired = applyDefaults(desired)
	if observed.GroupName != "" && desired.GroupName != observed.GroupName {
		return true
	}
	if desired.Description != observed.Description {
		return true
	}
	if !stringSliceEqual(desired.SubnetIds, observed.SubnetIds) {
		return true
	}
	return !drivers.TagsMatch(desired.Tags, observed.Tags)
}

// ComputeFieldDiffs returns a structured list of differences for display.
// GroupName is annotated as requiring replacement when different.
func ComputeFieldDiffs(desired DBSubnetGroupSpec, observed ObservedState) []drivers.FieldDiff {
	desired = applyDefaults(desired)
	var diffs []drivers.FieldDiff

	if desired.GroupName != observed.GroupName && observed.GroupName != "" {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.groupName (immutable, requires replacement)", OldValue: observed.GroupName, NewValue: desired.GroupName})
	}
	if desired.Description != observed.Description {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.description", OldValue: observed.Description, NewValue: desired.Description})
	}
	if !stringSliceEqual(desired.SubnetIds, observed.SubnetIds) {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.subnetIds", OldValue: normalizeStrings(observed.SubnetIds), NewValue: normalizeStrings(desired.SubnetIds)})
	}
	for key, value := range drivers.FilterPraxisTags(desired.Tags) {
		if observedValue, ok := drivers.FilterPraxisTags(observed.Tags)[key]; !ok {
			diffs = append(diffs, drivers.FieldDiff{Path: "tags." + key, OldValue: nil, NewValue: value})
		} else if observedValue != value {
			diffs = append(diffs, drivers.FieldDiff{Path: "tags." + key, OldValue: observedValue, NewValue: value})
		}
	}
	for key, value := range drivers.FilterPraxisTags(observed.Tags) {
		if _, ok := drivers.FilterPraxisTags(desired.Tags)[key]; !ok {
			diffs = append(diffs, drivers.FieldDiff{Path: "tags." + key, OldValue: value, NewValue: nil})
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
