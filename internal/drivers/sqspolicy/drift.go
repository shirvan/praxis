package sqspolicy

import "encoding/json"

type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
}

func HasDrift(desired SQSQueuePolicySpec, observed ObservedState) bool {
	return !policiesEqual(desired.Policy, observed.Policy)
}

func ComputeFieldDiffs(desired SQSQueuePolicySpec, observed ObservedState) []FieldDiffEntry {
	if policiesEqual(desired.Policy, observed.Policy) {
		return nil
	}
	return []FieldDiffEntry{{Path: "spec.policy", OldValue: observed.Policy, NewValue: desired.Policy}}
}

func policiesEqual(a, b string) bool {
	if a == b {
		return true
	}
	if a == "" || b == "" {
		return false
	}
	var aObj any
	var bObj any
	if json.Unmarshal([]byte(a), &aObj) != nil {
		return a == b
	}
	if json.Unmarshal([]byte(b), &bObj) != nil {
		return a == b
	}
	aNorm, _ := json.Marshal(aObj)
	bNorm, _ := json.Marshal(bObj)
	return string(aNorm) == string(bNorm)
}
