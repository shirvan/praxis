// Package loggroup implements the Praxis driver for AWS CloudWatch Log Group resources.
//
// This file defines the spec, outputs, and observed-state types that flow
// through the driver lifecycle (Provision → Reconcile → Delete).
// The spec is the user's desired configuration; the observed state is read from
// Amazon CloudWatch Logs. Durable lifecycle state is owned by the generic kernel.
package loggroup

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
