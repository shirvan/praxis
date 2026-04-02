package sqs

import "github.com/shirvan/praxis/pkg/types"

const ServiceName = "SQSQueue"

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

type RedrivePolicy struct {
	DeadLetterTargetArn string `json:"deadLetterTargetArn"`
	MaxReceiveCount     int    `json:"maxReceiveCount"`
}

type SQSQueueOutputs struct {
	QueueUrl  string `json:"queueUrl"`
	QueueArn  string `json:"queueArn"`
	QueueName string `json:"queueName"`
}

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
