// Package iamrole implements the Praxis driver for AWS IAM Roles.
// It manages the full lifecycle of IAM roles including creation, update,
// deletion, drift detection, and periodic reconciliation. IAM roles are
// the primary mechanism for granting AWS service and cross-account access
// via assume-role trust policies, inline policies, and managed policy attachments.
package iamrole

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate virtual object name used to register and address
// IAM role driver instances. Each IAM role is keyed by its Praxis resource key.
const ServiceName = "IAMRole"

// IAMRoleSpec defines the desired configuration for an AWS IAM role.
// Fields map directly to the AWS IAM CreateRole / UpdateRole API parameters.
type IAMRoleSpec struct {
	// Account is the AWS account alias or ID used to resolve credentials via the auth service.
	Account string `json:"account,omitempty"`
	// Path is the IAM path prefix for the role (e.g., "/service-roles/"). Immutable after creation.
	Path string `json:"path"`
	// RoleName is the unique name of the IAM role within the account. Required.
	RoleName string `json:"roleName"`
	// AssumeRolePolicyDocument is the JSON trust policy that defines which principals can assume this role. Required.
	AssumeRolePolicyDocument string `json:"assumeRolePolicyDocument"`
	// Description is a human-readable description of the role's purpose.
	Description string `json:"description,omitempty"`
	// MaxSessionDuration is the maximum session duration (in seconds) for assumed role sessions. Defaults to 3600.
	MaxSessionDuration int32 `json:"maxSessionDuration"`
	// PermissionsBoundary is the ARN of a managed policy used as the permissions boundary for the role.
	PermissionsBoundary string `json:"permissionsBoundary,omitempty"`
	// InlinePolicies maps policy names to their JSON policy documents, embedded directly in the role.
	InlinePolicies map[string]string `json:"inlinePolicies,omitempty"`
	// ManagedPolicyArns lists the ARNs of AWS managed or customer-managed policies to attach to the role.
	ManagedPolicyArns []string `json:"managedPolicyArns,omitempty"`
	// Tags are key-value metadata pairs applied to the role. Tags prefixed with "praxis:" are reserved.
	Tags map[string]string `json:"tags,omitempty"`
}

// IAMRoleOutputs contains the AWS-assigned identifiers returned after provisioning or importing an IAM role.
// These outputs are available for cross-resource references (e.g., instance profiles, policies).
type IAMRoleOutputs struct {
	// Arn is the Amazon Resource Name uniquely identifying this role globally.
	Arn string `json:"arn"`
	// RoleId is the AWS-generated unique identifier for the role (e.g., "AROA...").
	RoleId string `json:"roleId"`
	// RoleName is the human-readable name of the role.
	RoleName string `json:"roleName"`
}

// ObservedState captures the full live state of an IAM role as read from the AWS API.
// This is compared against the desired spec during drift detection and reconciliation.
type ObservedState struct {
	Arn                      string            `json:"arn"`
	RoleId                   string            `json:"roleId"`
	RoleName                 string            `json:"roleName"`
	Path                     string            `json:"path"`
	AssumeRolePolicyDocument string            `json:"assumeRolePolicyDocument"`
	Description              string            `json:"description"`
	MaxSessionDuration       int32             `json:"maxSessionDuration"`
	PermissionsBoundary      string            `json:"permissionsBoundary,omitempty"`
	InlinePolicies           map[string]string `json:"inlinePolicies,omitempty"`
	ManagedPolicyArns        []string          `json:"managedPolicyArns,omitempty"`
	Tags                     map[string]string `json:"tags,omitempty"`
	CreateDate               string            `json:"createDate"`
}

// IAMRoleState is the durable state persisted in Restate's virtual object storage.
// It tracks the desired spec, last observed AWS state, computed outputs, lifecycle status,
// management mode (managed vs observed), error state, generation counter, and reconcile scheduling.
type IAMRoleState struct {
	Desired            IAMRoleSpec          `json:"desired"`
	Observed           ObservedState        `json:"observed"`
	Outputs            IAMRoleOutputs       `json:"outputs"`
	Status             types.ResourceStatus `json:"status"`
	Mode               types.Mode           `json:"mode"`
	Error              string               `json:"error,omitempty"`
	Generation         int64                `json:"generation"`
	LastReconcile      string               `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
