// Package ebs implements the Praxis EBS volume driver as a Restate Virtual Object.
// It manages the full lifecycle of Amazon EBS volumes: provisioning, importing,
// reconcile (drift detection and correction), deletion, and status/output queries.
//
// EBS volumes are block-level storage devices that can be attached to EC2 instances.
// Unlike S3 buckets, volumes are zonal (tied to an Availability Zone) and support
// in-place modification of type, size, IOPS, and throughput (subject to cooldown).
package ebs

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate Virtual Object name for EBS volumes.
// This is the user-facing API surface (e.g., curl .../EBSVolume/key/Provision).
const ServiceName = "EBSVolume"

// EBSVolumeSpec is the desired state for an EBS volume.
// Fields map to the #EBSVolume CUE schema in schemas/aws/ec2/ebs.cue.
// This is what Core sends to the Provision handler after hydrating
// output expressions and resolving SSM references.
type EBSVolumeSpec struct {
	// Account is the operator-defined AWS account name used for this volume.
	Account string `json:"account,omitempty"`

	// Region is the AWS region where the volume will be created.
	Region string `json:"region"`

	// AvailabilityZone is the AZ where the volume will be created (e.g., "us-east-1a").
	// This is an immutable field — volumes cannot be moved between AZs after creation.
	AvailabilityZone string `json:"availabilityZone"`

	// VolumeType is the EBS volume type: "gp2", "gp3", "io1", "io2", "st1", "sc1", "standard".
	// Defaults to "gp3" if empty.
	VolumeType string `json:"volumeType"`

	// SizeGiB is the volume size in GiB. Defaults to 20 if zero.
	// EBS volumes can only be enlarged, never shrunk.
	SizeGiB int32 `json:"sizeGiB"`

	// Iops is the provisioned IOPS for io1/io2/gp3 volumes. Zero means use AWS defaults.
	Iops int32 `json:"iops,omitempty"`

	// Throughput is the provisioned throughput in MiB/s for gp3 volumes. Zero means use AWS defaults.
	Throughput int32 `json:"throughput,omitempty"`

	// Encrypted controls whether the volume uses EBS encryption.
	// This is an immutable field — encryption cannot be toggled after creation.
	Encrypted bool `json:"encrypted"`

	// KmsKeyId is the KMS key ARN/ID for encryption. Empty means use the default aws/ebs key.
	KmsKeyId string `json:"kmsKeyId,omitempty"`

	// SnapshotId is an optional snapshot to create the volume from.
	SnapshotId string `json:"snapshotId,omitempty"`

	// Tags are key-value pairs applied to the volume for cost allocation and tracking.
	Tags map[string]string `json:"tags,omitempty"`

	// ManagedKey is Praxis's unique identifier tag for ownership tracking.
	// Used to detect duplicate volumes and prevent double-provisioning.
	ManagedKey string `json:"managedKey,omitempty"`
}

// EBSVolumeOutputs are the user-facing outputs produced after provisioning.
// Dependent resources reference these values via output expressions
// (e.g., "${ resources.data_volume.outputs.volumeId }").
type EBSVolumeOutputs struct {
	// VolumeId is the AWS-assigned volume identifier (e.g., "vol-0123456789abcdef0").
	VolumeId string `json:"volumeId"`

	// ARN is the Amazon Resource Name for the volume.
	ARN string `json:"arn,omitempty"`

	// AvailabilityZone is the AZ the volume resides in.
	AvailabilityZone string `json:"availabilityZone"`

	// State is the current EC2 volume state ("creating", "available", "in-use", "deleting", etc.).
	State string `json:"state"`

	// SizeGiB is the actual volume size in GiB.
	SizeGiB int32 `json:"sizeGiB"`

	// VolumeType is the actual volume type.
	VolumeType string `json:"volumeType"`

	// Encrypted indicates whether the volume is encrypted.
	Encrypted bool `json:"encrypted"`
}

// ObservedState captures the actual AWS-side configuration of a volume
// as returned by ec2:DescribeVolumes. Used for drift comparison.
type ObservedState struct {
	VolumeId         string            `json:"volumeId"`
	AvailabilityZone string            `json:"availabilityZone"`
	VolumeType       string            `json:"volumeType"`
	SizeGiB          int32             `json:"sizeGiB"`
	Iops             int32             `json:"iops"`
	Throughput       int32             `json:"throughput"`
	Encrypted        bool              `json:"encrypted"`
	KmsKeyId         string            `json:"kmsKeyId"`
	State            string            `json:"state"`
	SnapshotId       string            `json:"snapshotId"`
	Tags             map[string]string `json:"tags"`
}

// EBSVolumeState is the single atomic state object stored under drivers.StateKey.
// All fields are written together in one restate.Set() call, ensuring no
// torn state after crash-during-replay.
type EBSVolumeState struct {
	// Desired is the user's declared configuration.
	Desired EBSVolumeSpec `json:"desired"`

	// Observed is the actual configuration in AWS, populated during reconcile.
	Observed ObservedState `json:"observed"`

	// Outputs are the values produced after provisioning (volume ID, ARN, etc.).
	Outputs EBSVolumeOutputs `json:"outputs"`

	// Status is the current lifecycle status of this resource.
	Status types.ResourceStatus `json:"status"`

	// Mode is Managed (drift corrected) or Observed (drift reported only).
	Mode types.Mode `json:"mode"`

	// Error holds the error message when Status is Error.
	Error string `json:"error,omitempty"`

	// Generation is a monotonically increasing counter incremented on every
	// Provision and Import call. Enables conflict detection and spec tracking.
	Generation int64 `json:"generation"`

	// LastReconcile is the RFC3339 timestamp of the last completed reconciliation.
	LastReconcile string `json:"lastReconcile,omitempty"`

	// ReconcileScheduled prevents timer fan-out. At most one pending
	// reconcile exists per object at any time.
	ReconcileScheduled bool `json:"reconcileScheduled"`
}
