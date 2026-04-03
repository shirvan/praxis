// Package snstopic – drift.go
//
// This file implements drift detection for AWS SNS Topic.
// HasDrift compares the desired spec against the observed state from AWS and
// returns true when any mutable field has diverged. ComputeFieldDiffs produces
// a structured list of individual field changes for plan output and logging.
// Immutable fields (those that require resource replacement) are annotated.
package snstopic

import (
	"encoding/json"
	"strings"
)

// FieldDiffEntry represents a single field-level change for plan output.
type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
}

// HasDrift returns true if the desired spec and observed state differ on mutable fields.
func HasDrift(desired SNSTopicSpec, observed ObservedState) bool {
	if desired.DisplayName != observed.DisplayName {
		return true
	}
	if !policiesEqual(desired.Policy, observed.Policy) {
		return true
	}
	if !policiesEqual(desired.DeliveryPolicy, observed.DeliveryPolicy) {
		return true
	}
	if desired.KmsMasterKeyId != observed.KmsMasterKeyId {
		return true
	}
	if desired.ContentBasedDeduplication != observed.ContentBasedDeduplication {
		return true
	}
	if !tagsMatch(desired.Tags, observed.Tags) {
		return true
	}
	return false
}

// ComputeFieldDiffs returns field-level differences for plan output.
func ComputeFieldDiffs(desired SNSTopicSpec, observed ObservedState) []FieldDiffEntry {
	var diffs []FieldDiffEntry

	if desired.DisplayName != observed.DisplayName {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.displayName",
			OldValue: observed.DisplayName,
			NewValue: desired.DisplayName,
		})
	}
	if !policiesEqual(desired.Policy, observed.Policy) {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.policy",
			OldValue: observed.Policy,
			NewValue: desired.Policy,
		})
	}
	if !policiesEqual(desired.DeliveryPolicy, observed.DeliveryPolicy) {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.deliveryPolicy",
			OldValue: observed.DeliveryPolicy,
			NewValue: desired.DeliveryPolicy,
		})
	}
	if desired.KmsMasterKeyId != observed.KmsMasterKeyId {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.kmsMasterKeyId",
			OldValue: observed.KmsMasterKeyId,
			NewValue: desired.KmsMasterKeyId,
		})
	}
	if desired.ContentBasedDeduplication != observed.ContentBasedDeduplication {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.contentBasedDeduplication",
			OldValue: observed.ContentBasedDeduplication,
			NewValue: desired.ContentBasedDeduplication,
		})
	}
	if !tagsMatch(desired.Tags, observed.Tags) {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "tags",
			OldValue: filterPraxisTags(observed.Tags),
			NewValue: filterPraxisTags(desired.Tags),
		})
	}

	return diffs
}

// policiesEqual compares two JSON policy strings semantically.
// Handles whitespace and key ordering differences.
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

func mergeTags(user, system map[string]string) map[string]string {
	merged := make(map[string]string, len(user)+len(system))
	for k, v := range user {
		merged[k] = v
	}
	for k, v := range system {
		merged[k] = v
	}
	return merged
}
