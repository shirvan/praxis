package iamgroup

import "github.com/praxiscloud/praxis/pkg/types"

const ServiceName = "IAMGroup"

type IAMGroupSpec struct {
	Account           string            `json:"account,omitempty"`
	Path              string            `json:"path"`
	GroupName         string            `json:"groupName"`
	InlinePolicies    map[string]string `json:"inlinePolicies,omitempty"`
	ManagedPolicyArns []string          `json:"managedPolicyArns,omitempty"`
}

type IAMGroupOutputs struct {
	Arn       string `json:"arn"`
	GroupId   string `json:"groupId"`
	GroupName string `json:"groupName"`
}

type ObservedState struct {
	Arn               string            `json:"arn"`
	GroupId           string            `json:"groupId"`
	GroupName         string            `json:"groupName"`
	Path              string            `json:"path"`
	InlinePolicies    map[string]string `json:"inlinePolicies,omitempty"`
	ManagedPolicyArns []string          `json:"managedPolicyArns,omitempty"`
	CreateDate        string            `json:"createDate"`
}

type IAMGroupState struct {
	Desired            IAMGroupSpec         `json:"desired"`
	Observed           ObservedState        `json:"observed"`
	Outputs            IAMGroupOutputs      `json:"outputs"`
	Status             types.ResourceStatus `json:"status"`
	Mode               types.Mode           `json:"mode"`
	Error              string               `json:"error,omitempty"`
	Generation         int64                `json:"generation"`
	LastReconcile      string               `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
