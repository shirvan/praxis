package dbparametergroup

import (
	"strings"

	"github.com/shirvan/praxis/internal/drivers"
)

// HasDrift compares identity and mutable fields. Identity differences are
// surfaced so Converge can return the approved replacement-required conflict.
func HasDrift(desired DBParameterGroupSpec, observed ObservedState) bool {
	desired = applyDefaults(desired)
	if (observed.GroupName != "" && desired.GroupName != observed.GroupName) ||
		(observed.Type != "" && desired.Type != observed.Type) ||
		(observed.Family != "" && desired.Family != observed.Family) ||
		(observed.Description != "" && desired.Description != observed.Description) {
		return true
	}
	if !parametersEqual(desired.Parameters, observed.Parameters) {
		return true
	}
	return !drivers.TagsMatch(desired.Tags, observed.Tags)
}

// ComputeFieldDiffs returns a structured list of differences for display.
// Immutable fields (groupName, type, family, description) are annotated as requiring replacement.
// Parameter diffs show added, changed, and removed entries.
func ComputeFieldDiffs(desired DBParameterGroupSpec, observed ObservedState) []drivers.FieldDiff {
	desired = applyDefaults(desired)
	var diffs []drivers.FieldDiff

	if desired.GroupName != observed.GroupName && observed.GroupName != "" {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.groupName (immutable, requires replacement)", OldValue: observed.GroupName, NewValue: desired.GroupName})
	}
	if desired.Type != observed.Type && observed.Type != "" {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.type (immutable, requires replacement)", OldValue: observed.Type, NewValue: desired.Type})
	}
	if desired.Family != observed.Family && observed.Family != "" {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.family (immutable, requires replacement)", OldValue: observed.Family, NewValue: desired.Family})
	}
	if desired.Description != observed.Description && observed.Description != "" {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.description (immutable, requires replacement)", OldValue: observed.Description, NewValue: desired.Description})
	}
	for key, value := range desired.Parameters {
		if current, ok := observed.Parameters[key]; !ok {
			diffs = append(diffs, drivers.FieldDiff{Path: "spec.parameters." + key, OldValue: nil, NewValue: value})
		} else if current != value {
			diffs = append(diffs, drivers.FieldDiff{Path: "spec.parameters." + key, OldValue: current, NewValue: value})
		}
	}
	for key, value := range observed.Parameters {
		if _, ok := desired.Parameters[key]; !ok {
			diffs = append(diffs, drivers.FieldDiff{Path: "spec.parameters." + key, OldValue: value, NewValue: nil})
		}
	}
	for key, value := range drivers.FilterPraxisTags(desired.Tags) {
		if current, ok := drivers.FilterPraxisTags(observed.Tags)[key]; !ok {
			diffs = append(diffs, drivers.FieldDiff{Path: "tags." + key, OldValue: nil, NewValue: value})
		} else if current != value {
			diffs = append(diffs, drivers.FieldDiff{Path: "tags." + key, OldValue: current, NewValue: value})
		}
	}
	for key, value := range drivers.FilterPraxisTags(observed.Tags) {
		if _, ok := drivers.FilterPraxisTags(desired.Tags)[key]; !ok {
			diffs = append(diffs, drivers.FieldDiff{Path: "tags." + key, OldValue: value, NewValue: nil})
		}
	}
	return diffs
}

// applyDefaults normalizes nil maps to empty and defaults Type to TypeDB.
func applyDefaults(spec DBParameterGroupSpec) DBParameterGroupSpec {
	if strings.TrimSpace(spec.Type) == "" {
		spec.Type = TypeDB
	}
	if strings.TrimSpace(spec.Description) == "" {
		spec.Description = spec.GroupName
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
