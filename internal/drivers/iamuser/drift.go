package iamuser

import (
	"github.com/shirvan/praxis/internal/drivers"
)

// HasDrift compares the desired IAM user spec against the observed AWS state.
// Compared fields: path, permissions boundary, inline policies (JSON-normalized),
// managed policy ARNs (as unordered sets), groups (as unordered sets), and user tags.
func HasDrift(desired IAMUserSpec, observed ObservedState) bool {
	if desired.Path != observed.Path {
		return true
	}
	if desired.PermissionsBoundary != observed.PermissionsBoundary {
		return true
	}
	if !inlinePoliciesEqual(desired.InlinePolicies, observed.InlinePolicies) {
		return true
	}
	if !stringSetEqual(desired.ManagedPolicyArns, observed.ManagedPolicyArns) {
		return true
	}
	if !stringSetEqual(desired.Groups, observed.Groups) {
		return true
	}
	return !drivers.TagsMatch(desired.Tags, observed.Tags)
}

// ComputeFieldDiffs produces a detailed list of per-field differences for drift reporting.
func ComputeFieldDiffs(desired IAMUserSpec, observed ObservedState) []FieldDiffEntry {
	var diffs []FieldDiffEntry

	if desired.Path != observed.Path {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.path", OldValue: observed.Path, NewValue: desired.Path})
	}
	if desired.PermissionsBoundary != observed.PermissionsBoundary {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.permissionsBoundary", OldValue: observed.PermissionsBoundary, NewValue: desired.PermissionsBoundary})
	}

	diffs = append(diffs, computeInlinePolicyDiffs(desired.InlinePolicies, observed.InlinePolicies)...)

	if !stringSetEqual(desired.ManagedPolicyArns, observed.ManagedPolicyArns) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.managedPolicyArns", OldValue: sortedStrings(observed.ManagedPolicyArns), NewValue: sortedStrings(desired.ManagedPolicyArns)})
	}
	if !stringSetEqual(desired.Groups, observed.Groups) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.groups", OldValue: sortedStrings(observed.Groups), NewValue: sortedStrings(desired.Groups)})
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

func computeInlinePolicyDiffs(desired, observed map[string]string) []FieldDiffEntry {
	var diffs []FieldDiffEntry
	normalizedDesired := normalizePolicyMap(desired)
	normalizedObserved := normalizePolicyMap(observed)
	for key, value := range normalizedDesired {
		if current, ok := normalizedObserved[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "spec.inlinePolicies." + key, OldValue: nil, NewValue: value})
		} else if current != value {
			diffs = append(diffs, FieldDiffEntry{Path: "spec.inlinePolicies." + key, OldValue: current, NewValue: value})
		}
	}
	for key, value := range normalizedObserved {
		if _, ok := normalizedDesired[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "spec.inlinePolicies." + key, OldValue: value, NewValue: nil})
		}
	}
	return diffs
}

func inlinePoliciesEqual(desired, observed map[string]string) bool {
	nd := normalizePolicyMap(desired)
	no := normalizePolicyMap(observed)
	if len(nd) != len(no) {
		return false
	}
	for key, value := range nd {
		if other, ok := no[key]; !ok || other != value {
			return false
		}
	}
	return true
}

func normalizePolicyMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = normalizePolicyDocument(value)
	}
	return out
}

func computeTagDiffs(desired, observed map[string]string) []FieldDiffEntry {
	var diffs []FieldDiffEntry
	filteredDesired := drivers.FilterPraxisTags(desired)
	filteredObserved := drivers.FilterPraxisTags(observed)
	for key, value := range filteredDesired {
		if observedValue, ok := filteredObserved[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: nil, NewValue: value})
		} else if observedValue != value {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: observedValue, NewValue: value})
		}
	}
	for key, value := range filteredObserved {
		if _, ok := filteredDesired[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: value, NewValue: nil})
		}
	}
	return diffs
}
