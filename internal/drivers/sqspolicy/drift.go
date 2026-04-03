// Package sqspolicy – drift.go
//
// This file implements drift detection for AWS SQS Queue Policy.
// HasDrift compares the desired spec against the observed state from AWS and
// returns true when any mutable field has diverged. ComputeFieldDiffs produces
// a structured list of individual field changes for plan output and logging.
// Immutable fields (those that require resource replacement) are annotated.
package sqspolicy

import "encoding/json"

// FieldDiffEntry represents a single field-level difference between the desired
// spec and the observed state. Path uses dot notation (e.g. "spec.name");
// immutable fields are annotated with "(immutable, requires replacement)".
type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
}

// HasDrift compares the desired SQSQueuePolicy spec against the observed
// state from AWS and returns true if any mutable field has diverged.
// It is called during Reconcile to decide whether drift correction is needed.
func HasDrift(desired SQSQueuePolicySpec, observed ObservedState) bool {
	return !policiesEqual(desired.Policy, observed.Policy)
}

// ComputeFieldDiffs produces a structured list of individual field changes
// between the desired spec and observed state. Used for plan output, CLI
// display, and audit logging. Immutable field changes are clearly annotated.
func ComputeFieldDiffs(desired SQSQueuePolicySpec, observed ObservedState) []FieldDiffEntry {
	if policiesEqual(desired.Policy, observed.Policy) {
		return nil
	}
	return []FieldDiffEntry{{Path: "spec.policy", OldValue: observed.Policy, NewValue: desired.Policy}}
}

func policiesEqual(a, b string) bool {
	if a == b {
		return true
	}
	if a == "" || b == "" {
		return false
	}
	var aObj any
	var bObj any
	if json.Unmarshal([]byte(a), &aObj) != nil {
		return a == b
	}
	if json.Unmarshal([]byte(b), &bObj) != nil {
		return a == b
	}
	aNorm, _ := json.Marshal(aObj)
	bNorm, _ := json.Marshal(bObj)
	return string(aNorm) == string(bNorm)
}
