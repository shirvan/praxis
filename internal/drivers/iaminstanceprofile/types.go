package iaminstanceprofile

import "github.com/shirvan/praxis/pkg/types"

const ServiceName = "IAMInstanceProfile"

type IAMInstanceProfileSpec struct {
	Account             string            `json:"account,omitempty"`
	Path                string            `json:"path"`
	InstanceProfileName string            `json:"instanceProfileName"`
	RoleName            string            `json:"roleName"`
	Tags                map[string]string `json:"tags,omitempty"`
}

type IAMInstanceProfileOutputs struct {
	Arn                 string `json:"arn"`
	InstanceProfileId   string `json:"instanceProfileId"`
	InstanceProfileName string `json:"instanceProfileName"`
}

type ObservedState struct {
	Arn                 string            `json:"arn"`
	InstanceProfileId   string            `json:"instanceProfileId"`
	InstanceProfileName string            `json:"instanceProfileName"`
	Path                string            `json:"path"`
	RoleName            string            `json:"roleName"`
	Tags                map[string]string `json:"tags"`
	CreateDate          string            `json:"createDate"`
}

type IAMInstanceProfileState struct {
	Desired            IAMInstanceProfileSpec    `json:"desired"`
	Observed           ObservedState             `json:"observed"`
	Outputs            IAMInstanceProfileOutputs `json:"outputs"`
	Status             types.ResourceStatus      `json:"status"`
	Mode               types.Mode                `json:"mode"`
	Error              string                    `json:"error,omitempty"`
	Generation         int64                     `json:"generation"`
	LastReconcile      string                    `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                      `json:"reconcileScheduled"`
}
