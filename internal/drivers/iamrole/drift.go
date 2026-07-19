package iamrole

import (
	"sort"

	"github.com/shirvan/praxis/internal/drivers"
)

// HasDrift compares the desired IAM role spec against the observed AWS state and returns
// true if any mutable field has diverged. Compared fields include: assume role policy document
// (JSON-normalized), description, max session duration, permissions boundary, inline policies,
// managed policy ARNs, and user-defined tags (excluding praxis: prefixed tags).
// Path is included so a direct Provision with an immutable change reaches the
// convergence guard and fails instead of silently accepting an impossible spec.
func HasDrift(desired IAMRoleSpec, observed ObservedState) bool {
	if desired.Path != "" && observed.Path != "" && desired.Path != observed.Path {
		return true
	}
	if !policyDocumentsEqual(desired.AssumeRolePolicyDocument, observed.AssumeRolePolicyDocument) {
		return true
	}
	if desired.Description != observed.Description {
		return true
	}
	if desired.MaxSessionDuration != observed.MaxSessionDuration {
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
	return !drivers.TagsMatch(desired.Tags, observed.Tags)
}

// ComputeFieldDiffs produces a detailed list of per-field differences between the desired spec
// and the observed AWS state. Each entry identifies the field path (e.g., "spec.description"),
// the old (observed) value, and the new (desired) value. Immutable fields like path are reported
// with an "(immutable, requires replacement)" suffix to indicate they cannot be corrected in place.
// This is used for drift reporting, audit logging, and user-facing diff displays.
func ComputeFieldDiffs(desired IAMRoleSpec, observed ObservedState) []drivers.FieldDiff {
	var diffs []drivers.FieldDiff

	if desired.Path != "" && observed.Path != "" && desired.Path != observed.Path {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.path (immutable, requires replacement)", OldValue: observed.Path, NewValue: desired.Path})
	}
	if !policyDocumentsEqual(desired.AssumeRolePolicyDocument, observed.AssumeRolePolicyDocument) {
		diffs = append(diffs, drivers.FieldDiff{
			Path:     "spec.assumeRolePolicyDocument",
			OldValue: normalizePolicyDocument(observed.AssumeRolePolicyDocument),
			NewValue: normalizePolicyDocument(desired.AssumeRolePolicyDocument),
		})
	}
	if desired.Description != observed.Description {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.description", OldValue: observed.Description, NewValue: desired.Description})
	}
	if desired.MaxSessionDuration != observed.MaxSessionDuration {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.maxSessionDuration", OldValue: observed.MaxSessionDuration, NewValue: desired.MaxSessionDuration})
	}
	if desired.PermissionsBoundary != observed.PermissionsBoundary {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.permissionsBoundary", OldValue: observed.PermissionsBoundary, NewValue: desired.PermissionsBoundary})
	}

	diffs = append(diffs, computeInlinePolicyDiffs(desired.InlinePolicies, observed.InlinePolicies)...)
	if !stringSetEqual(desired.ManagedPolicyArns, observed.ManagedPolicyArns) {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.managedPolicyArns", OldValue: sortedStrings(observed.ManagedPolicyArns), NewValue: sortedStrings(desired.ManagedPolicyArns)})
	}
	diffs = append(diffs, computeTagDiffs(desired.Tags, observed.Tags)...)
	return diffs
}

// policyDocumentsEqual compares two IAM policy documents by normalizing them to canonical JSON.
// This handles differences in whitespace, key ordering, and URL-encoding that the AWS API may introduce.
func policyDocumentsEqual(a, b string) bool {
	return normalizePolicyDocument(a) == normalizePolicyDocument(b)
}

// inlinePoliciesEqual compares two sets of inline policies by normalizing each policy document
// to canonical JSON, then checking that both maps have the same keys with identical documents.
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

func computeInlinePolicyDiffs(desired, observed map[string]string) []drivers.FieldDiff {
	var diffs []drivers.FieldDiff
	nd := normalizePolicyMap(desired)
	no := normalizePolicyMap(observed)
	for key, value := range nd {
		if current, ok := no[key]; !ok {
			diffs = append(diffs, drivers.FieldDiff{Path: "spec.inlinePolicies." + key, OldValue: nil, NewValue: value})
		} else if current != value {
			diffs = append(diffs, drivers.FieldDiff{Path: "spec.inlinePolicies." + key, OldValue: current, NewValue: value})
		}
	}
	for key, value := range no {
		if _, ok := nd[key]; !ok {
			diffs = append(diffs, drivers.FieldDiff{Path: "spec.inlinePolicies." + key, OldValue: value, NewValue: nil})
		}
	}
	return diffs
}

func computeTagDiffs(desired, observed map[string]string) []drivers.FieldDiff {
	var diffs []drivers.FieldDiff
	filteredDesired := drivers.FilterPraxisTags(desired)
	filteredObserved := drivers.FilterPraxisTags(observed)
	for key, value := range filteredDesired {
		if observedValue, ok := filteredObserved[key]; !ok {
			diffs = append(diffs, drivers.FieldDiff{Path: "tags." + key, OldValue: nil, NewValue: value})
		} else if observedValue != value {
			diffs = append(diffs, drivers.FieldDiff{Path: "tags." + key, OldValue: observedValue, NewValue: value})
		}
	}
	for key, value := range filteredObserved {
		if _, ok := filteredDesired[key]; !ok {
			diffs = append(diffs, drivers.FieldDiff{Path: "tags." + key, OldValue: value, NewValue: nil})
		}
	}
	return diffs
}

// stringSetEqual compares two string slices as unordered sets, returning true
// if they contain exactly the same elements regardless of order.
func stringSetEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[string]int, len(a))
	for _, value := range a {
		counts[value]++
	}
	for _, value := range b {
		counts[value]--
	}
	for _, value := range counts {
		if value != 0 {
			return false
		}
	}
	return true
}

func sortedStrings(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}
