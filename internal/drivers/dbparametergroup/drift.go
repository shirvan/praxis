package dbparametergroup

import (
	"github.com/shirvan/praxis/internal/drivers"
	"strings"
)

// FieldDiffEntry represents a single field difference between desired and observed state.
type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
}

// HasDrift compares desired spec against observed for mutable fields:
// parameters (key/value equality) and tags.
// Immutable fields (groupName, type, family, description) are NOT checked.
func HasDrift(desired DBParameterGroupSpec, observed ObservedState) bool {
	desired = applyDefaults(desired)
	if !parametersEqual(desired.Parameters, observed.Parameters) {
		return true
	}
	return !drivers.TagsMatch(desired.Tags, observed.Tags)
}

// ComputeFieldDiffs returns a structured list of differences for display.
// Immutable fields (groupName, type, family, description) are annotated "(immutable, ignored)".
// Parameter diffs show added, changed, and removed entries.
func ComputeFieldDiffs(desired DBParameterGroupSpec, observed ObservedState) []FieldDiffEntry {
	desired = applyDefaults(desired)
	var diffs []FieldDiffEntry

	if desired.GroupName != observed.GroupName && observed.GroupName != "" {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.groupName (immutable, ignored)", OldValue: observed.GroupName, NewValue: desired.GroupName})
	}
	if desired.Type != observed.Type && observed.Type != "" {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.type (immutable, ignored)", OldValue: observed.Type, NewValue: desired.Type})
	}
	if desired.Family != observed.Family && observed.Family != "" {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.family (immutable, ignored)", OldValue: observed.Family, NewValue: desired.Family})
	}
	if desired.Description != observed.Description && observed.Description != "" {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.description (immutable, ignored)", OldValue: observed.Description, NewValue: desired.Description})
	}
	for key, value := range desired.Parameters {
		if current, ok := observed.Parameters[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "spec.parameters." + key, OldValue: nil, NewValue: value})
		} else if current != value {
			diffs = append(diffs, FieldDiffEntry{Path: "spec.parameters." + key, OldValue: current, NewValue: value})
		}
	}
	for key, value := range observed.Parameters {
		if _, ok := desired.Parameters[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "spec.parameters." + key, OldValue: value, NewValue: nil})
		}
	}
	for key, value := range drivers.FilterPraxisTags(desired.Tags) {
		if current, ok := drivers.FilterPraxisTags(observed.Tags)[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: nil, NewValue: value})
		} else if current != value {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: current, NewValue: value})
		}
	}
	for key, value := range drivers.FilterPraxisTags(observed.Tags) {
		if _, ok := drivers.FilterPraxisTags(desired.Tags)[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: value, NewValue: nil})
		}
	}
	return diffs
}

// applyDefaults normalizes nil maps to empty and defaults Type to TypeDB.
func applyDefaults(spec DBParameterGroupSpec) DBParameterGroupSpec {
	if strings.TrimSpace(spec.Type) == "" {
		spec.Type = TypeDB
	}
	if spec.Parameters == nil {
		spec.Parameters = map[string]string{}
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	return spec
}

// parametersEqual returns true if both maps have identical key-value entries.
func parametersEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if other, ok := b[key]; !ok || other != value {
			return false
		}
	}
	return true
}
