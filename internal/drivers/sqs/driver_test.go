package sqs

import (
	"testing"

	"github.com/shirvan/praxis/pkg/types"
	"github.com/stretchr/testify/assert"
)

func TestServiceName(t *testing.T) {
	drv := NewSQSQueueDriver(nil)
	assert.Equal(t, ServiceName, drv.ServiceName())
}

func TestSpecFromObserved(t *testing.T) {
	obs := ObservedState{
		QueueArn:                      "arn:aws:sqs:us-east-1:123456789012:orders.fifo",
		QueueName:                     "orders.fifo",
		FifoQueue:                     true,
		VisibilityTimeout:             30,
		MessageRetentionPeriod:        345600,
		MaximumMessageSize:            262144,
		DelaySeconds:                  0,
		ReceiveMessageWaitTimeSeconds: 10,
		RedrivePolicy:                 &RedrivePolicy{DeadLetterTargetArn: "arn:aws:sqs:us-east-1:123456789012:dlq", MaxReceiveCount: 5},
		SqsManagedSseEnabled:          true,
		ContentBasedDeduplication:     true,
		DeduplicationScope:            "messageGroup",
		FifoThroughputLimit:           "perMessageGroupId",
		Tags:                          map[string]string{"env": "prod", "praxis:managed-key": "us-east-1~orders.fifo"},
	}

	spec := specFromObserved(obs, types.ImportRef{Account: "prod"})
	assert.Equal(t, "prod", spec.Account)
	assert.Equal(t, "us-east-1", spec.Region)
	assert.Equal(t, "orders.fifo", spec.QueueName)
	assert.True(t, spec.FifoQueue)
	assert.True(t, spec.ContentBasedDeduplication)
	assert.Equal(t, map[string]string{"env": "prod"}, spec.Tags)
}

func TestDefaultImportMode(t *testing.T) {
	assert.Equal(t, types.ModeObserved, defaultImportMode(""))
	assert.Equal(t, types.ModeManaged, defaultImportMode(types.ModeManaged))
}

func TestValidateSpec(t *testing.T) {
	assert.Error(t, validateSpec(SQSQueueSpec{Region: "us-east-1", QueueName: "orders.fifo"}))
	assert.Error(t, validateSpec(SQSQueueSpec{Region: "us-east-1", QueueName: "orders", KmsMasterKeyId: "alias/key", SqsManagedSseEnabled: true}))
	assert.NoError(t, validateSpec(SQSQueueSpec{Region: "us-east-1", QueueName: "orders", SqsManagedSseEnabled: true}))
}
