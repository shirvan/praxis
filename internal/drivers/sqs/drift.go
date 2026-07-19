// Package sqs – drift.go
//
// This file implements drift detection for AWS SQS Queue.
// HasDrift compares the desired spec against the observed state from AWS and
// returns true when any mutable field has diverged. ComputeFieldDiffs produces
// a structured list of individual field changes for plan output and logging.
// Immutable fields (those that require resource replacement) are annotated.
package sqs

import (
	"maps"

	"github.com/shirvan/praxis/internal/drivers"
)

// HasDrift compares the desired SQSQueue spec against the observed
// state from AWS and returns true if any mutable field has diverged.
// It is called during Reconcile to decide whether drift correction is needed.
func HasDrift(desired SQSQueueSpec, observed ObservedState) bool {
	if desired.QueueName != observed.QueueName || desired.FifoQueue != observed.FifoQueue {
		return true
	}
	if observedRegion := regionFromQueueARN(observed.QueueArn); observedRegion != "" && desired.Region != observedRegion {
		return true
	}
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
	return !drivers.TagsMatch(desired.Tags, observed.Tags)
}

// ComputeFieldDiffs produces a structured list of individual field changes
// between the desired spec and observed state. Used for plan output, CLI
// display, and audit logging. Immutable field changes are clearly annotated.
func ComputeFieldDiffs(desired SQSQueueSpec, observed ObservedState) []drivers.FieldDiff {
	var diffs []drivers.FieldDiff
	if desired.Region != "" {
		observedRegion := regionFromQueueARN(observed.QueueArn)
		if observedRegion != "" && desired.Region != observedRegion {
			diffs = append(diffs, drivers.FieldDiff{
				Path: "spec.region (immutable, requires replacement)", OldValue: observedRegion, NewValue: desired.Region,
			})
		}
	}
	if desired.QueueName != observed.QueueName {
		diffs = append(diffs, drivers.FieldDiff{
			Path: "spec.queueName (immutable, requires replacement)", OldValue: observed.QueueName, NewValue: desired.QueueName,
		})
	}
	if desired.FifoQueue != observed.FifoQueue {
		diffs = append(diffs, drivers.FieldDiff{
			Path: "spec.fifoQueue (immutable, requires replacement)", OldValue: observed.FifoQueue, NewValue: desired.FifoQueue,
		})
	}
	addIntDiff := func(path string, desiredValue, observedValue int) {
		if desiredValue != observedValue {
			diffs = append(diffs, drivers.FieldDiff{Path: path, OldValue: observedValue, NewValue: desiredValue})
		}
	}

	addIntDiff("spec.visibilityTimeout", desired.VisibilityTimeout, observed.VisibilityTimeout)
	addIntDiff("spec.messageRetentionPeriod", desired.MessageRetentionPeriod, observed.MessageRetentionPeriod)
	addIntDiff("spec.maximumMessageSize", desired.MaximumMessageSize, observed.MaximumMessageSize)
	addIntDiff("spec.delaySeconds", desired.DelaySeconds, observed.DelaySeconds)
	addIntDiff("spec.receiveMessageWaitTimeSeconds", desired.ReceiveMessageWaitTimeSeconds, observed.ReceiveMessageWaitTimeSeconds)

	if !redrivePolicyEqual(desired.RedrivePolicy, observed.RedrivePolicy) {
		diffs = append(diffs, drivers.FieldDiff{
			Path:     "spec.redrivePolicy",
			OldValue: observed.RedrivePolicy,
			NewValue: desired.RedrivePolicy,
		})
	}

	if desired.KmsMasterKeyId != observed.KmsMasterKeyId {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.kmsMasterKeyId", OldValue: observed.KmsMasterKeyId, NewValue: desired.KmsMasterKeyId})
	}
	if desired.KmsMasterKeyId != "" {
		addIntDiff("spec.kmsDataKeyReusePeriodSeconds", desired.KmsDataKeyReusePeriodSeconds, observed.KmsDataKeyReusePeriodSeconds)
	} else if desired.SqsManagedSseEnabled != observed.SqsManagedSseEnabled {
		diffs = append(diffs, drivers.FieldDiff{Path: "spec.sqsManagedSseEnabled", OldValue: observed.SqsManagedSseEnabled, NewValue: desired.SqsManagedSseEnabled})
	}

	if desired.FifoQueue {
		if desired.ContentBasedDeduplication != observed.ContentBasedDeduplication {
			diffs = append(diffs, drivers.FieldDiff{Path: "spec.contentBasedDeduplication", OldValue: observed.ContentBasedDeduplication, NewValue: desired.ContentBasedDeduplication})
		}
		if desired.DeduplicationScope != "" && desired.DeduplicationScope != observed.DeduplicationScope {
			diffs = append(diffs, drivers.FieldDiff{Path: "spec.deduplicationScope", OldValue: observed.DeduplicationScope, NewValue: desired.DeduplicationScope})
		}
		if desired.FifoThroughputLimit != "" && desired.FifoThroughputLimit != observed.FifoThroughputLimit {
			diffs = append(diffs, drivers.FieldDiff{Path: "spec.fifoThroughputLimit", OldValue: observed.FifoThroughputLimit, NewValue: desired.FifoThroughputLimit})
		}
	}

	if !drivers.TagsMatch(desired.Tags, observed.Tags) {
		diffs = append(diffs, drivers.FieldDiff{Path: "tags", OldValue: drivers.FilterPraxisTags(observed.Tags), NewValue: drivers.FilterPraxisTags(desired.Tags)})
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

func managedTags(user map[string]string, managedKey string) map[string]string {
	merged := make(map[string]string, len(user)+1)
	maps.Copy(merged, drivers.FilterPraxisTags(user))
	if managedKey != "" {
		merged["praxis:managed-key"] = managedKey
	}
	return merged
}
