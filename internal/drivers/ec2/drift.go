// Package ec2 — drift.go contains drift detection logic for EC2 instances.
//
// Drift detection compares the user's desired spec against the live AWS observed state.
// Only mutable fields are compared for actionable drift. Immutable fields (imageId,
// subnetId, keyName) are surfaced as replacement-required diffs in ComputeFieldDiffs
// but never trigger in-place corrective action.

package ec2

import (
	"sort"

	"github.com/shirvan/praxis/internal/drivers"
)

// HasDrift returns true when any mutable EC2 instance field differs between desired and observed.
//
// Drift is only evaluated when the instance is in a stable state (running or stopped).
// Instances in transitional states (pending, shutting-down, terminated) are not considered
// drifted because their configuration cannot be reliably read or changed.
//
// Mutable fields checked:
//   - InstanceType:     requires stop/modify/start to correct
//   - SecurityGroupIds: hot-swappable via ModifyInstanceAttribute
//   - IamInstanceProfile: attachable, replaceable, and removable without replacement
//   - Monitoring:       toggleable via MonitorInstances/UnmonitorInstances
//   - Tags:             user tags (excluding praxis:-prefixed internal tags)
func HasDrift(desired EC2InstanceSpec, observed ObservedState) bool {
	if observed.State != "running" && observed.State != "stopped" {
		return false
	}

	if desired.InstanceType != observed.InstanceType {
		return true
	}
	// An empty desired list means "use the default SG" — the correction paths
	// skip it, so flagging it as drift would loop forever without correction.
	if len(desired.SecurityGroupIds) > 0 && !securityGroupsMatch(desired.SecurityGroupIds, observed.SecurityGroupIds) {
		return true
	}
	if !instanceProfilesMatch(desired.IamInstanceProfile, observed.IamInstanceProfile) {
		return true
	}
	if desired.Monitoring != observed.Monitoring {
		return true
	}
	if !drivers.TagsMatch(desired.Tags, observed.Tags) {
		return true
	}

	return false
}

// ComputeFieldDiffs returns a list of field-level differences suitable for plan output.
// Each entry identifies the JSON path, old (observed) value, and new (desired) value.
// Immutable fields are included with an "(immutable, requires replacement)" suffix
// and are never corrected in place by the driver.
func ComputeFieldDiffs(desired EC2InstanceSpec, observed ObservedState) []drivers.FieldDiff {
	var diffs []drivers.FieldDiff

	if desired.InstanceType != observed.InstanceType {
		diffs = append(diffs, drivers.FieldDiff{
			Path:     "spec.instanceType",
			OldValue: observed.InstanceType,
			NewValue: desired.InstanceType,
		})
	}

	if !securityGroupsMatch(desired.SecurityGroupIds, observed.SecurityGroupIds) {
		diffs = append(diffs, drivers.FieldDiff{
			Path:     "spec.securityGroupIds",
			OldValue: observed.SecurityGroupIds,
			NewValue: desired.SecurityGroupIds,
		})
	}

	if !instanceProfilesMatch(desired.IamInstanceProfile, observed.IamInstanceProfile) {
		diffs = append(diffs, drivers.FieldDiff{
			Path:     "spec.iamInstanceProfile",
			OldValue: observed.IamInstanceProfile,
			NewValue: desired.IamInstanceProfile,
		})
	}

	if desired.Monitoring != observed.Monitoring {
		diffs = append(diffs, drivers.FieldDiff{
			Path:     "spec.monitoring",
			OldValue: observed.Monitoring,
			NewValue: desired.Monitoring,
		})
	}

	desiredFiltered := drivers.FilterPraxisTags(desired.Tags)
	observedFiltered := drivers.FilterPraxisTags(observed.Tags)
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

	if desired.ImageId != observed.ImageId && observed.ImageId != "" {
		diffs = append(diffs, drivers.FieldDiff{
			Path:     "spec.imageId (immutable, requires replacement)",
			OldValue: observed.ImageId,
			NewValue: desired.ImageId,
		})
	}
	if desired.SubnetId != observed.SubnetId && observed.SubnetId != "" {
		diffs = append(diffs, drivers.FieldDiff{
			Path:     "spec.subnetId (immutable, requires replacement)",
			OldValue: observed.SubnetId,
			NewValue: desired.SubnetId,
		})
	}
	if desired.KeyName != observed.KeyName && observed.KeyName != "" {
		diffs = append(diffs, drivers.FieldDiff{
			Path:     "spec.keyName (immutable, requires replacement)",
			OldValue: observed.KeyName,
			NewValue: desired.KeyName,
		})
	}

	return diffs
}

// securityGroupsMatch compares two security group ID slices, ignoring order.
// Both slices are sorted before element-wise comparison.
func securityGroupsMatch(desired, observed []string) bool {
	a := sortedCopy(desired)
	b := sortedCopy(observed)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func instanceProfilesMatch(desired, observed string) bool {
	return instanceProfileName(desired) == instanceProfileName(observed)
}

// sortedCopy returns a sorted copy of the input slice without modifying the original.
func sortedCopy(values []string) []string {
	copyOf := make([]string, len(values))
	copy(copyOf, values)
	sort.Strings(copyOf)
	return copyOf
}
