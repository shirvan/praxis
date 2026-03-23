package iamgroup

import (
	"encoding/json"
	"net/url"
	"sort"
)

func HasDrift(desired IAMGroupSpec, observed ObservedState) bool {
	if desired.Path != observed.Path {
		return true
	}
	if !inlinePoliciesEqual(desired.InlinePolicies, observed.InlinePolicies) {
		return true
	}
	return !stringSetEqual(desired.ManagedPolicyArns, observed.ManagedPolicyArns)
}

func ComputeFieldDiffs(desired IAMGroupSpec, observed ObservedState) []FieldDiffEntry {
	var diffs []FieldDiffEntry

	if desired.Path != observed.Path {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.path", OldValue: observed.Path, NewValue: desired.Path})
	}
	diffs = append(diffs, computeInlinePolicyDiffs(desired.InlinePolicies, observed.InlinePolicies)...)
	if !stringSetEqual(desired.ManagedPolicyArns, observed.ManagedPolicyArns) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.managedPolicyArns", OldValue: sortedStrings(observed.ManagedPolicyArns), NewValue: sortedStrings(desired.ManagedPolicyArns)})
	}
	return diffs
}

type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
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

func computeInlinePolicyDiffs(desired, observed map[string]string) []FieldDiffEntry {
	var diffs []FieldDiffEntry
	nd := normalizePolicyMap(desired)
	no := normalizePolicyMap(observed)
	for key, value := range nd {
		if current, ok := no[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "spec.inlinePolicies." + key, OldValue: nil, NewValue: value})
		} else if current != value {
			diffs = append(diffs, FieldDiffEntry{Path: "spec.inlinePolicies." + key, OldValue: current, NewValue: value})
		}
	}
	for key, value := range no {
		if _, ok := nd[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "spec.inlinePolicies." + key, OldValue: value, NewValue: nil})
		}
	}
	return diffs
}

func normalizePolicyDocument(document string) string {
	if document == "" {
		return ""
	}
	if decoded, err := url.QueryUnescape(document); err == nil {
		document = decoded
	}
	var parsed any
	if err := json.Unmarshal([]byte(document), &parsed); err != nil {
		return document
	}
	normalized, err := json.Marshal(parsed)
	if err != nil {
		return document
	}
	return string(normalized)
}

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
