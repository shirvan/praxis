package iamrole

import "github.com/praxiscloud/praxis/pkg/types"

const ServiceName = "IAMRole"

type IAMRoleSpec struct {
	Account                  string            `json:"account,omitempty"`
	Path                     string            `json:"path"`
	RoleName                 string            `json:"roleName"`
	AssumeRolePolicyDocument string            `json:"assumeRolePolicyDocument"`
	Description              string            `json:"description,omitempty"`
	MaxSessionDuration       int32             `json:"maxSessionDuration"`
	PermissionsBoundary      string            `json:"permissionsBoundary,omitempty"`
	InlinePolicies           map[string]string `json:"inlinePolicies,omitempty"`
	ManagedPolicyArns        []string          `json:"managedPolicyArns,omitempty"`
	Tags                     map[string]string `json:"tags,omitempty"`
}

type IAMRoleOutputs struct {
	Arn      string `json:"arn"`
	RoleId   string `json:"roleId"`
	RoleName string `json:"roleName"`
}

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