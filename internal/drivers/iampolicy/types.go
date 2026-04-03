// Package iampolicy implements the Praxis driver for AWS IAM customer-managed policies.
// It manages the full lifecycle of IAM policies including creation, policy document versioning,
// deletion, drift detection, and periodic reconciliation. IAM policies define permission sets
// that can be attached to roles, users, and groups.
package iampolicy

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate virtual object name for IAM policy driver instances.
const ServiceName = "IAMPolicy"

// IAMPolicySpec defines the desired configuration for an AWS IAM customer-managed policy.
type IAMPolicySpec struct {
	// Account is the AWS account alias or ID for credential resolution.
	Account string `json:"account,omitempty"`
	// Path is the IAM path prefix for the policy. Immutable after creation.
	Path string `json:"path"`
	// PolicyName is the unique name of the policy within the account. Required.
	PolicyName string `json:"policyName"`
	// PolicyDocument is the JSON permissions policy document. Required. Updated via versioning.
	PolicyDocument string `json:"policyDocument"`
	// Description is a human-readable description. Immutable after creation.
	Description string `json:"description,omitempty"`
	// Tags are key-value metadata pairs. Tags prefixed with "praxis:" are reserved.
	Tags map[string]string `json:"tags,omitempty"`
}

// IAMPolicyOutputs contains the AWS-assigned identifiers for the policy.
type IAMPolicyOutputs struct {
	Arn        string `json:"arn"`
	PolicyId   string `json:"policyId"`
	PolicyName string `json:"policyName"`
}

// ObservedState captures the live AWS state of an IAM policy, including its current
// policy document (from the default version), attachment count, and version history.
type ObservedState struct {
	Arn              string            `json:"arn"`
	PolicyId         string            `json:"policyId"`
	PolicyName       string            `json:"policyName"`
	Path             string            `json:"path"`
	Description      string            `json:"description"`
	PolicyDocument   string            `json:"policyDocument"`
	DefaultVersionId string            `json:"defaultVersionId"`
	AttachmentCount  int32             `json:"attachmentCount"`
	Tags             map[string]string `json:"tags"`
	CreateDate       string            `json:"createDate"`
	UpdateDate       string            `json:"updateDate"`
}

// IAMPolicyState is the durable state persisted in Restate's virtual object storage.
// Tracks desired spec, observed AWS state, outputs, status, mode, error, and reconcile scheduling.
type IAMPolicyState struct {
	Desired            IAMPolicySpec        `json:"desired"`
	Observed           ObservedState        `json:"observed"`
	Outputs            IAMPolicyOutputs     `json:"outputs"`
	Status             types.ResourceStatus `json:"status"`
	Mode               types.Mode           `json:"mode"`
	Error              string               `json:"error,omitempty"`
	Generation         int64                `json:"generation"`
	LastReconcile      string               `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
