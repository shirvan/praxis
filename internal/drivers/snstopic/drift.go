// Package snstopic – drift.go
//
// This file implements drift detection for AWS SNS Topic.
// HasDrift compares the desired spec against the observed state from AWS and
// returns true when any mutable field has diverged. ComputeFieldDiffs produces
// a structured list of individual field changes for plan output and logging.
// Immutable fields (those that require resource replacement) are annotated.
package snstopic

import (
	"bytes"
	"encoding/json"
	"maps"

	"github.com/shirvan/praxis/internal/drivers"
)

// FieldDiffEntry represents a single field-level change for plan output.
type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
}

// HasDrift returns true if the desired spec and observed state differ on mutable fields.
// Optional attributes (Policy, DeliveryPolicy, KmsMasterKeyId) are only checked when the
// desired value is non-empty; an empty desired value means "not managed by Praxis".
func HasDrift(desired SNSTopicSpec, observed ObservedState) bool {
	if desired.DisplayName != observed.DisplayName {
		return true
	}
	if desired.Policy != "" && !policiesEqual(desired.Policy, observed.Policy) {
		return true
	}
	if desired.DeliveryPolicy != "" && !policiesEqual(desired.DeliveryPolicy, observed.DeliveryPolicy) {
		return true
	}
	if desired.KmsMasterKeyId != "" && desired.KmsMasterKeyId != observed.KmsMasterKeyId {
		return true
	}
	if desired.ContentBasedDeduplication != observed.ContentBasedDeduplication {
		return true
	}
	if !drivers.TagsMatch(desired.Tags, observed.Tags) {
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
	if desired.Policy != "" && !policiesEqual(desired.Policy, observed.Policy) {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.policy",
			OldValue: observed.Policy,
			NewValue: desired.Policy,
		})
	}
	if desired.DeliveryPolicy != "" && !policiesEqual(desired.DeliveryPolicy, observed.DeliveryPolicy) {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.deliveryPolicy",
			OldValue: observed.DeliveryPolicy,
			NewValue: desired.DeliveryPolicy,
		})
	}
	if desired.KmsMasterKeyId != "" && desired.KmsMasterKeyId != observed.KmsMasterKeyId {
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
	if !drivers.TagsMatch(desired.Tags, observed.Tags) {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "tags",
			OldValue: drivers.FilterPraxisTags(observed.Tags),
			NewValue: drivers.FilterPraxisTags(desired.Tags),
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

func mergeTags(user, system map[string]string) map[string]string {
	merged := make(map[string]string, len(user)+len(system))
	maps.Copy(merged, user)
	maps.Copy(merged, system)
	return merged
}
