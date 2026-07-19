// Package alb – drift.go
//
// This file implements drift detection for AWS Application Load Balancer (ALB).
// HasDrift compares the desired spec against the observed state from AWS and
// returns true when any mutable field has diverged. ComputeFieldDiffs produces
// a structured list of individual field changes for plan output and logging.
// Immutable fields (those that require resource replacement) are annotated.
package alb

import (
	"sort"

	"github.com/shirvan/praxis/internal/drivers"
)

// HasDrift compares the desired ALB spec against the observed
// state from AWS and returns true if any mutable field has diverged.
// It is called during Reconcile to decide whether drift correction is needed.
func HasDrift(desired ALBSpec, observed ObservedState) bool {
	desired = applyDefaults(desired)
	if desired.Name != observed.Name || desired.Scheme != observed.Scheme {
		return true
	}
	if desired.IpAddressType != observed.IpAddressType {
		return true
	}
	if !sortedStringsEqual(resolveSubnets(desired), observed.Subnets) {
		return true
	}
	if !sortedStringsEqual(sortedCopy(desired.SecurityGroups), observed.SecurityGroups) {
		return true
	}
	if !accessLogsEqual(desired.AccessLogs, observed.AccessLogs) {
		return true
	}
	if desired.DeletionProtection != observed.DeletionProtection {
		return true
	}
	if desired.IdleTimeout != observed.IdleTimeout {
		return true
	}
	return !drivers.TagsMatch(desired.Tags, observed.Tags)
}

// ComputeFieldDiffs produces a structured list of individual field changes
// between the desired spec and observed state. Used for plan output, CLI
// display, and audit logging. Immutable field changes are clearly annotated.
func ComputeFieldDiffs(desired ALBSpec, observed ObservedState) []drivers.FieldDiff {
	desired = applyDefaults(desired)
	var diffs []drivers.FieldDiff
	if desired.Name != observed.Name {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.name (immutable, requires replacement)", OldValue: observed.Name, NewValue: desired.Name})
	}

	if desired.Scheme != observed.Scheme {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.scheme (immutable, requires replacement)", OldValue: observed.Scheme, NewValue: desired.Scheme})
	}
	if desired.IpAddressType != observed.IpAddressType {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.ipAddressType", OldValue: observed.IpAddressType, NewValue: desired.IpAddressType})
	}
	desiredSubnets := resolveSubnets(desired)
	if !sortedStringsEqual(desiredSubnets, observed.Subnets) {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.subnets", OldValue: observed.Subnets, NewValue: desiredSubnets})
	}
	desiredSGs := sortedCopy(desired.SecurityGroups)
	if !sortedStringsEqual(desiredSGs, observed.SecurityGroups) {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.securityGroups", OldValue: observed.SecurityGroups, NewValue: desiredSGs})
	}
	if !accessLogsEqual(desired.AccessLogs, observed.AccessLogs) {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.accessLogs", OldValue: observed.AccessLogs, NewValue: desired.AccessLogs})
	}
	if desired.DeletionProtection != observed.DeletionProtection {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.deletionProtection", OldValue: observed.DeletionProtection, NewValue: desired.DeletionProtection})
	}
	if desired.IdleTimeout != observed.IdleTimeout {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.idleTimeout", OldValue: observed.IdleTimeout, NewValue: desired.IdleTimeout})
	}
	diffs = append(diffs, computeTagDiffs(desired.Tags, observed.Tags)...)
	return diffs
}

func computeTagDiffs(desired, observed map[string]string) []drivers.FieldDiff {
	var diffs []drivers.FieldDiff
	fd := drivers.FilterPraxisTags(desired)
	fo := drivers.FilterPraxisTags(observed)
	for key, value := range fd {
		if oldValue, ok := fo[key]; !ok {
			diffs = append(diffs, drivers.FieldDiff{Path: "tags." + key, OldValue: nil, NewValue: value})
		} else if oldValue != value {
			diffs = append(diffs, drivers.FieldDiff{Path: "tags." + key, OldValue: oldValue, NewValue: value})
		}
	}
	for key, value := range fo {
		if _, ok := fd[key]; !ok {
			diffs = append(diffs, drivers.FieldDiff{Path: "tags." + key, OldValue: value, NewValue: nil})
		}
	}
	sort.Slice(diffs, func(i, j int) bool { return diffs[i].Path < diffs[j].Path })
	return diffs
}

func accessLogsEqual(a, b *AccessLogConfig) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func resolveSubnets(spec ALBSpec) []string {
	if len(spec.SubnetMappings) > 0 {
		return normalizeSubnets(spec.SubnetMappings)
	}
	return sortedCopy(spec.Subnets)
}

func sortedCopy(s []string) []string {
	out := make([]string, len(s))
	copy(out, s)
	sort.Strings(out)
	return out
}

func sortedStringsEqual(a, b []string) bool {
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

// resolveSubnetMappings returns either SubnetMappings or converts Subnets.
func resolveSubnetMappings(spec ALBSpec) []SubnetMapping {
	if len(spec.SubnetMappings) > 0 {
		return spec.SubnetMappings
	}
	return subnetsToMappings(spec.Subnets)
}
