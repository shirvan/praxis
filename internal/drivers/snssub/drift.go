package snssub

import "encoding/json"

// FieldDiffEntry represents a single field-level change for plan output.
type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
}

// HasDrift returns true if the desired spec and observed state differ on mutable fields.
func HasDrift(desired SNSSubscriptionSpec, observed ObservedState) bool {
	if !policiesEqual(desired.FilterPolicy, observed.FilterPolicy) {
		return true
	}
	if desired.FilterPolicyScope != "" && desired.FilterPolicyScope != observed.FilterPolicyScope {
		return true
	}
	if desired.RawMessageDelivery != observed.RawMessageDelivery {
		return true
	}
	if !policiesEqual(desired.DeliveryPolicy, observed.DeliveryPolicy) {
		return true
	}
	if !policiesEqual(desired.RedrivePolicy, observed.RedrivePolicy) {
		return true
	}
	if desired.SubscriptionRoleArn != observed.SubscriptionRoleArn {
		return true
	}
	return false
}

// ComputeFieldDiffs returns field-level differences for plan output.
func ComputeFieldDiffs(desired SNSSubscriptionSpec, observed ObservedState) []FieldDiffEntry {
	var diffs []FieldDiffEntry

	if !policiesEqual(desired.FilterPolicy, observed.FilterPolicy) {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.filterPolicy",
			OldValue: observed.FilterPolicy,
			NewValue: desired.FilterPolicy,
		})
	}
	if desired.FilterPolicyScope != "" && desired.FilterPolicyScope != observed.FilterPolicyScope {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.filterPolicyScope",
			OldValue: observed.FilterPolicyScope,
			NewValue: desired.FilterPolicyScope,
		})
	}
	if desired.RawMessageDelivery != observed.RawMessageDelivery {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.rawMessageDelivery",
			OldValue: observed.RawMessageDelivery,
			NewValue: desired.RawMessageDelivery,
		})
	}
	if !policiesEqual(desired.DeliveryPolicy, observed.DeliveryPolicy) {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.deliveryPolicy",
			OldValue: observed.DeliveryPolicy,
			NewValue: desired.DeliveryPolicy,
		})
	}
	if !policiesEqual(desired.RedrivePolicy, observed.RedrivePolicy) {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.redrivePolicy",
			OldValue: observed.RedrivePolicy,
			NewValue: desired.RedrivePolicy,
		})
	}
	if desired.SubscriptionRoleArn != observed.SubscriptionRoleArn {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.subscriptionRoleArn",
			OldValue: observed.SubscriptionRoleArn,
			NewValue: desired.SubscriptionRoleArn,
		})
	}

	return diffs
}

// policiesEqual compares two JSON policy strings semantically.
func policiesEqual(a, b string) bool {
	if a == b {
		return true
	}
	if a == "" || b == "" {
		return false
	}
	var aObj, bObj interface{}
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
