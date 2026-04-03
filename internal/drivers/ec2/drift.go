// Package ec2 — drift.go contains drift detection logic for EC2 instances.
//
// Drift detection compares the user's desired spec against the live AWS observed state.
// Only mutable fields are compared for actionable drift. Immutable fields (imageId,
// subnetId, keyName) are surfaced as informational-only diffs in ComputeFieldDiffs
// but never trigger corrective action.

package ec2

import (
	"sort"
	"strings"
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
//   - Monitoring:       toggleable via MonitorInstances/UnmonitorInstances
//   - Tags:             user tags (excluding praxis:-prefixed internal tags)
func HasDrift(desired EC2InstanceSpec, observed ObservedState) bool {
	if observed.State != "running" && observed.State != "stopped" {
		return false
	}

	if desired.InstanceType != observed.InstanceType {
		return true
	}
	if !securityGroupsMatch(desired.SecurityGroupIds, observed.SecurityGroupIds) {
		return true
	}
	if desired.Monitoring != observed.Monitoring {
		return true
	}
	if !tagsMatch(desired.Tags, observed.Tags) {
		return true
	}

	return false
}

// ComputeFieldDiffs returns a list of field-level differences suitable for plan output.
// Each entry identifies the JSON path, old (observed) value, and new (desired) value.
// Immutable fields are included with an "(immutable, ignored)" suffix for user awareness
// but are never corrected by the driver.
func ComputeFieldDiffs(desired EC2InstanceSpec, observed ObservedState) []FieldDiffEntry {
	var diffs []FieldDiffEntry

	if desired.InstanceType != observed.InstanceType {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.instanceType",
			OldValue: observed.InstanceType,
			NewValue: desired.InstanceType,
		})
	}

	if !securityGroupsMatch(desired.SecurityGroupIds, observed.SecurityGroupIds) {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.securityGroupIds",
			OldValue: observed.SecurityGroupIds,
			NewValue: desired.SecurityGroupIds,
		})
	}

	if desired.Monitoring != observed.Monitoring {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.monitoring",
			OldValue: observed.Monitoring,
			NewValue: desired.Monitoring,
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

	if desired.ImageId != observed.ImageId && observed.ImageId != "" {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.imageId (immutable, ignored)",
			OldValue: observed.ImageId,
			NewValue: desired.ImageId,
		})
	}
	if desired.SubnetId != observed.SubnetId && observed.SubnetId != "" {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.subnetId (immutable, ignored)",
			OldValue: observed.SubnetId,
			NewValue: desired.SubnetId,
		})
	}
	if desired.KeyName != observed.KeyName && observed.KeyName != "" {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.keyName (immutable, ignored)",
			OldValue: observed.KeyName,
			NewValue: desired.KeyName,
		})
	}

	return diffs
}

// FieldDiffEntry represents a single field-level difference between desired and observed state.
// Used by the CLI/UI to display human-readable drift reports and plan previews.
type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
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

// sortedCopy returns a sorted copy of the input slice without modifying the original.
func sortedCopy(values []string) []string {
	copyOf := make([]string, len(values))
	copy(copyOf, values)
	sort.Strings(copyOf)
	return copyOf
}

// tagsMatch compares user tags between desired and observed, excluding praxis:-prefixed tags.
// praxis: tags are internal system tags and are not part of the user-facing drift model.
func tagsMatch(a, b map[string]string) bool {
	fa := filterPraxisTags(a)
	fb := filterPraxisTags(b)
	if len(fa) != len(fb) {
		return false
	}
	for key, value := range fa {
		if other, ok := fb[key]; !ok || other != value {
			return false
		}
	}
	return true
}

// filterPraxisTags returns a copy of the tag map with all praxis:-prefixed keys removed.
// Used to isolate user tags for drift comparison.
func filterPraxisTags(m map[string]string) map[string]string {
	if len(m) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(m))
	for key, value := range m {
		if !strings.HasPrefix(key, "praxis:") {
			out[key] = value
		}
	}
	return out
}
