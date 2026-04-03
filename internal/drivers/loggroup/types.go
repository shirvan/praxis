// Package loggroup implements the Praxis driver for AWS CloudWatch Log Group resources.
//
// This file defines the spec, outputs, observed-state, and reconciliation-state
// types that flow through the driver lifecycle (Provision → Reconcile → Delete).
// The spec is the user's desired configuration; the observed state is read from
// Amazon CloudWatch Logs; the driver state couples both together with status tracking.
package loggroup

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate Virtual Object service name used to register the AWS CloudWatch Log Group driver.
const ServiceName = "LogGroup"

// LogGroupSpec declares the user's desired configuration for a AWS CloudWatch Log Group.
// Fields are validated before any AWS call and mapped to Amazon CloudWatch Logs API inputs.
type LogGroupSpec struct {
	Account         string            `json:"account,omitempty"`
	Region          string            `json:"region"`
	LogGroupName    string            `json:"logGroupName"`
	LogGroupClass   string            `json:"logGroupClass,omitempty"`
	RetentionInDays *int32            `json:"retentionInDays,omitempty"`
	KmsKeyID        string            `json:"kmsKeyId,omitempty"`
	Tags            map[string]string `json:"tags,omitempty"`
	ManagedKey      string            `json:"managedKey,omitempty"`
}

// LogGroupOutputs holds the values produced after provisioning a AWS CloudWatch Log Group.
// These outputs are stored in Restate K/V and can be referenced by
// downstream resources (e.g. listeners referencing an ALB ARN).
type LogGroupOutputs struct {
	ARN             string `json:"arn"`
	LogGroupName    string `json:"logGroupName"`
	LogGroupClass   string `json:"logGroupClass"`
	RetentionInDays int32  `json:"retentionInDays"`
	KmsKeyID        string `json:"kmsKeyId,omitempty"`
	CreationTime    int64  `json:"creationTime"`
	StoredBytes     int64  `json:"storedBytes"`
}

// ObservedState captures the live configuration of a AWS CloudWatch Log Group
// as read from Amazon CloudWatch Logs. It is compared against the spec
// during drift detection.
type ObservedState struct {
	ARN             string            `json:"arn"`
	LogGroupName    string            `json:"logGroupName"`
	LogGroupClass   string            `json:"logGroupClass"`
	RetentionInDays *int32            `json:"retentionInDays,omitempty"`
	KmsKeyID        string            `json:"kmsKeyId,omitempty"`
	CreationTime    int64             `json:"creationTime"`
	StoredBytes     int64             `json:"storedBytes"`
	Tags            map[string]string `json:"tags,omitempty"`
}

// LogGroupState is the single atomic state object persisted under drivers.StateKey
// in the Restate K/V store. It combines desired spec, observed state,
// outputs, lifecycle status, mode (managed/observed), error message,
// generation counter, and reconciliation scheduling metadata.
type LogGroupState struct {
	Desired            LogGroupSpec         `json:"desired"`
	Observed           ObservedState        `json:"observed"`
	Outputs            LogGroupOutputs      `json:"outputs"`
	Status             types.ResourceStatus `json:"status"`
	Mode               types.Mode           `json:"mode"`
	Error              string               `json:"error,omitempty"`
	Generation         int64                `json:"generation"`
	LastReconcile      string               `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
