// Package iaminstanceprofile implements AWS IAM Instance Profile provider
// semantics for the shared Praxis lifecycle kernel.
package iaminstanceprofile

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
