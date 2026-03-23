package iampolicy

import "github.com/praxiscloud/praxis/pkg/types"

const ServiceName = "IAMPolicy"

type IAMPolicySpec struct {
	Account        string            `json:"account,omitempty"`
	Path           string            `json:"path"`
	PolicyName     string            `json:"policyName"`
	PolicyDocument string            `json:"policyDocument"`
	Description    string            `json:"description,omitempty"`
	Tags           map[string]string `json:"tags,omitempty"`
}

type IAMPolicyOutputs struct {
	Arn        string `json:"arn"`
	PolicyId   string `json:"policyId"`
	PolicyName string `json:"policyName"`
}

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
