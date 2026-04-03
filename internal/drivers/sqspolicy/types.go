// Package sqspolicy implements the Praxis driver for AWS SQS Queue Policy resources.
//
// This file defines the spec, outputs, observed-state, and reconciliation-state
// types that flow through the driver lifecycle (Provision → Reconcile → Delete).
// The spec is the user's desired configuration; the observed state is read from
// Amazon Simple Queue Service (SQS); the driver state couples both together with status tracking.
package sqspolicy

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate Virtual Object service name used to register the AWS SQS Queue Policy driver.
const ServiceName = "SQSQueuePolicy"

// SQSQueuePolicySpec declares the user's desired configuration for a AWS SQS Queue Policy.
// Fields are validated before any AWS call and mapped to Amazon Simple Queue Service (SQS) API inputs.
type SQSQueuePolicySpec struct {
	Account   string `json:"account,omitempty"`
	Region    string `json:"region"`
	QueueName string `json:"queueName"`
	Policy    string `json:"policy"`
}

// SQSQueuePolicyOutputs holds the values produced after provisioning a AWS SQS Queue Policy.
// These outputs are stored in Restate K/V and can be referenced by
// downstream resources (e.g. listeners referencing an ALB ARN).
type SQSQueuePolicyOutputs struct {
	QueueUrl  string `json:"queueUrl"`
	QueueArn  string `json:"queueArn"`
	QueueName string `json:"queueName"`
}

// ObservedState captures the live configuration of a AWS SQS Queue Policy
// as read from Amazon Simple Queue Service (SQS). It is compared against the spec
// during drift detection.
type ObservedState struct {
	QueueUrl string `json:"queueUrl"`
	QueueArn string `json:"queueArn"`
	Policy   string `json:"policy"`
}

// SQSQueuePolicyState is the single atomic state object persisted under drivers.StateKey
// in the Restate K/V store. It combines desired spec, observed state,
// outputs, lifecycle status, mode (managed/observed), error message,
// generation counter, and reconciliation scheduling metadata.
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
