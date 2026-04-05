package vpc

import (
	"github.com/shirvan/praxis/internal/drivers"
)

// HasDrift returns true if the desired spec and observed state differ.
//
// VPC-specific drift rules:
// - cidrBlock is NOT checked — immutable after creation.
// - instanceTenancy is NOT checked — immutable after creation.
//
// Fields that ARE checked (and can be corrected in-place):
// - enableDnsHostnames
// - enableDnsSupport
// - tags
func HasDrift(desired VPCSpec, observed ObservedState) bool {
	if observed.State != "available" {
		return false
	}

	if desired.EnableDnsHostnames != observed.EnableDnsHostnames {
		return true
	}

	if desired.EnableDnsSupport != observed.EnableDnsSupport {
		return true
	}

	if !drivers.TagsMatch(desired.Tags, observed.Tags) {
		return true
	}

	return false
}

// ComputeFieldDiffs returns field-level differences for plan output.
func ComputeFieldDiffs(desired VPCSpec, observed ObservedState) []FieldDiffEntry {
	var diffs []FieldDiffEntry

	// --- Mutable fields ---

	if desired.EnableDnsHostnames != observed.EnableDnsHostnames {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.enableDnsHostnames",
			OldValue: observed.EnableDnsHostnames,
			NewValue: desired.EnableDnsHostnames,
		})
	}

	if desired.EnableDnsSupport != observed.EnableDnsSupport {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.enableDnsSupport",
			OldValue: observed.EnableDnsSupport,
			NewValue: desired.EnableDnsSupport,
		})
	}

	desiredFiltered := drivers.FilterPraxisTags(desired.Tags)
	observedFiltered := drivers.FilterPraxisTags(observed.Tags)
	for k, v := range desiredFiltered {
		if ov, ok := observedFiltered[k]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + k, OldValue: nil, NewValue: v})
		} else if ov != v {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + k, OldValue: ov, NewValue: v})
		}
	}
	for k, v := range observedFiltered {
		if _, ok := desiredFiltered[k]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + k, OldValue: v, NewValue: nil})
		}
	}

	// --- Immutable fields (reported for visibility, not corrected) ---

	if desired.CidrBlock != observed.CidrBlock && observed.CidrBlock != "" {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.cidrBlock (immutable, requires replacement)",
			OldValue: observed.CidrBlock,
			NewValue: desired.CidrBlock,
		})
	}

	desiredTenancy := desired.InstanceTenancy
	if desiredTenancy == "" {
		desiredTenancy = "default"
	}
	if desiredTenancy != observed.InstanceTenancy && observed.InstanceTenancy != "" {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.instanceTenancy (immutable, ignored)",
			OldValue: observed.InstanceTenancy,
			NewValue: desiredTenancy,
		})
	}

	return diffs
}

// FieldDiffEntry is the driver-specific diff unit.
type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
}
