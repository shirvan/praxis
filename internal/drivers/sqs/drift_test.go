package sqs

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHasDrift_NoDrift(t *testing.T) {
	desired := SQSQueueSpec{
		VisibilityTimeout:             30,
		MessageRetentionPeriod:        345600,
		MaximumMessageSize:            262144,
		ReceiveMessageWaitTimeSeconds: 5,
		SqsManagedSseEnabled:          true,
		Tags:                          map[string]string{"env": "dev"},
	}
	observed := ObservedState{
		VisibilityTimeout:             30,
		MessageRetentionPeriod:        345600,
		MaximumMessageSize:            262144,
		ReceiveMessageWaitTimeSeconds: 5,
		SqsManagedSseEnabled:          true,
		Tags:                          map[string]string{"env": "dev"},
	}
	assert.False(t, HasDrift(desired, observed))
}

func TestHasDrift_RedrivePolicy(t *testing.T) {
	assert.True(t, HasDrift(
		SQSQueueSpec{RedrivePolicy: &RedrivePolicy{DeadLetterTargetArn: "arn:new", MaxReceiveCount: 3}},
		ObservedState{RedrivePolicy: &RedrivePolicy{DeadLetterTargetArn: "arn:old", MaxReceiveCount: 3}},
	))
}

func TestHasDrift_KMSAndFIFO(t *testing.T) {
	desired := SQSQueueSpec{
		FifoQueue:                    true,
		KmsMasterKeyId:               "alias/new",
		KmsDataKeyReusePeriodSeconds: 300,
		ContentBasedDeduplication:    true,
		DeduplicationScope:           "messageGroup",
		FifoThroughputLimit:          "perMessageGroupId",
	}
	observed := ObservedState{
		FifoQueue:                    true,
		KmsMasterKeyId:               "alias/old",
		KmsDataKeyReusePeriodSeconds: 300,
		ContentBasedDeduplication:    false,
		DeduplicationScope:           "queue",
		FifoThroughputLimit:          "perQueue",
	}
	assert.True(t, HasDrift(desired, observed))
}

func TestComputeFieldDiffs_Tags(t *testing.T) {
	diffs := ComputeFieldDiffs(
		SQSQueueSpec{Tags: map[string]string{"env": "prod"}},
		ObservedState{Tags: map[string]string{"env": "dev", "praxis:managed-key": "key"}},
	)
	assert.Len(t, diffs, 1)
	assert.Equal(t, "tags", diffs[0].Path)
	assert.Equal(t, map[string]string{"env": "dev"}, diffs[0].OldValue)
	assert.Equal(t, map[string]string{"env": "prod"}, diffs[0].NewValue)
}

func TestRedrivePolicyEqual(t *testing.T) {
	assert.True(t, redrivePolicyEqual(nil, nil))
	assert.False(t, redrivePolicyEqual(&RedrivePolicy{DeadLetterTargetArn: "arn", MaxReceiveCount: 1}, nil))
	assert.True(t, redrivePolicyEqual(
		&RedrivePolicy{DeadLetterTargetArn: "arn", MaxReceiveCount: 1},
		&RedrivePolicy{DeadLetterTargetArn: "arn", MaxReceiveCount: 1},
	))
}
