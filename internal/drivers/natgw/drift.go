package natgw

import (
	"github.com/shirvan/praxis/internal/drivers"
)

// HasDrift returns true if the desired spec and observed state differ.
//
// NAT Gateway drift rules:
//   - Only checked when the NAT GW is in "available" state.
//   - ONLY tags are checked — everything else (SubnetId, ConnectivityType,
//     AllocationId) is immutable after creation.
//   - applyDefaults is called first to fill in the default ConnectivityType.
func HasDrift(desired NATGatewaySpec, observed ObservedState) bool {
	desired = applyDefaults(desired)
	if observed.State != "available" {
		return false
	}
	return !drivers.TagsMatch(desired.Tags, observed.Tags)
}

// ComputeFieldDiffs returns a human-readable list of differences for drift
// event reporting. Reports immutable fields (SubnetId, ConnectivityType,
// AllocationId) in addition to tags for operator visibility.
func ComputeFieldDiffs(desired NATGatewaySpec, observed ObservedState) []FieldDiffEntry {
	desired = applyDefaults(desired)
	var diffs []FieldDiffEntry

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

	if desired.SubnetId != observed.SubnetId && observed.SubnetId != "" {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.subnetId (immutable, requires replacement)",
			OldValue: observed.SubnetId,
			NewValue: desired.SubnetId,
		})
	}

	if desired.ConnectivityType != observed.ConnectivityType && observed.ConnectivityType != "" {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.connectivityType (immutable, requires replacement)",
			OldValue: observed.ConnectivityType,
			NewValue: desired.ConnectivityType,
		})
	}

	if desired.AllocationId != observed.AllocationId && observed.AllocationId != "" {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.allocationId (immutable, requires replacement)",
			OldValue: observed.AllocationId,
			NewValue: desired.AllocationId,
		})
	}

	if desired.AllocationId != observed.AllocationId && observed.AllocationId == "" && desired.ConnectivityType == "public" && desired.AllocationId != "" {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.allocationId (immutable, requires replacement)",
			OldValue: nil,
			NewValue: desired.AllocationId,
		})
	}

	return diffs
}

type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
}
