package listenerrule

import (
	"sort"
	"strings"
)

type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
}

func HasDrift(desired ListenerRuleSpec, observed ObservedState) bool {
	if desired.Priority != observed.Priority {
		return true
	}
	if !conditionsEqual(desired.Conditions, observed.Conditions) {
		return true
	}
	if !actionsEqual(desired.Actions, observed.Actions) {
		return true
	}
	if !tagsMatch(desired.Tags, observed.Tags) {
		return true
	}
	return false
}

func ComputeFieldDiffs(desired ListenerRuleSpec, observed ObservedState) []FieldDiffEntry {
	var diffs []FieldDiffEntry
	if desired.ListenerArn != observed.ListenerArn {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.listenerArn (immutable, requires replacement)", OldValue: observed.ListenerArn, NewValue: desired.ListenerArn})
	}
	if desired.Priority != observed.Priority {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.priority", OldValue: observed.Priority, NewValue: desired.Priority})
	}
	if !conditionsEqual(desired.Conditions, observed.Conditions) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.conditions", OldValue: observed.Conditions, NewValue: desired.Conditions})
	}
	if !actionsEqual(desired.Actions, observed.Actions) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.actions", OldValue: observed.Actions, NewValue: desired.Actions})
	}
	for _, diff := range computeTagDiffs(desired.Tags, observed.Tags) {
		diffs = append(diffs, diff)
	}
	return diffs
}

// conditionsEqual compares two condition slices after normalization.
// Conditions are compared independent of order: sorted by field name,
// then by values within each condition.
func conditionsEqual(a, b []RuleCondition) bool {
	if len(a) != len(b) {
		return false
	}
	na := normalizeConditions(a)
	nb := normalizeConditions(b)
	for i := range na {
		if na[i].Field != nb[i].Field {
			return false
		}
		if !sortedStringsEqual(na[i].Values, nb[i].Values) {
			return false
		}
		if !httpHeaderConfigEqual(na[i].HttpHeaderConfig, nb[i].HttpHeaderConfig) {
			return false
		}
		if !queryStringConfigEqual(na[i].QueryStringConfig, nb[i].QueryStringConfig) {
			return false
		}
	}
	return true
}

func normalizeConditions(conditions []RuleCondition) []RuleCondition {
	norm := make([]RuleCondition, len(conditions))
	copy(norm, conditions)
	for i := range norm {
		if len(norm[i].Values) > 0 {
			sorted := make([]string, len(norm[i].Values))
			copy(sorted, norm[i].Values)
			sort.Strings(sorted)
			norm[i].Values = sorted
		}
		if norm[i].HttpHeaderConfig != nil {
			hc := *norm[i].HttpHeaderConfig
			if len(hc.Values) > 0 {
				sorted := make([]string, len(hc.Values))
				copy(sorted, hc.Values)
				sort.Strings(sorted)
				hc.Values = sorted
			}
			norm[i].HttpHeaderConfig = &hc
		}
		if norm[i].QueryStringConfig != nil {
			qsc := *norm[i].QueryStringConfig
			if len(qsc.Values) > 0 {
				sorted := make([]QueryStringKV, len(qsc.Values))
				copy(sorted, qsc.Values)
				sort.Slice(sorted, func(a, b int) bool {
					if sorted[a].Key != sorted[b].Key {
						return sorted[a].Key < sorted[b].Key
					}
					return sorted[a].Value < sorted[b].Value
				})
				qsc.Values = sorted
			}
			norm[i].QueryStringConfig = &qsc
		}
	}
	sort.Slice(norm, func(i, j int) bool {
		return norm[i].Field < norm[j].Field
	})
	return norm
}

func httpHeaderConfigEqual(a, b *HttpHeaderConfig) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	if !strings.EqualFold(a.Name, b.Name) {
		return false
	}
	return sortedStringsEqual(a.Values, b.Values)
}

func queryStringConfigEqual(a, b *QueryStringConfig) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	if len(a.Values) != len(b.Values) {
		return false
	}
	for i := range a.Values {
		if a.Values[i].Key != b.Values[i].Key || a.Values[i].Value != b.Values[i].Value {
			return false
		}
	}
	return true
}

// actionsEqual compares two action slices after normalization.
func actionsEqual(a, b []RuleAction) bool {
	if len(a) != len(b) {
		return false
	}
	na := normalizeActions(a)
	nb := normalizeActions(b)
	for i := range na {
		if na[i].Type != nb[i].Type {
			return false
		}
		switch na[i].Type {
		case "forward":
			if !forwardEqual(na[i], nb[i]) {
				return false
			}
		case "redirect":
			if !redirectEqual(na[i].RedirectConfig, nb[i].RedirectConfig) {
				return false
			}
		case "fixed-response":
			if !fixedResponseEqual(na[i].FixedResponseConfig, nb[i].FixedResponseConfig) {
				return false
			}
		}
	}
	return true
}

func normalizeActions(actions []RuleAction) []RuleAction {
	norm := make([]RuleAction, len(actions))
	copy(norm, actions)
	// Assign order if missing
	for i := range norm {
		if norm[i].Order == 0 {
			norm[i].Order = i + 1
		}
	}
	sort.Slice(norm, func(i, j int) bool {
		return norm[i].Order < norm[j].Order
	})
	// Normalize forward configs: sort target groups by ARN
	for i := range norm {
		if norm[i].ForwardConfig != nil {
			fc := *norm[i].ForwardConfig
			tgs := make([]WeightedTargetGroup, len(fc.TargetGroups))
			copy(tgs, fc.TargetGroups)
			sort.Slice(tgs, func(a, b int) bool {
				return tgs[a].TargetGroupArn < tgs[b].TargetGroupArn
			})
			fc.TargetGroups = tgs
			norm[i].ForwardConfig = &fc
		}
	}
	return norm
}

func forwardEqual(a, b RuleAction) bool {
	// Simple forward (single target group)
	if a.ForwardConfig == nil && b.ForwardConfig == nil {
		return a.TargetGroupArn == b.TargetGroupArn
	}
	if a.ForwardConfig == nil || b.ForwardConfig == nil {
		return false
	}
	if len(a.ForwardConfig.TargetGroups) != len(b.ForwardConfig.TargetGroups) {
		return false
	}
	for i := range a.ForwardConfig.TargetGroups {
		if a.ForwardConfig.TargetGroups[i].TargetGroupArn != b.ForwardConfig.TargetGroups[i].TargetGroupArn {
			return false
		}
		if !weightsEquivalent(a.ForwardConfig.TargetGroups[i].Weight, b.ForwardConfig.TargetGroups[i].Weight) {
			return false
		}
	}
	if !stickinessEqual(a.ForwardConfig.Stickiness, b.ForwardConfig.Stickiness) {
		return false
	}
	return true
}

func weightsEquivalent(a, b int) bool {
	normalizeWeight := func(w int) int {
		if w == 0 {
			return 1
		}
		return w
	}
	return normalizeWeight(a) == normalizeWeight(b)
}

func stickinessEqual(a, b *ForwardStickiness) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.Enabled == b.Enabled && a.Duration == b.Duration
}

func redirectEqual(a, b *RedirectConfig) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.Protocol == b.Protocol && a.Host == b.Host && a.Port == b.Port &&
		a.Path == b.Path && a.Query == b.Query && a.StatusCode == b.StatusCode
}

func fixedResponseEqual(a, b *FixedResponseConfig) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.StatusCode == b.StatusCode && a.ContentType == b.ContentType && a.MessageBody == b.MessageBody
}

func computeTagDiffs(desired, observed map[string]string) []FieldDiffEntry {
	var diffs []FieldDiffEntry
	fd := filterPraxisTags(desired)
	fo := filterPraxisTags(observed)
	for key, value := range fd {
		if oldValue, ok := fo[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: nil, NewValue: value})
		} else if oldValue != value {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: oldValue, NewValue: value})
		}
	}
	for key, value := range fo {
		if _, ok := fd[key]; !ok {
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

func sortedStringsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
