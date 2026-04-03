// Package nlb – drift.go
//
// This file implements drift detection for AWS Network Load Balancer (NLB).
// HasDrift compares the desired spec against the observed state from AWS and
// returns true when any mutable field has diverged. ComputeFieldDiffs produces
// a structured list of individual field changes for plan output and logging.
// Immutable fields (those that require resource replacement) are annotated.
package nlb

import (
	"sort"
)

// FieldDiffEntry represents a single field-level difference between the desired
// spec and the observed state. Path uses dot notation (e.g. "spec.name");
// immutable fields are annotated with "(immutable, requires replacement)".
type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
}

// HasDrift compares the desired NLB spec against the observed
// state from AWS and returns true if any mutable field has diverged.
// It is called during Reconcile to decide whether drift correction is needed.
func HasDrift(desired NLBSpec, observed ObservedState) bool {
	desired = applyDefaults(desired)
	if desired.IpAddressType != observed.IpAddressType {
		return true
	}
	desiredSubnets := resolveSubnets(desired)
	if !sortedStringsEqual(desiredSubnets, observed.Subnets) {
		return true
	}
	if desired.CrossZoneLoadBalancing != observed.CrossZoneLoadBalancing {
		return true
	}
	if desired.DeletionProtection != observed.DeletionProtection {
		return true
	}
	if !tagsMatch(desired.Tags, observed.Tags) {
		return true
	}
	return false
}

// ComputeFieldDiffs produces a structured list of individual field changes
// between the desired spec and observed state. Used for plan output, CLI
// display, and audit logging. Immutable field changes are clearly annotated.
func ComputeFieldDiffs(desired NLBSpec, observed ObservedState) []FieldDiffEntry {
	desired = applyDefaults(desired)
	var diffs []FieldDiffEntry

	if desired.Scheme != observed.Scheme {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.scheme (immutable, requires replacement)", OldValue: observed.Scheme, NewValue: desired.Scheme})
	}
	if desired.IpAddressType != observed.IpAddressType {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.ipAddressType", OldValue: observed.IpAddressType, NewValue: desired.IpAddressType})
	}
	desiredSubnets := resolveSubnets(desired)
	if !sortedStringsEqual(desiredSubnets, observed.Subnets) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.subnets", OldValue: observed.Subnets, NewValue: desiredSubnets})
	}
	if desired.CrossZoneLoadBalancing != observed.CrossZoneLoadBalancing {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.crossZoneLoadBalancing", OldValue: observed.CrossZoneLoadBalancing, NewValue: desired.CrossZoneLoadBalancing})
	}
	if desired.DeletionProtection != observed.DeletionProtection {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.deletionProtection", OldValue: observed.DeletionProtection, NewValue: desired.DeletionProtection})
	}
	diffs = append(diffs, computeTagDiffs(desired.Tags, observed.Tags)...)
	return diffs
}

func computeTagDiffs(desired, observed map[string]string) []FieldDiffEntry {
	var diffs []FieldDiffEntry
	fd := filterPraxisTags(desired)
	fo := filterPraxisTags(observed)
	for key, value := range fd {
		if oldValue, ok := fo[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: nil, NewValue: value})
		} else if oldValue != value {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: oldValue, NewValue: value})
		}
	}
	for key, value := range fo {
		if _, ok := fd[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: value, NewValue: nil})
		}
	}
	sort.Slice(diffs, func(i, j int) bool { return diffs[i].Path < diffs[j].Path })
	return diffs
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

func sortedCopy(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	out := make([]string, len(s))
	copy(out, s)
	sort.Strings(out)
	return out
}

func sortedStringsEqual(a, b []string) bool {
	sa := sortedCopy(a)
	sb := sortedCopy(b)
	if len(sa) != len(sb) {
		return false
	}
	for i := range sa {
		if sa[i] != sb[i] {
			return false
		}
	}
	return true
}
