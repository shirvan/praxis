package sg

import (
	"fmt"
	"sort"
	"strconv"
)

// NormalizedRule is the canonical representation of a security group rule
// for order-independent set comparison. AWS returns rules in arbitrary order,
// so diffing must operate on sets of normalized tuples.
type NormalizedRule struct {
	Direction string `json:"direction"` // "ingress" or "egress"
	Protocol  string `json:"protocol"`  // "tcp", "udp", "icmp", "all"
	FromPort  int32  `json:"fromPort"`
	ToPort    int32  `json:"toPort"`
	Target    string `json:"target"` // "cidr:10.0.0.0/8"
}

// ruleKey returns a unique string key for set membership checks.
func (r NormalizedRule) ruleKey() string {
	return r.Direction + "|" + r.Protocol + "|" +
		strconv.Itoa(int(r.FromPort)) + "|" +
		strconv.Itoa(int(r.ToPort)) + "|" +
		r.Target
}

// Normalize converts a SecurityGroupSpec into a sorted slice of NormalizedRules.
// This expands each spec rule into one NormalizedRule per CIDR block,
// lowercases protocols, and normalizes "-1" to "all".
func Normalize(spec SecurityGroupSpec) []NormalizedRule {
	var rules []NormalizedRule

	for _, r := range spec.IngressRules {
		rules = append(rules, NormalizedRule{
			Direction: "ingress",
			Protocol:  normalizeProtocol(r.Protocol),
			FromPort:  r.FromPort,
			ToPort:    r.ToPort,
			Target:    "cidr:" + r.CidrBlock,
		})
	}

	for _, r := range spec.EgressRules {
		rules = append(rules, NormalizedRule{
			Direction: "egress",
			Protocol:  normalizeProtocol(r.Protocol),
			FromPort:  r.FromPort,
			ToPort:    r.ToPort,
			Target:    "cidr:" + r.CidrBlock,
		})
	}

	sortRules(rules)
	return rules
}

// HasDrift returns true if the desired and observed rule sets differ,
// or if tags differ.
func HasDrift(desired SecurityGroupSpec, observed ObservedState) bool {
	desiredRules := Normalize(desired)
	observedRules := mergeObservedRules(observed)

	if !rulesEqual(desiredRules, observedRules) {
		return true
	}

	if !tagsMatch(desired.Tags, observed.Tags) {
		return true
	}

	return false
}

// ComputeDiff returns the rules to add and remove to converge observed → desired.
// This is a pure set difference operation on normalized rule tuples.
func ComputeDiff(desired, observed []NormalizedRule) (toAdd, toRemove []NormalizedRule) {
	desiredSet := make(map[string]NormalizedRule, len(desired))
	for _, r := range desired {
		desiredSet[r.ruleKey()] = r
	}
	observedSet := make(map[string]NormalizedRule, len(observed))
	for _, r := range observed {
		observedSet[r.ruleKey()] = r
	}

	// toAdd = desired - observed
	for key, rule := range desiredSet {
		if _, exists := observedSet[key]; !exists {
			toAdd = append(toAdd, rule)
		}
	}

	// toRemove = observed - desired
	for key, rule := range observedSet {
		if _, exists := desiredSet[key]; !exists {
			toRemove = append(toRemove, rule)
		}
	}

	sortRules(toAdd)
	sortRules(toRemove)
	return toAdd, toRemove
}

// ComputeFieldDiffs returns a human-readable set of differences between the
// desired security group spec and the current AWS-observed state.
func ComputeFieldDiffs(desired SecurityGroupSpec, observed ObservedState) []FieldDiffEntry {
	var diffs []FieldDiffEntry

	if desired.GroupName != observed.GroupName {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.groupName", OldValue: observed.GroupName, NewValue: desired.GroupName})
	}
	if desired.Description != observed.Description {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.description", OldValue: observed.Description, NewValue: desired.Description})
	}
	if desired.VpcId != observed.VpcId {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.vpcId", OldValue: observed.VpcId, NewValue: desired.VpcId})
	}

	for key, value := range desired.Tags {
		if observedValue, ok := observed.Tags[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: nil, NewValue: value})
		} else if observedValue != value {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: observedValue, NewValue: value})
		}
	}
	for key, value := range observed.Tags {
		if _, ok := desired.Tags[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: value, NewValue: nil})
		}
	}

	desiredRules := Normalize(desired)
	observedRules := mergeObservedRules(observed)
	toAdd, toRemove := ComputeDiff(desiredRules, observedRules)
	for _, rule := range toAdd {
		diffs = append(diffs, FieldDiffEntry{Path: ruleDiffPath(rule), OldValue: nil, NewValue: rule})
	}
	for _, rule := range toRemove {
		diffs = append(diffs, FieldDiffEntry{Path: ruleDiffPath(rule), OldValue: rule, NewValue: nil})
	}

	return diffs
}

// FieldDiffEntry is the provider-specific diff unit consumed by the generic
// plan renderer.
type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// mergeObservedRules combines ingress and egress rules from ObservedState.
func mergeObservedRules(obs ObservedState) []NormalizedRule {
	rules := make([]NormalizedRule, 0, len(obs.IngressRules)+len(obs.EgressRules))
	rules = append(rules, obs.IngressRules...)
	rules = append(rules, obs.EgressRules...)
	sortRules(rules)
	return rules
}

// rulesEqual checks if two sorted NormalizedRule slices are identical.
func rulesEqual(a, b []NormalizedRule) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ruleKey() != b[i].ruleKey() {
			return false
		}
	}
	return true
}

// sortRules sorts NormalizedRules by their string key for deterministic comparison.
func sortRules(rules []NormalizedRule) {
	sort.Slice(rules, func(i, j int) bool {
		return rules[i].ruleKey() < rules[j].ruleKey()
	})
}

// tagsMatch returns true when the two tag maps are semantically equal.
func tagsMatch(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

// SplitByDirection splits a rule slice into ingress and egress slices.
func SplitByDirection(rules []NormalizedRule) (ingress, egress []NormalizedRule) {
	for _, r := range rules {
		switch r.Direction {
		case "ingress":
			ingress = append(ingress, r)
		case "egress":
			egress = append(egress, r)
		}
	}
	return
}

func ruleDiffPath(rule NormalizedRule) string {
	collection := "spec.ingressRules"
	if rule.Direction == "egress" {
		collection = "spec.egressRules"
	}
	return fmt.Sprintf("%s[%s]", collection, rule.ruleKey())
}
