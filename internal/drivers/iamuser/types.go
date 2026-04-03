// Package iamuser implements the Praxis driver for AWS IAM Users.
// It manages the full lifecycle of IAM users including creation, update, deletion,
// drift detection, and periodic reconciliation. IAM users represent individual identities
// with their own credentials, group memberships, inline policies, and managed policy attachments.
package iamuser

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate virtual object name for IAM user driver instances.
const ServiceName = "IAMUser"

// IAMUserSpec defines the desired configuration for an AWS IAM user.
type IAMUserSpec struct {
	// Account is the AWS account alias or ID for credential resolution.
	Account string `json:"account,omitempty"`
	// Path is the IAM path prefix for the user. Mutable via UpdateUser.
	Path string `json:"path"`
	// UserName is the unique user name within the account. Required.
	UserName string `json:"userName"`
	// PermissionsBoundary is the ARN of a managed policy used as the permissions boundary.
	PermissionsBoundary string `json:"permissionsBoundary,omitempty"`
	// InlinePolicies maps policy names to JSON policy documents embedded directly on the user.
	InlinePolicies map[string]string `json:"inlinePolicies,omitempty"`
	// ManagedPolicyArns lists the ARNs of managed policies to attach to the user.
	ManagedPolicyArns []string `json:"managedPolicyArns,omitempty"`
	// Groups lists the IAM group names the user should belong to.
	Groups []string `json:"groups,omitempty"`
	// Tags are key-value metadata pairs. Tags prefixed with "praxis:" are reserved.
	Tags map[string]string `json:"tags,omitempty"`
}

// IAMUserOutputs contains the AWS-assigned identifiers for the user.
type IAMUserOutputs struct {
	Arn      string `json:"arn"`
	UserId   string `json:"userId"`
	UserName string `json:"userName"`
}

// ObservedState captures the full live state of an IAM user as read from the AWS API,
// including group memberships, policies, and permissions boundary.
type ObservedState struct {
	Arn                 string            `json:"arn"`
	UserId              string            `json:"userId"`
	UserName            string            `json:"userName"`
	Path                string            `json:"path"`
	PermissionsBoundary string            `json:"permissionsBoundary,omitempty"`
	InlinePolicies      map[string]string `json:"inlinePolicies,omitempty"`
	ManagedPolicyArns   []string          `json:"managedPolicyArns,omitempty"`
	Groups              []string          `json:"groups,omitempty"`
	Tags                map[string]string `json:"tags,omitempty"`
	CreateDate          string            `json:"createDate"`
}

// IAMUserState is the durable state persisted in Restate's virtual object storage.
type IAMUserState struct {
	Desired            IAMUserSpec          `json:"desired"`
	Observed           ObservedState        `json:"observed"`
	Outputs            IAMUserOutputs       `json:"outputs"`
	Status             types.ResourceStatus `json:"status"`
	Mode               types.Mode           `json:"mode"`
	Error              string               `json:"error,omitempty"`
	Generation         int64                `json:"generation"`
	LastReconcile      string               `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
