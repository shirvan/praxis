package sqspolicy

import "github.com/shirvan/praxis/pkg/types"

const ServiceName = "SQSQueuePolicy"

type SQSQueuePolicySpec struct {
	Account   string `json:"account,omitempty"`
	Region    string `json:"region"`
	QueueName string `json:"queueName"`
	Policy    string `json:"policy"`
}

type SQSQueuePolicyOutputs struct {
	QueueUrl  string `json:"queueUrl"`
	QueueArn  string `json:"queueArn"`
	QueueName string `json:"queueName"`
}

type ObservedState struct {
	QueueUrl string `json:"queueUrl"`
	QueueArn string `json:"queueArn"`
	Policy   string `json:"policy"`
}

type SQSQueuePolicyState struct {
	Desired            SQSQueuePolicySpec    `json:"desired"`
	Observed           ObservedState         `json:"observed"`
	Outputs            SQSQueuePolicyOutputs `json:"outputs"`
	Status             types.ResourceStatus  `json:"status"`
	Mode               types.Mode            `json:"mode"`
	Error              string                `json:"error,omitempty"`
	Generation         int64                 `json:"generation"`
	LastReconcile      string                `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                  `json:"reconcileScheduled"`
}
