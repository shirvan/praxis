// Package sqs implements the Praxis driver for AWS SQS Queue resources.
//
// This file defines the spec, outputs, observed-state, and reconciliation-state
// types that flow through the driver lifecycle (Provision → Reconcile → Delete).
// The spec is the user's desired configuration; the observed state is read from
// Amazon Simple Queue Service (SQS); the driver state couples both together with status tracking.
package sqs

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate Virtual Object service name used to register the AWS SQS Queue driver.
const ServiceName = "SQSQueue"

// SQSQueueSpec declares the user's desired configuration for a AWS SQS Queue.
// Fields are validated before any AWS call and mapped to Amazon Simple Queue Service (SQS) API inputs.
type SQSQueueSpec struct {
	Account                       string            `json:"account,omitempty"`
	Region                        string            `json:"region"`
	QueueName                     string            `json:"queueName"`
	FifoQueue                     bool              `json:"fifoQueue"`
	VisibilityTimeout             int               `json:"visibilityTimeout"`
	MessageRetentionPeriod        int               `json:"messageRetentionPeriod"`
	MaximumMessageSize            int               `json:"maximumMessageSize"`
	DelaySeconds                  int               `json:"delaySeconds"`
	ReceiveMessageWaitTimeSeconds int               `json:"receiveMessageWaitTimeSeconds"`
	RedrivePolicy                 *RedrivePolicy    `json:"redrivePolicy,omitempty"`
	SqsManagedSseEnabled          bool              `json:"sqsManagedSseEnabled"`
	KmsMasterKeyId                string            `json:"kmsMasterKeyId,omitempty"`
	KmsDataKeyReusePeriodSeconds  int               `json:"kmsDataKeyReusePeriodSeconds"`
	ContentBasedDeduplication     bool              `json:"contentBasedDeduplication"`
	DeduplicationScope            string            `json:"deduplicationScope,omitempty"`
	FifoThroughputLimit           string            `json:"fifoThroughputLimit,omitempty"`
	Tags                          map[string]string `json:"tags,omitempty"`
	ManagedKey                    string            `json:"managedKey,omitempty"`
}

// RedrivePolicy configures dead-letter queue redrive for an SQS queue.
type RedrivePolicy struct {
	DeadLetterTargetArn string `json:"deadLetterTargetArn"`
	MaxReceiveCount     int    `json:"maxReceiveCount"`
}

// SQSQueueOutputs holds the values produced after provisioning a AWS SQS Queue.
// These outputs are stored in Restate K/V and can be referenced by
// downstream resources (e.g. listeners referencing an ALB ARN).
type SQSQueueOutputs struct {
	QueueUrl  string `json:"queueUrl"`
	QueueArn  string `json:"queueArn"`
	QueueName string `json:"queueName"`
}

// ObservedState captures the live configuration of a AWS SQS Queue
// as read from Amazon Simple Queue Service (SQS). It is compared against the spec
// during drift detection.
type ObservedState struct {
	QueueUrl                      string            `json:"queueUrl"`
	QueueArn                      string            `json:"queueArn"`
	QueueName                     string            `json:"queueName"`
	FifoQueue                     bool              `json:"fifoQueue"`
	VisibilityTimeout             int               `json:"visibilityTimeout"`
	MessageRetentionPeriod        int               `json:"messageRetentionPeriod"`
	MaximumMessageSize            int               `json:"maximumMessageSize"`
	DelaySeconds                  int               `json:"delaySeconds"`
	ReceiveMessageWaitTimeSeconds int               `json:"receiveMessageWaitTimeSeconds"`
	RedrivePolicy                 *RedrivePolicy    `json:"redrivePolicy,omitempty"`
	SqsManagedSseEnabled          bool              `json:"sqsManagedSseEnabled"`
	KmsMasterKeyId                string            `json:"kmsMasterKeyId,omitempty"`
	KmsDataKeyReusePeriodSeconds  int               `json:"kmsDataKeyReusePeriodSeconds"`
	ContentBasedDeduplication     bool              `json:"contentBasedDeduplication"`
	DeduplicationScope            string            `json:"deduplicationScope,omitempty"`
	FifoThroughputLimit           string            `json:"fifoThroughputLimit,omitempty"`
	ApproximateNumberOfMessages   int64             `json:"approximateNumberOfMessages"`
	CreatedTimestamp              string            `json:"createdTimestamp"`
	LastModifiedTimestamp         string            `json:"lastModifiedTimestamp"`
	Tags                          map[string]string `json:"tags"`
}

// SQSQueueState is the single atomic state object persisted under drivers.StateKey
// in the Restate K/V store. It combines desired spec, observed state,
// outputs, lifecycle status, mode (managed/observed), error message,
// generation counter, and reconciliation scheduling metadata.
type SQSQueueState struct {
	Desired            SQSQueueSpec         `json:"desired"`
	Observed           ObservedState        `json:"observed"`
	Outputs            SQSQueueOutputs      `json:"outputs"`
	Status             types.ResourceStatus `json:"status"`
	Mode               types.Mode           `json:"mode"`
	Error              string               `json:"error,omitempty"`
	Generation         int64                `json:"generation"`
	LastReconcile      string               `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
