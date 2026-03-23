package iamuser

import "github.com/shirvan/praxis/pkg/types"

const ServiceName = "IAMUser"

type IAMUserSpec struct {
	Account             string            `json:"account,omitempty"`
	Path                string            `json:"path"`
	UserName            string            `json:"userName"`
	PermissionsBoundary string            `json:"permissionsBoundary,omitempty"`
	InlinePolicies      map[string]string `json:"inlinePolicies,omitempty"`
	ManagedPolicyArns   []string          `json:"managedPolicyArns,omitempty"`
	Groups              []string          `json:"groups,omitempty"`
	Tags                map[string]string `json:"tags,omitempty"`
}

type IAMUserOutputs struct {
	Arn      string `json:"arn"`
	UserId   string `json:"userId"`
	UserName string `json:"userName"`
}

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
