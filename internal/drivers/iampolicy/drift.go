package iampolicy

import (
	"github.com/shirvan/praxis/internal/drivers"
)

// HasDrift compares the desired IAM policy spec against the observed AWS state.
// Immutable identity/configuration fields are included so Provision can reject
// an in-place change instead of silently accepting desired state that AWS can
// never converge.
func HasDrift(desired IAMPolicySpec, observed ObservedState) bool {
	if desired.PolicyName != observed.PolicyName || desired.Path != observed.Path || desired.Description != observed.Description {
		return true
	}
	if !policyDocumentsEqual(desired.PolicyDocument, observed.PolicyDocument) {
		return true
	}
	return !drivers.TagsMatch(desired.Tags, observed.Tags)
}

// ComputeFieldDiffs produces a detailed list of per-field differences between desired and observed.
// Immutable fields are reported as replacement-requiring differences.
func ComputeFieldDiffs(desired IAMPolicySpec, observed ObservedState) []drivers.FieldDiff {
	var diffs []drivers.FieldDiff

	if desired.PolicyName != observed.PolicyName {
		diffs = append(diffs, drivers.FieldDiff{
			Path:     "spec.policyName (immutable, requires replacement)",
			OldValue: observed.PolicyName,
			NewValue: desired.PolicyName,
		})
	}
	if desired.Path != observed.Path {
		diffs = append(diffs, drivers.FieldDiff{
			Path:     "spec.path (immutable, requires replacement)",
			OldValue: observed.Path,
			NewValue: desired.Path,
		})
	}

	if desired.Description != observed.Description {
		diffs = append(diffs, drivers.FieldDiff{
			Path:     "spec.description (immutable, requires replacement)",
			OldValue: observed.Description,
			NewValue: desired.Description,
		})
	}

	if !policyDocumentsEqual(desired.PolicyDocument, observed.PolicyDocument) {
		diffs = append(diffs, drivers.FieldDiff{
			Path:     "spec.policyDocument",
			OldValue: normalizePolicyDocument(observed.PolicyDocument),
			NewValue: normalizePolicyDocument(desired.PolicyDocument),
		})
	}

	diffs = append(diffs, computeTagDiffs(desired.Tags, observed.Tags)...)
	return diffs
}

func policyDocumentsEqual(a, b string) bool {
	return normalizePolicyDocument(a) == normalizePolicyDocument(b)
}

func computeTagDiffs(desired, observed map[string]string) []drivers.FieldDiff {
	var diffs []drivers.FieldDiff
	desiredFiltered := drivers.FilterPraxisTags(desired)
	observedFiltered := drivers.FilterPraxisTags(observed)
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
	return diffs
}
