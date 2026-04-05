package igw

import (
	"github.com/shirvan/praxis/internal/drivers"
)

// HasDrift returns true if the desired spec and observed state differ.
//
// IGW-specific drift rules:
// - VpcId attachment is checked — the IGW must be attached to the correct VPC.
// - Tags are compared (excluding praxis:-prefixed tags).
//
// Unlike VPCs or subnets there are no immutable fields to skip; the VPC
// attachment can be changed by detaching and re-attaching.
func HasDrift(desired IGWSpec, observed ObservedState) bool {
	if desired.VpcId != observed.AttachedVpcId {
		return true
	}
	if !drivers.TagsMatch(desired.Tags, observed.Tags) {
		return true
	}
	return false
}

// ComputeFieldDiffs returns a human-readable list of differences between
// the desired spec and observed state, used for drift event reporting.
func ComputeFieldDiffs(desired IGWSpec, observed ObservedState) []FieldDiffEntry {
	var diffs []FieldDiffEntry

	if desired.VpcId != observed.AttachedVpcId {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.vpcId",
			OldValue: observed.AttachedVpcId,
			NewValue: desired.VpcId,
		})
	}

	desiredFiltered := drivers.FilterPraxisTags(desired.Tags)
	observedFiltered := drivers.FilterPraxisTags(observed.Tags)
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

// FieldDiffEntry represents a single field difference between desired and observed state.
type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
}
