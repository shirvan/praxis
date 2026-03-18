package ec2

import (
	"sort"
	"strings"
)

// HasDrift returns true when the mutable EC2 instance fields differ.
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

// ComputeFieldDiffs returns field-level differences for plan output.
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

type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
}

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

func sortedCopy(values []string) []string {
	copyOf := make([]string, len(values))
	copy(copyOf, values)
	sort.Strings(copyOf)
	return copyOf
}

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
