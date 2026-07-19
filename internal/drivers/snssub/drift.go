// Package snssub – drift.go
//
// This file implements drift detection for AWS SNS Subscription.
// HasDrift compares the desired spec against the observed state from AWS and
// returns true when any mutable field has diverged. ComputeFieldDiffs produces
// a structured list of individual field changes for plan output and logging.
// Immutable fields (those that require resource replacement) are annotated.
package snssub

import (
	"bytes"
	"encoding/json"
	"github.com/shirvan/praxis/internal/drivers"
)

// HasDrift includes immutable identity fields so Provision reaches the
// convergence guard and rejects an impossible in-place change.
func HasDrift(desired SNSSubscriptionSpec, observed ObservedState) bool {
	if desired.TopicArn != observed.TopicArn || desired.Protocol != observed.Protocol || desired.Endpoint != observed.Endpoint {
		return true
	}
	if !filterPoliciesEqual(desired.FilterPolicy, observed.FilterPolicy) {
		return true
	}
	if !filterPolicyScopesEqual(desired.FilterPolicyScope, observed.FilterPolicyScope) {
		return true
	}
	if desired.RawMessageDelivery != observed.RawMessageDelivery {
		return true
	}
	if !optionalPoliciesEqual(desired.DeliveryPolicy, observed.DeliveryPolicy) {
		return true
	}
	if !optionalPoliciesEqual(desired.RedrivePolicy, observed.RedrivePolicy) {
		return true
	}
	if desired.SubscriptionRoleArn != observed.SubscriptionRoleArn {
		return true
	}
	return false
}

// ComputeFieldDiffs returns field-level differences for plan output.
func ComputeFieldDiffs(desired SNSSubscriptionSpec, observed ObservedState) []drivers.FieldDiff {
	var diffs []drivers.FieldDiff
	if desired.TopicArn != observed.TopicArn {
		diffs = append(diffs, drivers.FieldDiff{
			Path: "spec.topicArn (immutable, requires replacement)", OldValue: observed.TopicArn, NewValue: desired.TopicArn,
		})
	}
	if desired.Protocol != observed.Protocol {
		diffs = append(diffs, drivers.FieldDiff{
			Path: "spec.protocol (immutable, requires replacement)", OldValue: observed.Protocol, NewValue: desired.Protocol,
		})
	}
	if desired.Endpoint != observed.Endpoint {
		diffs = append(diffs, drivers.FieldDiff{
			Path: "spec.endpoint (immutable, requires replacement)", OldValue: observed.Endpoint, NewValue: desired.Endpoint,
		})
	}

	if !filterPoliciesEqual(desired.FilterPolicy, observed.FilterPolicy) {
		diffs = append(diffs, drivers.FieldDiff{
			Path:     "spec.filterPolicy",
			OldValue: observed.FilterPolicy,
			NewValue: desired.FilterPolicy,
		})
	}
	if !filterPolicyScopesEqual(desired.FilterPolicyScope, observed.FilterPolicyScope) {
		diffs = append(diffs, drivers.FieldDiff{
			Path:     "spec.filterPolicyScope",
			OldValue: observed.FilterPolicyScope,
			NewValue: desired.FilterPolicyScope,
		})
	}
	if desired.RawMessageDelivery != observed.RawMessageDelivery {
		diffs = append(diffs, drivers.FieldDiff{
			Path:     "spec.rawMessageDelivery",
			OldValue: observed.RawMessageDelivery,
			NewValue: desired.RawMessageDelivery,
		})
	}
	if !optionalPoliciesEqual(desired.DeliveryPolicy, observed.DeliveryPolicy) {
		diffs = append(diffs, drivers.FieldDiff{
			Path:     "spec.deliveryPolicy",
			OldValue: observed.DeliveryPolicy,
			NewValue: desired.DeliveryPolicy,
		})
	}
	if !optionalPoliciesEqual(desired.RedrivePolicy, observed.RedrivePolicy) {
		diffs = append(diffs, drivers.FieldDiff{
			Path:     "spec.redrivePolicy",
			OldValue: observed.RedrivePolicy,
			NewValue: desired.RedrivePolicy,
		})
	}
	if desired.SubscriptionRoleArn != observed.SubscriptionRoleArn {
		diffs = append(diffs, drivers.FieldDiff{
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
	var aObj, bObj any
	if json.Unmarshal([]byte(a), &aObj) != nil {
		return a == b
	}
	if json.Unmarshal([]byte(b), &bObj) != nil {
		return a == b
	}
	aNorm, _ := json.Marshal(aObj)
	bNorm, _ := json.Marshal(bObj)
	return bytes.Equal(aNorm, bNorm)
}

// filterPoliciesEqual treats both the empty string and an empty JSON object as
// the absence of a filter policy. AWS documents SetSubscriptionAttributes with
// "{}" as the removal operation and may subsequently report that representation.
func filterPoliciesEqual(a, b string) bool {
	if isEmptyJSONObject(a) && isEmptyJSONObject(b) {
		return true
	}
	return policiesEqual(a, b)
}

// optionalPoliciesEqual applies the greenfield desired-state contract for
// optional JSON policies: omission means the policy must be absent. SNS may
// report a removed policy either as a missing/empty value or as an empty JSON
// object, so both provider representations are equivalent to desired absence.
func optionalPoliciesEqual(a, b string) bool {
	if isEmptyJSONObject(a) && isEmptyJSONObject(b) {
		return true
	}
	return policiesEqual(a, b)
}

func isEmptyJSONObject(policy string) bool {
	if policy == "" {
		return true
	}
	var decoded map[string]any
	return json.Unmarshal([]byte(policy), &decoded) == nil && len(decoded) == 0
}

// filterPolicyScopesEqual treats an omitted scope as the documented SNS
// default, MessageAttributes. MessageBody is therefore drift when the desired
// field is omitted, rather than an unmanaged provider value.
func filterPolicyScopesEqual(a, b string) bool {
	normalize := func(scope string) string {
		if scope == "" {
			return "MessageAttributes"
		}
		return scope
	}
	return normalize(a) == normalize(b)
}
