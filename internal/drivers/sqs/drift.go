// Package sqs – drift.go
//
// This file implements drift detection for AWS SQS Queue.
// HasDrift compares the desired spec against the observed state from AWS and
// returns true when any mutable field has diverged. ComputeFieldDiffs produces
// a structured list of individual field changes for plan output and logging.
// Immutable fields (those that require resource replacement) are annotated.
package sqs

import (
	"fmt"
	"maps"
	"strings"
)

// FieldDiffEntry represents a single field-level difference between the desired
// spec and the observed state. Path uses dot notation (e.g. "spec.name");
// immutable fields are annotated with "(immutable, requires replacement)".
type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
}

// HasDrift compares the desired SQSQueue spec against the observed
// state from AWS and returns true if any mutable field has diverged.
// It is called during Reconcile to decide whether drift correction is needed.
func HasDrift(desired SQSQueueSpec, observed ObservedState) bool {
	if desired.VisibilityTimeout != observed.VisibilityTimeout {
		return true
	}
	if desired.MessageRetentionPeriod != observed.MessageRetentionPeriod {
		return true
	}
	if desired.MaximumMessageSize != observed.MaximumMessageSize {
		return true
	}
	if desired.DelaySeconds != observed.DelaySeconds {
		return true
	}
	if desired.ReceiveMessageWaitTimeSeconds != observed.ReceiveMessageWaitTimeSeconds {
		return true
	}
	if !redrivePolicyEqual(desired.RedrivePolicy, observed.RedrivePolicy) {
		return true
	}
	if desired.KmsMasterKeyId != observed.KmsMasterKeyId {
		return true
	}
	if desired.KmsMasterKeyId != "" {
		if desired.KmsDataKeyReusePeriodSeconds != observed.KmsDataKeyReusePeriodSeconds {
			return true
		}
	} else if desired.SqsManagedSseEnabled != observed.SqsManagedSseEnabled {
		return true
	}
	if desired.FifoQueue {
		if desired.ContentBasedDeduplication != observed.ContentBasedDeduplication {
			return true
		}
		if desired.DeduplicationScope != "" && desired.DeduplicationScope != observed.DeduplicationScope {
			return true
		}
		if desired.FifoThroughputLimit != "" && desired.FifoThroughputLimit != observed.FifoThroughputLimit {
			return true
		}
	}
	return !tagsMatch(desired.Tags, observed.Tags)
}

// ComputeFieldDiffs produces a structured list of individual field changes
// between the desired spec and observed state. Used for plan output, CLI
// display, and audit logging. Immutable field changes are clearly annotated.
func ComputeFieldDiffs(desired SQSQueueSpec, observed ObservedState) []FieldDiffEntry {
	var diffs []FieldDiffEntry
	addIntDiff := func(path string, desiredValue, observedValue int) {
		if desiredValue != observedValue {
			diffs = append(diffs, FieldDiffEntry{Path: path, OldValue: observedValue, NewValue: desiredValue})
		}
	}

	addIntDiff("spec.visibilityTimeout", desired.VisibilityTimeout, observed.VisibilityTimeout)
	addIntDiff("spec.messageRetentionPeriod", desired.MessageRetentionPeriod, observed.MessageRetentionPeriod)
	addIntDiff("spec.maximumMessageSize", desired.MaximumMessageSize, observed.MaximumMessageSize)
	addIntDiff("spec.delaySeconds", desired.DelaySeconds, observed.DelaySeconds)
	addIntDiff("spec.receiveMessageWaitTimeSeconds", desired.ReceiveMessageWaitTimeSeconds, observed.ReceiveMessageWaitTimeSeconds)

	if !redrivePolicyEqual(desired.RedrivePolicy, observed.RedrivePolicy) {
		diffs = append(diffs, FieldDiffEntry{
			Path:     "spec.redrivePolicy",
			OldValue: observed.RedrivePolicy,
			NewValue: desired.RedrivePolicy,
		})
	}

	if desired.KmsMasterKeyId != observed.KmsMasterKeyId {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.kmsMasterKeyId", OldValue: observed.KmsMasterKeyId, NewValue: desired.KmsMasterKeyId})
	}
	if desired.KmsMasterKeyId != "" {
		addIntDiff("spec.kmsDataKeyReusePeriodSeconds", desired.KmsDataKeyReusePeriodSeconds, observed.KmsDataKeyReusePeriodSeconds)
	} else if desired.SqsManagedSseEnabled != observed.SqsManagedSseEnabled {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.sqsManagedSseEnabled", OldValue: observed.SqsManagedSseEnabled, NewValue: desired.SqsManagedSseEnabled})
	}

	if desired.FifoQueue {
		if desired.ContentBasedDeduplication != observed.ContentBasedDeduplication {
			diffs = append(diffs, FieldDiffEntry{Path: "spec.contentBasedDeduplication", OldValue: observed.ContentBasedDeduplication, NewValue: desired.ContentBasedDeduplication})
		}
		if desired.DeduplicationScope != "" && desired.DeduplicationScope != observed.DeduplicationScope {
			diffs = append(diffs, FieldDiffEntry{Path: "spec.deduplicationScope", OldValue: observed.DeduplicationScope, NewValue: desired.DeduplicationScope})
		}
		if desired.FifoThroughputLimit != "" && desired.FifoThroughputLimit != observed.FifoThroughputLimit {
			diffs = append(diffs, FieldDiffEntry{Path: "spec.fifoThroughputLimit", OldValue: observed.FifoThroughputLimit, NewValue: desired.FifoThroughputLimit})
		}
	}

	if !tagsMatch(desired.Tags, observed.Tags) {
		diffs = append(diffs, FieldDiffEntry{Path: "tags", OldValue: filterPraxisTags(observed.Tags), NewValue: filterPraxisTags(desired.Tags)})
	}

	return diffs
}

func redrivePolicyEqual(a, b *RedrivePolicy) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.DeadLetterTargetArn == b.DeadLetterTargetArn && a.MaxReceiveCount == b.MaxReceiveCount
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
	maps.Copy(merged, user)
	maps.Copy(merged, system)
	return merged
}

func formatManagedKeyConflict(managedKey, queueURL string) error {
	return fmt.Errorf("queue %q in this region is already managed by Praxis (queueUrl: %s); remove the existing resource or use a different metadata.name", managedKey, queueURL)
}
