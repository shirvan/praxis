package subnet

import "strings"

// HasDrift returns true if the desired spec and observed state differ.
//
// Subnet-specific drift rules:
//   - Only checked when the subnet is in "available" state.
//   - CidrBlock, AvailabilityZone, and VpcId are NOT checked — they are
//     immutable after creation and cannot be corrected.
//   - MapPublicIpOnLaunch is checked — it can be modified in-place via
//     EC2 ModifySubnetAttribute.
//   - Tags are compared (excluding praxis:-prefixed tags).
func HasDrift(desired SubnetSpec, observed ObservedState) bool {
	if observed.State != "available" {
		return false
	}

	if desired.MapPublicIpOnLaunch != observed.MapPublicIpOnLaunch {
		return true
	}

	if !tagsMatch(desired.Tags, observed.Tags) {
		return true
	}

	return false
}

// ComputeFieldDiffs returns a human-readable list of differences for drift
// event reporting. Reports both mutable fields (MapPublicIpOnLaunch, tags)
// and immutable fields (CidrBlock, AvailabilityZone, VpcId) for visibility,
// even though immutable fields cannot be corrected.
func ComputeFieldDiffs(desired SubnetSpec, observed ObservedState) []FieldDiffEntry {
	var diffs []FieldDiffEntry

	if desired.MapPublicIpOnLaunch != observed.MapPublicIpOnLaunch {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.mapPublicIpOnLaunch",
			OldValue: observed.MapPublicIpOnLaunch,
			NewValue: desired.MapPublicIpOnLaunch,
		})
	}

	desiredFiltered := filterPraxisTags(desired.Tags)
	observedFiltered := filterPraxisTags(observed.Tags)
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

	if desired.CidrBlock != observed.CidrBlock && observed.CidrBlock != "" {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.cidrBlock (immutable, requires replacement)",
			OldValue: observed.CidrBlock,
			NewValue: desired.CidrBlock,
		})
	}

	if desired.AvailabilityZone != observed.AvailabilityZone && observed.AvailabilityZone != "" {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.availabilityZone (immutable, requires replacement)",
			OldValue: observed.AvailabilityZone,
			NewValue: desired.AvailabilityZone,
		})
	}

	if desired.VpcId != observed.VpcId && observed.VpcId != "" {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.vpcId (immutable, requires replacement)",
			OldValue: observed.VpcId,
			NewValue: desired.VpcId,
		})
	}

	return diffs
}

type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
}

func tagsMatch(a, b map[string]string) bool {
	fa := filterPraxisTags(a)
	fb := filterPraxisTags(b)
	if len(fa) != len(fb) {
		return false
	}
	for key, value := range fa {
		if observedValue, ok := fb[key]; !ok || observedValue != value {
			return false
		}
	}
	return true
}

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
