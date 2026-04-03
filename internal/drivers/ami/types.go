// Package ami implements the Praxis driver for Amazon Machine Images (AMIs).
//
// This driver manages the full lifecycle of custom AMIs: creation from an EBS snapshot
// (RegisterImage) or by copying an existing AMI (CopyImage), in-place updates to mutable
// attributes (description, tags, launch permissions, deprecation schedule), drift detection
// and correction during reconciliation, import of existing AMIs, and deregistration.
//
// The driver is registered as a Restate Virtual Object named "AMI". Each object key
// corresponds to one managed AMI. State is persisted in Restate's durable K/V store.
package ami

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate Virtual Object name used to register the AMI driver.
const ServiceName = "AMI"

// AMISpec is the user-declared desired state for an AMI resource.
//
// Immutable fields (set at creation, cannot be changed without deregister+recreate):
//   - Name:    the AMI name (maps to RegisterImage/CopyImage Name)
//   - Source:  the creation method (from snapshot or copy from existing AMI)
//
// Mutable fields (can be updated in-place on an existing AMI):
//   - Description:       human-readable description (ModifyImageAttribute)
//   - Tags:              user-defined key-value tags
//   - LaunchPermissions: accounts and public visibility (ModifyImageAttribute launchPermission)
//   - Deprecation:       scheduled deprecation time (EnableImageDeprecation/DisableImageDeprecation)
//
// Other fields:
//   - Account:    Praxis account alias for AWS credential resolution
//   - Region:     AWS region (required)
//   - ManagedKey: unique key for idempotent lookup via praxis:managed-key tag
type AMISpec struct {
	Account           string            `json:"account,omitempty"`
	Region            string            `json:"region"`
	Name              string            `json:"name"`
	Description       string            `json:"description,omitempty"`
	Source            SourceSpec        `json:"source"`
	LaunchPermissions *LaunchPermsSpec  `json:"launchPermissions,omitempty"`
	Deprecation       *DeprecationSpec  `json:"deprecation,omitempty"`
	Tags              map[string]string `json:"tags,omitempty"`
	ManagedKey        string            `json:"managedKey,omitempty"`
}

// SourceSpec selects the AMI creation method. Exactly one of FromSnapshot or FromAMI must be set.
type SourceSpec struct {
	FromSnapshot *FromSnapshotSpec `json:"fromSnapshot,omitempty"`
	FromAMI      *FromAMISpec      `json:"fromAMI,omitempty"`
}

// FromSnapshotSpec creates an AMI by registering an existing EBS snapshot as the root device.
// Maps to the EC2 RegisterImage API.
type FromSnapshotSpec struct {
	SnapshotId         string `json:"snapshotId"`
	Architecture       string `json:"architecture"`
	VirtualizationType string `json:"virtualizationType"`
	RootDeviceName     string `json:"rootDeviceName"`
	VolumeType         string `json:"volumeType"`
	VolumeSize         int32  `json:"volumeSize,omitempty"`
	EnaSupport         *bool  `json:"enaSupport,omitempty"`
}

// FromAMISpec creates an AMI by copying an existing AMI (possibly cross-region).
// Maps to the EC2 CopyImage API. Optionally re-encrypts with a different KMS key.
type FromAMISpec struct {
	SourceImageId string `json:"sourceImageId"`
	SourceRegion  string `json:"sourceRegion,omitempty"`
	Encrypted     bool   `json:"encrypted,omitempty"`
	KmsKeyId      string `json:"kmsKeyId,omitempty"`
}

// LaunchPermsSpec controls which AWS accounts can launch instances from this AMI.
//   - AccountIds: explicit list of AWS account IDs granted launch permission
//   - Public:     if true, the AMI is shared publicly (group "all")
type LaunchPermsSpec struct {
	AccountIds []string `json:"accountIds,omitempty"`
	Public     bool     `json:"public"`
}

// DeprecationSpec schedules the AMI for deprecation at a specific time (RFC3339).
// After deprecation, the AMI is still usable but marked as deprecated in describe calls.
type DeprecationSpec struct {
	DeprecateAt string `json:"deprecateAt"`
}

// AMIOutputs are the user-facing outputs produced after provisioning or import.
type AMIOutputs struct {
	ImageId            string `json:"imageId"`
	Name               string `json:"name"`
	State              string `json:"state"`
	Architecture       string `json:"architecture"`
	VirtualizationType string `json:"virtualizationType"`
	RootDeviceName     string `json:"rootDeviceName"`
	OwnerId            string `json:"ownerId"`
	CreationDate       string `json:"creationDate"`
}

// ObservedState captures the live AWS-side configuration of an AMI.
// Populated by DescribeImage and used for drift detection.
type ObservedState struct {
	ImageId            string            `json:"imageId"`
	Name               string            `json:"name"`
	Description        string            `json:"description"`
	State              string            `json:"state"`
	Architecture       string            `json:"architecture"`
	VirtualizationType string            `json:"virtualizationType"`
	RootDeviceName     string            `json:"rootDeviceName"`
	OwnerId            string            `json:"ownerId"`
	CreationDate       string            `json:"creationDate"`
	Tags               map[string]string `json:"tags"`
	LaunchPermPublic   bool              `json:"launchPermPublic"`
	LaunchPermAccounts []string          `json:"launchPermAccounts,omitempty"`
	DeprecationTime    string            `json:"deprecationTime,omitempty"`
}

// AMIState is the atomic state object persisted in Restate's K/V store for each Virtual Object key.
// See EC2InstanceState for field semantics (same pattern across all drivers).
type AMIState struct {
	Desired            AMISpec              `json:"desired"`
	Observed           ObservedState        `json:"observed"`
	Outputs            AMIOutputs           `json:"outputs"`
	Status             types.ResourceStatus `json:"status"`
	Mode               types.Mode           `json:"mode"`
	Error              string               `json:"error,omitempty"`
	Generation         int64                `json:"generation"`
	LastReconcile      string               `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
