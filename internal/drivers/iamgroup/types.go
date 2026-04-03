// Package iamgroup implements the Restate virtual-object driver for AWS IAM Groups.
// It manages the lifecycle of IAM groups including path, inline policies, and
// managed policy attachments via the create-or-converge pattern with drift detection.
package iamgroup

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate virtual object service name used to register and address this driver.
const ServiceName = "IAMGroup"

// IAMGroupSpec defines the desired state of an IAM group.
type IAMGroupSpec struct {
	Account           string            `json:"account,omitempty"`           // AWS account alias resolved via the auth service.
	Path              string            `json:"path"`                        // IAM path prefix for the group (default "/").
	GroupName         string            `json:"groupName"`                   // Name of the IAM group (required).
	InlinePolicies    map[string]string `json:"inlinePolicies,omitempty"`    // Map of policy-name to JSON policy document.
	ManagedPolicyArns []string          `json:"managedPolicyArns,omitempty"` // ARNs of managed policies to attach.
}

// IAMGroupOutputs holds the AWS-assigned identifiers returned after provisioning.
type IAMGroupOutputs struct {
	Arn       string `json:"arn"`
	GroupId   string `json:"groupId"`
	GroupName string `json:"groupName"`
}

// ObservedState captures the last-known live state of the IAM group from AWS.
type ObservedState struct {
	Arn               string            `json:"arn"`
	GroupId           string            `json:"groupId"`
	GroupName         string            `json:"groupName"`
	Path              string            `json:"path"`
	InlinePolicies    map[string]string `json:"inlinePolicies,omitempty"`
	ManagedPolicyArns []string          `json:"managedPolicyArns,omitempty"`
	CreateDate        string            `json:"createDate"`
}

// IAMGroupState is the full persisted state of this virtual-object, stored via restate.Set.
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
