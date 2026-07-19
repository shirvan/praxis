// Package ekscluster – drift.go
//
// This file implements drift detection for EKS clusters. HasDrift compares the
// desired spec against the observed state from AWS and returns true when any
// mutable field has diverged. ComputeFieldDiffs produces a structured list of
// individual field changes for plan output and logging; immutable fields are
// annotated with "(immutable, requires replacement)".
package ekscluster

import (
	"sort"

	"github.com/shirvan/praxis/internal/drivers"
)

// HasDrift compares the desired EKSCluster spec against the observed state from
// AWS and returns true if any mutable field has diverged. It is called during
// Reconcile to decide whether drift correction is needed. Immutable fields
// (role, subnets, security groups) are intentionally excluded — they cannot be
// corrected in place.
func HasDrift(desired EKSClusterSpec, observed ObservedState) bool {
	if validateEKSImmutableIdentity(desired, observed) != nil {
		return true
	}
	if configDrift(desired, observed) {
		return true
	}
	return !drivers.TagsMatch(desired.Tags, observed.Tags)
}

// versionDrift reports whether the desired control-plane version differs from
// the observed one. An empty desired version means "track the AWS default", so
// it never drifts.
func versionDrift(desired EKSClusterSpec, observed ObservedState) bool {
	return desired.Version != "" && desired.Version != observed.Version
}

func endpointAccessDrift(spec EKSClusterSpec, observed ObservedState) bool {
	if spec.EndpointPublicAccess != observed.EndpointPublicAccess || spec.EndpointPrivateAccess != observed.EndpointPrivateAccess {
		return true
	}
	if spec.EndpointPublicAccess && !stringSetEqual(normalizePublicCidrs(spec.PublicAccessCidrs), normalizePublicCidrs(observed.PublicAccessCidrs)) {
		return true
	}
	return false
}

func loggingDrift(spec EKSClusterSpec, observed ObservedState) bool {
	return !stringSetEqual(spec.EnabledLoggingTypes, observed.EnabledLoggingTypes)
}

// configDrift reports whether any field converged via UpdateClusterConfig or
// UpdateClusterVersion has diverged from the observed state.
func configDrift(desired EKSClusterSpec, observed ObservedState) bool {
	if versionDrift(desired, observed) {
		return true
	}
	if desired.EndpointPublicAccess != observed.EndpointPublicAccess {
		return true
	}
	if desired.EndpointPrivateAccess != observed.EndpointPrivateAccess {
		return true
	}
	if desired.EndpointPublicAccess && !stringSetEqual(normalizePublicCidrs(desired.PublicAccessCidrs), normalizePublicCidrs(observed.PublicAccessCidrs)) {
		return true
	}
	if !stringSetEqual(desired.EnabledLoggingTypes, observed.EnabledLoggingTypes) {
		return true
	}
	return false
}

// ComputeFieldDiffs produces a structured list of individual field changes
// between the desired spec and observed state. Used for plan output, CLI
// display, and audit logging.
func ComputeFieldDiffs(desired EKSClusterSpec, observed ObservedState) []drivers.FieldDiff {
	var diffs []drivers.FieldDiff

	// Immutable fields — reported for visibility, never corrected in place.
	if observed.RoleArn != "" && desired.RoleArn != observed.RoleArn {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.roleArn (immutable, requires replacement)", OldValue: observed.RoleArn, NewValue: desired.RoleArn})
	}
	if len(observed.SubnetIds) > 0 && !stringSetEqual(desired.SubnetIds, observed.SubnetIds) {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.subnetIds (immutable, requires replacement)", OldValue: sortedCopy(observed.SubnetIds), NewValue: sortedCopy(desired.SubnetIds)})
	}
	if len(desired.SecurityGroupIds) > 0 && !stringSetEqual(desired.SecurityGroupIds, observed.SecurityGroupIds) {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.securityGroupIds (immutable, requires replacement)", OldValue: sortedCopy(observed.SecurityGroupIds), NewValue: sortedCopy(desired.SecurityGroupIds)})
	}

	// Mutable fields.
	if versionDrift(desired, observed) {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.version", OldValue: observed.Version, NewValue: desired.Version})
	}
	if desired.EndpointPublicAccess != observed.EndpointPublicAccess {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.endpointPublicAccess", OldValue: observed.EndpointPublicAccess, NewValue: desired.EndpointPublicAccess})
	}
	if desired.EndpointPrivateAccess != observed.EndpointPrivateAccess {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.endpointPrivateAccess", OldValue: observed.EndpointPrivateAccess, NewValue: desired.EndpointPrivateAccess})
	}
	if desired.EndpointPublicAccess && !stringSetEqual(normalizePublicCidrs(desired.PublicAccessCidrs), normalizePublicCidrs(observed.PublicAccessCidrs)) {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.publicAccessCidrs", OldValue: normalizePublicCidrs(observed.PublicAccessCidrs), NewValue: normalizePublicCidrs(desired.PublicAccessCidrs)})
	}
	if !stringSetEqual(desired.EnabledLoggingTypes, observed.EnabledLoggingTypes) {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.enabledLoggingTypes", OldValue: sortedCopy(observed.EnabledLoggingTypes), NewValue: sortedCopy(desired.EnabledLoggingTypes)})
	}

	diffs = append(diffs, computeTagDiffs(desired.Tags, observed.Tags)...)
	return diffs
}

// normalizePublicCidrs treats an unset CIDR list as AWS's default of
// "0.0.0.0/0", which is what a cluster reports when public access is enabled
// without an explicit allow-list.
func normalizePublicCidrs(cidrs []string) []string {
	if len(cidrs) == 0 {
		return []string{"0.0.0.0/0"}
	}
	return sortedCopy(cidrs)
}

// stringSetEqual reports whether two slices contain the same set of values,
// ignoring order and duplicates.
func stringSetEqual(a, b []string) bool {
	sa, sb := map[string]struct{}{}, map[string]struct{}{}
	for _, v := range a {
		sa[v] = struct{}{}
	}
	for _, v := range b {
		sb[v] = struct{}{}
	}
	if len(sa) != len(sb) {
		return false
	}
	for v := range sa {
		if _, ok := sb[v]; !ok {
			return false
		}
	}
	return true
}

func sortedCopy(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}

func computeTagDiffs(desired, observed map[string]string) []drivers.FieldDiff {
	var diffs []drivers.FieldDiff
	cleanDesired := drivers.FilterPraxisTags(desired)
	cleanObserved := drivers.FilterPraxisTags(observed)
	for key, value := range cleanDesired {
		if current, ok := cleanObserved[key]; !ok {
			diffs = append(diffs, drivers.FieldDiff{Path: "tags." + key, OldValue: nil, NewValue: value})
		} else if current != value {
			diffs = append(diffs, drivers.FieldDiff{Path: "tags." + key, OldValue: current, NewValue: value})
		}
	}
	for key, value := range cleanObserved {
		if _, ok := cleanDesired[key]; !ok {
			diffs = append(diffs, drivers.FieldDiff{Path: "tags." + key, OldValue: value, NewValue: nil})
		}
	}
	return diffs
}
