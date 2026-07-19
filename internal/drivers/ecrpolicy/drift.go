// Package ecrpolicy – drift.go
//
// This file implements drift detection for AWS ECR Lifecycle Policy.
// HasDrift compares the desired spec against the observed state from AWS and
// returns true when any mutable field has diverged. ComputeFieldDiffs produces
// a structured list of individual field changes for plan output and logging.
// Immutable fields (those that require resource replacement) are annotated.
package ecrpolicy

import "github.com/shirvan/praxis/internal/drivers"

// HasDrift compares the desired ECRLifecyclePolicy spec against the observed
// state from AWS and returns true if any mutable field has diverged.
// It is called during Reconcile to decide whether drift correction is needed.
func HasDrift(desired ECRLifecyclePolicySpec, observed ObservedState) bool {
	return len(ComputeFieldDiffs(desired, observed)) > 0
}

// ComputeFieldDiffs produces a structured list of individual field changes
// between the desired spec and observed state. Used for plan output, CLI
// display, and audit logging. Immutable field changes are clearly annotated.
func ComputeFieldDiffs(desired ECRLifecyclePolicySpec, observed ObservedState) []drivers.FieldDiff {
	var diffs []drivers.FieldDiff
	if desired.RepositoryName != observed.RepositoryName {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.repositoryName (immutable, requires replacement)", OldValue: observed.RepositoryName, NewValue: desired.RepositoryName})
	}
	if observedRegion := regionFromRepositoryARN(observed.RepositoryArn); observedRegion != "" && observedRegion != desired.Region {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.region (immutable, requires replacement)", OldValue: observedRegion, NewValue: desired.Region})
	}
	if normalizePolicy(desired.LifecyclePolicyText) != normalizePolicy(observed.LifecyclePolicyText) {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.lifecyclePolicyText", OldValue: observed.LifecyclePolicyText, NewValue: desired.LifecyclePolicyText})
	}
	return diffs
}
