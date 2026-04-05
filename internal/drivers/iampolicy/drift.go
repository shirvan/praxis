package iampolicy

import (
	"github.com/shirvan/praxis/internal/drivers"
)

// HasDrift compares the desired IAM policy spec against the observed AWS state.
// Only mutable fields are compared: the policy document (JSON-normalized) and tags.
// Immutable fields like path and description are not compared for drift.
func HasDrift(desired IAMPolicySpec, observed ObservedState) bool {
	if !policyDocumentsEqual(desired.PolicyDocument, observed.PolicyDocument) {
		return true
	}
	return !drivers.TagsMatch(desired.Tags, observed.Tags)
}

// ComputeFieldDiffs produces a detailed list of per-field differences between desired and observed.
// Immutable fields (path, description) are reported with "(immutable, ignored)" annotations.
func ComputeFieldDiffs(desired IAMPolicySpec, observed ObservedState) []FieldDiffEntry {
	var diffs []FieldDiffEntry

	if desired.Path != "" && observed.Path != "" && desired.Path != observed.Path {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.path (immutable, ignored)",
			OldValue: observed.Path,
			NewValue: desired.Path,
		})
	}

	if desired.Description != observed.Description {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.description (immutable, ignored)",
			OldValue: observed.Description,
			NewValue: desired.Description,
		})
	}

	if !policyDocumentsEqual(desired.PolicyDocument, observed.PolicyDocument) {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.policyDocument",
			OldValue: normalizePolicyDocument(observed.PolicyDocument),
			NewValue: normalizePolicyDocument(desired.PolicyDocument),
		})
	}

	diffs = append(diffs, computeTagDiffs(desired.Tags, observed.Tags)...)
	return diffs
}

// FieldDiffEntry represents a single field-level difference between desired and observed state.
type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
}

func policyDocumentsEqual(a, b string) bool {
	return normalizePolicyDocument(a) == normalizePolicyDocument(b)
}

func computeTagDiffs(desired, observed map[string]string) []FieldDiffEntry {
	var diffs []FieldDiffEntry
	desiredFiltered := drivers.FilterPraxisTags(desired)
	observedFiltered := drivers.FilterPraxisTags(observed)
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
	return diffs
}
