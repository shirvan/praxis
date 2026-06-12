// Package ssmparameter implements the Praxis driver for AWS SSM Parameter Store parameters.
//
// This file defines the spec, outputs, observed-state, and reconciliation-state
// types that flow through the driver lifecycle (Provision → Reconcile → Delete).
// The spec is the user's desired configuration; the observed state is read from
// AWS Systems Manager; the driver state couples both together with status tracking.
package ssmparameter

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate Virtual Object service name used to register the AWS SSM Parameter driver.
const ServiceName = "SSMParameter"

// SSMParameterSpec declares the user's desired configuration for an SSM parameter.
// Fields are validated before any AWS call and mapped to AWS Systems Manager API inputs.
type SSMParameterSpec struct {
	Account        string            `json:"account,omitempty"`
	Region         string            `json:"region"`
	ParameterName  string            `json:"parameterName"`
	Type           string            `json:"type,omitempty"`
	Value          string            `json:"value"`
	Description    string            `json:"description,omitempty"`
	Tier           string            `json:"tier,omitempty"`
	KmsKeyID       string            `json:"kmsKeyId,omitempty"`
	AllowedPattern string            `json:"allowedPattern,omitempty"`
	DataType       string            `json:"dataType,omitempty"`
	Tags           map[string]string `json:"tags,omitempty"`
	ManagedKey     string            `json:"managedKey,omitempty"`
}

// SSMParameterOutputs holds the values produced after provisioning an SSM parameter.
// The parameter value is intentionally excluded: deployment outputs flow into
// deployment state and expression hydration, and SecureString values must not
// leak there. Downstream resources that need the value should use the ssm://
// resolver, which tracks sensitivity for plan masking.
type SSMParameterOutputs struct {
	ARN           string `json:"arn"`
	ParameterName string `json:"parameterName"`
	Type          string `json:"type"`
	Version       int64  `json:"version"`
	Tier          string `json:"tier"`
	DataType      string `json:"dataType,omitempty"`
}

// ObservedState captures the live configuration of an SSM parameter as read
// from AWS Systems Manager. It is compared against the spec during drift
// detection. Value holds the decrypted value for SecureString parameters so
// value drift is detectable; it never leaves the driver as an output.
type ObservedState struct {
	ARN            string            `json:"arn"`
	ParameterName  string            `json:"parameterName"`
	Type           string            `json:"type"`
	Value          string            `json:"value"`
	Description    string            `json:"description,omitempty"`
	Tier           string            `json:"tier,omitempty"`
	KmsKeyID       string            `json:"kmsKeyId,omitempty"`
	AllowedPattern string            `json:"allowedPattern,omitempty"`
	DataType       string            `json:"dataType,omitempty"`
	Version        int64             `json:"version"`
	Tags           map[string]string `json:"tags,omitempty"`
}

// SSMParameterState is the single atomic state object persisted under drivers.StateKey
// in the Restate K/V store. It combines desired spec, observed state,
// outputs, lifecycle status, mode (managed/observed), error message,
// generation counter, and reconciliation scheduling metadata.
type SSMParameterState struct {
	Desired            SSMParameterSpec     `json:"desired"`
	Observed           ObservedState        `json:"observed"`
	Outputs            SSMParameterOutputs  `json:"outputs"`
	Status             types.ResourceStatus `json:"status"`
	Mode               types.Mode           `json:"mode"`
	Error              string               `json:"error,omitempty"`
	Generation         int64                `json:"generation"`
	LastReconcile      string               `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
