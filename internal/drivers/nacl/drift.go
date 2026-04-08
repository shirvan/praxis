package nacl

import (
	"fmt"
	"sort"
	"strings"

	"github.com/shirvan/praxis/internal/drivers"
)

// FieldDiffEntry represents a single field difference between desired and observed state.
type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
}

// HasDrift returns true if the desired spec and observed state differ.
//
// Network ACL drift rules:
//   - Tags are compared (excluding praxis:-prefixed tags).
//   - Ingress and egress rules are compared by rule number, normalizing
//     protocol names to IANA numbers (e.g. "tcp" → "6").
//   - Subnet associations are compared as sets of subnet IDs.
func HasDrift(desired NetworkACLSpec, observed ObservedState) bool {
	if !drivers.TagsMatch(desired.Tags, observed.Tags) {
		return true
	}
	if !rulesMatch(desired.IngressRules, observed.IngressRules) {
		return true
	}
	if !rulesMatch(desired.EgressRules, observed.EgressRules) {
		return true
	}
	if !associationsMatch(desired.SubnetAssociations, observed.Associations) {
		return true
	}
	return false
}

func ComputeFieldDiffs(desired NetworkACLSpec, observed ObservedState) []FieldDiffEntry {
	var diffs []FieldDiffEntry

	if desired.VpcId != observed.VpcId && observed.VpcId != "" {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.vpcId (immutable, ignored)",
			OldValue: observed.VpcId,
			NewValue: desired.VpcId,
		})
	}

	diffs = append(diffs, computeTagDiffs(desired.Tags, observed.Tags)...)
	diffs = append(diffs, computeRuleDiffs("spec.ingressRules", desired.IngressRules, observed.IngressRules)...)
	diffs = append(diffs, computeRuleDiffs("spec.egressRules", desired.EgressRules, observed.EgressRules)...)
	diffs = append(diffs, computeAssociationDiffs(desired.SubnetAssociations, observed.Associations)...)

	return diffs
}

func rulesMatch(desired, observed []NetworkACLRule) bool {
	if len(desired) != len(observed) {
		return false
	}
	desiredMap := ruleMap(desired)
	observedMap := ruleMap(observed)
	if len(desiredMap) != len(observedMap) {
		return false
	}
	for ruleNumber, desiredRule := range desiredMap {
		observedRule, ok := observedMap[ruleNumber]
		if !ok || !ruleEqual(desiredRule, observedRule) {
			return false
		}
	}
	return true
}

func associationsMatch(desired []string, observed []NetworkACLAssociation) bool {
	if len(desired) != len(observed) {
		return false
	}
	desiredSet := make(map[string]struct{}, len(desired))
	for _, subnetID := range desired {
		desiredSet[subnetID] = struct{}{}
	}
	for _, association := range observed {
		if _, ok := desiredSet[association.SubnetId]; !ok {
			return false
		}
	}
	return true
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

func computeRuleDiffs(path string, desired, observed []NetworkACLRule) []FieldDiffEntry {
	var diffs []FieldDiffEntry
	desiredMap := ruleMap(desired)
	observedMap := ruleMap(observed)
	keys := make([]int, 0, len(desiredMap)+len(observedMap))
	seen := make(map[int]struct{}, len(desiredMap)+len(observedMap))
	for ruleNumber := range desiredMap {
		keys = append(keys, ruleNumber)
		seen[ruleNumber] = struct{}{}
	}
	for ruleNumber := range observedMap {
		if _, ok := seen[ruleNumber]; !ok {
			keys = append(keys, ruleNumber)
		}
	}
	sort.Ints(keys)
	for _, ruleNumber := range keys {
		desiredRule, desiredOK := desiredMap[ruleNumber]
		observedRule, observedOK := observedMap[ruleNumber]
		switch {
		case desiredOK && !observedOK:
			diffs = append(diffs, FieldDiffEntry{Path: fmt.Sprintf("%s[%d]", path, ruleNumber), OldValue: nil, NewValue: desiredRule})
		case !desiredOK && observedOK:
			diffs = append(diffs, FieldDiffEntry{Path: fmt.Sprintf("%s[%d]", path, ruleNumber), OldValue: observedRule, NewValue: nil})
		case desiredOK && observedOK && !ruleEqual(desiredRule, observedRule):
			diffs = append(diffs, FieldDiffEntry{Path: fmt.Sprintf("%s[%d]", path, ruleNumber), OldValue: observedRule, NewValue: desiredRule})
		}
	}
	return diffs
}

func computeAssociationDiffs(desired []string, observed []NetworkACLAssociation) []FieldDiffEntry {
	var diffs []FieldDiffEntry
	desiredSet := make(map[string]struct{}, len(desired))
	for _, subnetID := range desired {
		desiredSet[subnetID] = struct{}{}
	}
	observedSet := make(map[string]struct{}, len(observed))
	for _, association := range observed {
		observedSet[association.SubnetId] = struct{}{}
	}
	for _, subnetID := range desired {
		if _, ok := observedSet[subnetID]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: fmt.Sprintf("spec.subnetAssociations[%s]", subnetID), OldValue: nil, NewValue: subnetID})
		}
	}
	for _, association := range observed {
		if _, ok := desiredSet[association.SubnetId]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: fmt.Sprintf("spec.subnetAssociations[%s]", association.SubnetId), OldValue: association.SubnetId, NewValue: nil})
		}
	}
	return diffs
}

func ruleMap(rules []NetworkACLRule) map[int]NetworkACLRule {
	indexed := make(map[int]NetworkACLRule, len(rules))
	for _, rule := range rules {
		indexed[rule.RuleNumber] = normalizeRule(rule)
	}
	return indexed
}

func ruleEqual(a, b NetworkACLRule) bool {
	a = normalizeRule(a)
	b = normalizeRule(b)
	return a.RuleNumber == b.RuleNumber &&
		a.Protocol == b.Protocol &&
		a.RuleAction == b.RuleAction &&
		a.CidrBlock == b.CidrBlock &&
		a.FromPort == b.FromPort &&
		a.ToPort == b.ToPort
}

func normalizeRule(rule NetworkACLRule) NetworkACLRule {
	rule.Protocol = normalizeProtocolForDrift(strings.TrimSpace(strings.ToLower(rule.Protocol)))
	rule.RuleAction = strings.TrimSpace(strings.ToLower(rule.RuleAction))
	rule.CidrBlock = strings.TrimSpace(rule.CidrBlock)
	return rule
}

// normalizeProtocolForDrift maps protocol names to IANA numbers so that
// user-friendly names ("tcp") match AWS-returned numbers ("6") during drift comparison.
func normalizeProtocolForDrift(protocol string) string {
	switch protocol {
	case "", "all", "-1":
		return "-1"
	case "tcp":
		return "6"
	case "udp":
		return "17"
	case "icmp":
		return "1"
	default:
		return protocol
	}
}
