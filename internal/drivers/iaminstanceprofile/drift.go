package iaminstanceprofile

import (
	"github.com/shirvan/praxis/internal/drivers"
)

// HasDrift returns true if the role name or user-managed tags differ between
// desired and observed state. Path is immutable and excluded from drift checks.
func HasDrift(desired IAMInstanceProfileSpec, observed ObservedState) bool {
	if desired.RoleName != observed.RoleName {
		return true
	}
	return !drivers.TagsMatch(desired.Tags, observed.Tags)
}

// ComputeFieldDiffs returns a per-field list of differences between desired and observed state.
// Reports path as an informational immutable diff; checks roleName and tags for actionable drift.
func ComputeFieldDiffs(desired IAMInstanceProfileSpec, observed ObservedState) []FieldDiffEntry {
	var diffs []FieldDiffEntry

	if desired.Path != "" && observed.Path != "" && desired.Path != observed.Path {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.path (immutable, ignored)",
			OldValue: observed.Path,
			NewValue: desired.Path,
		})
	}

	if desired.RoleName != observed.RoleName {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.roleName",
			OldValue: observed.RoleName,
			NewValue: desired.RoleName,
		})
	}

	diffs = append(diffs, computeTagDiffs(desired.Tags, observed.Tags)...)
	return diffs
}

// FieldDiffEntry describes a single field-level difference between desired and observed state.
type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
}

func computeTagDiffs(desired, observed map[string]string) []FieldDiffEntry {
	var diffs []FieldDiffEntry
	desiredFiltered := drivers.FilterPraxisTags(desired)
	observedFiltered := drivers.FilterPraxisTags(observed)
	for key, value := range desiredFiltered {
		if observedValue, ok := observedFiltered[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: nil, NewValue: value})
		} else if observedValue != value {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: observedValue, NewValue: value})
		}
	}
	for key, value := range observedFiltered {
		if _, ok := desiredFiltered[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: value, NewValue: nil})
		}
	}
	return diffs
}
