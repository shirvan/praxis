package iaminstanceprofile

import (
	"github.com/shirvan/praxis/internal/drivers"
)

// HasDrift includes immutable identity fields so Provision reaches the
// convergence guard and rejects an impossible in-place change.
func HasDrift(desired IAMInstanceProfileSpec, observed ObservedState) bool {
	if desired.InstanceProfileName != observed.InstanceProfileName {
		return true
	}
	if desired.Path != observed.Path {
		return true
	}
	if desired.RoleName != observed.RoleName {
		return true
	}
	return !drivers.TagsMatch(desired.Tags, observed.Tags)
}

// ComputeFieldDiffs returns immutable identity differences plus actionable role
// and user-managed tag differences.
func ComputeFieldDiffs(desired IAMInstanceProfileSpec, observed ObservedState) []drivers.FieldDiff {
	var diffs []drivers.FieldDiff
	if desired.InstanceProfileName != observed.InstanceProfileName {
		diffs = append(diffs, drivers.FieldDiff{
			Path:     "spec.instanceProfileName (immutable, requires replacement)",
			OldValue: observed.InstanceProfileName,
			NewValue: desired.InstanceProfileName,
		})
	}

	if desired.Path != observed.Path {
		diffs = append(diffs, drivers.FieldDiff{
			Path:     "spec.path (immutable, requires replacement)",
			OldValue: observed.Path,
			NewValue: desired.Path,
		})
	}

	if desired.RoleName != observed.RoleName {
		diffs = append(diffs, drivers.FieldDiff{
			Path:     "spec.roleName",
			OldValue: observed.RoleName,
			NewValue: desired.RoleName,
		})
	}

	diffs = append(diffs, computeTagDiffs(desired.Tags, observed.Tags)...)
	return diffs
}

func computeTagDiffs(desired, observed map[string]string) []drivers.FieldDiff {
	var diffs []drivers.FieldDiff
	desiredFiltered := drivers.FilterPraxisTags(desired)
	observedFiltered := drivers.FilterPraxisTags(observed)
	for key, value := range desiredFiltered {
		if observedValue, ok := observedFiltered[key]; !ok {
			diffs = append(diffs, drivers.FieldDiff{Path: "tags." + key, OldValue: nil, NewValue: value})
		} else if observedValue != value {
			diffs = append(diffs, drivers.FieldDiff{Path: "tags." + key, OldValue: observedValue, NewValue: value})
		}
	}
	for key, value := range observedFiltered {
		if _, ok := desiredFiltered[key]; !ok {
			diffs = append(diffs, drivers.FieldDiff{Path: "tags." + key, OldValue: value, NewValue: nil})
		}
	}
	return diffs
}
