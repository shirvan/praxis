// Package loggroup – drift.go
//
// This file implements drift detection for AWS CloudWatch Log Group.
// HasDrift compares the desired spec against the observed state from AWS and
// returns true when any mutable field has diverged. ComputeFieldDiffs produces
// a structured list of individual field changes for plan output and logging.
// Immutable fields (those that require resource replacement) are annotated.
package loggroup

import "strings"

// HasDrift compares the desired LogGroup spec against the observed
// state from AWS and returns true if any mutable field has diverged.
// It is called during Reconcile to decide whether drift correction is needed.
func HasDrift(desired LogGroupSpec, observed ObservedState) bool {
	if !retentionMatch(desired.RetentionInDays, observed.RetentionInDays) {
		return true
	}
	if strings.TrimSpace(desired.KmsKeyID) != strings.TrimSpace(observed.KmsKeyID) {
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
func ComputeFieldDiffs(desired LogGroupSpec, observed ObservedState) []FieldDiffEntry {
	var diffs []FieldDiffEntry
	if desired.LogGroupClass != "" && observed.LogGroupClass != "" && desired.LogGroupClass != observed.LogGroupClass {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.logGroupClass (immutable, requires replacement)",
			OldValue: observed.LogGroupClass,
			NewValue: desired.LogGroupClass,
		})
	}
	if !retentionMatch(desired.RetentionInDays, observed.RetentionInDays) {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.retentionInDays",
			OldValue: retentionValue(observed.RetentionInDays),
			NewValue: retentionValue(desired.RetentionInDays),
		})
	}
	if strings.TrimSpace(desired.KmsKeyID) != strings.TrimSpace(observed.KmsKeyID) {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.kmsKeyId",
			OldValue: observed.KmsKeyID,
			NewValue: desired.KmsKeyID,
		})
	}
	diffs = append(diffs, computeTagDiffs(desired.Tags, observed.Tags)...)
	return diffs
}

// FieldDiffEntry represents a single field-level difference between the desired
// spec and the observed state. Path uses dot notation (e.g. "spec.name");
// immutable fields are annotated with "(immutable, requires replacement)".
type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
}

func retentionMatch(desired, observed *int32) bool {
	if desired == nil && observed == nil {
		return true
	}
	if desired == nil || observed == nil {
		return false
	}
	return *desired == *observed
}

func retentionValue(v *int32) any {
	if v == nil {
		return nil
	}
	return *v
}

func computeTagDiffs(desired, observed map[string]string) []FieldDiffEntry {
	var diffs []FieldDiffEntry
	cleanDesired := filterPraxisTags(desired)
	cleanObserved := filterPraxisTags(observed)
	for key, value := range cleanDesired {
		if current, ok := cleanObserved[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: nil, NewValue: value})
		} else if current != value {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: current, NewValue: value})
		}
	}
	for key, value := range cleanObserved {
		if _, ok := cleanDesired[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: value, NewValue: nil})
		}
	}
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
