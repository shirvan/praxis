package iamuser

import "strings"

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
	return !tagsMatch(desired.Tags, observed.Tags)
}

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
	filteredDesired := filterPraxisTags(desired)
	filteredObserved := filterPraxisTags(observed)
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

func filterPraxisTags(m map[string]string) map[string]string {
	if len(m) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(m))
	for key, value := range m {
		if !strings.HasPrefix(key, "praxis:") {
			out[key] = value
		}
	}
	return out
}
