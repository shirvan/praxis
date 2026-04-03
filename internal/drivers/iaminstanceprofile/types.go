// Package iaminstanceprofile implements the Restate virtual-object driver for AWS IAM Instance Profiles.
// It manages the lifecycle of instance profiles including role association (single role only),
// tags, and an immutable path via the create-or-converge pattern with drift detection.
package iaminstanceprofile

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate virtual object service name used to register and address this driver.
const ServiceName = "IAMInstanceProfile"

// IAMInstanceProfileSpec defines the desired state of an IAM instance profile.
type IAMInstanceProfileSpec struct {
	Account             string            `json:"account,omitempty"`   // AWS account alias resolved via the auth service.
	Path                string            `json:"path"`                // IAM path prefix (immutable after creation).
	InstanceProfileName string            `json:"instanceProfileName"` // Name of the instance profile (required).
	RoleName            string            `json:"roleName"`            // IAM role to associate (exactly one role, required).
	Tags                map[string]string `json:"tags,omitempty"`      // User-managed tags ("praxis:"-prefixed tags excluded from drift).
}

// IAMInstanceProfileOutputs holds the AWS-assigned identifiers returned after provisioning.
type IAMInstanceProfileOutputs struct {
	Arn                 string `json:"arn"`
	InstanceProfileId   string `json:"instanceProfileId"`
	InstanceProfileName string `json:"instanceProfileName"`
}

// ObservedState captures the last-known live state of the instance profile from AWS.
type ObservedState struct {
	Arn                 string            `json:"arn"`
	InstanceProfileId   string            `json:"instanceProfileId"`
	InstanceProfileName string            `json:"instanceProfileName"`
	Path                string            `json:"path"`
	RoleName            string            `json:"roleName"`
	Tags                map[string]string `json:"tags"`
	CreateDate          string            `json:"createDate"`
}

// IAMInstanceProfileState is the full persisted state of this virtual-object.
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
