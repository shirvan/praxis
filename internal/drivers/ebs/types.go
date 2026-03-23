package ebs

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate Virtual Object name for EBS volumes.
const ServiceName = "EBSVolume"

// EBSVolumeSpec is the desired state for an EBS volume.
type EBSVolumeSpec struct {
	Account          string            `json:"account,omitempty"`
	Region           string            `json:"region"`
	AvailabilityZone string            `json:"availabilityZone"`
	VolumeType       string            `json:"volumeType"`
	SizeGiB          int32             `json:"sizeGiB"`
	Iops             int32             `json:"iops,omitempty"`
	Throughput       int32             `json:"throughput,omitempty"`
	Encrypted        bool              `json:"encrypted"`
	KmsKeyId         string            `json:"kmsKeyId,omitempty"`
	SnapshotId       string            `json:"snapshotId,omitempty"`
	Tags             map[string]string `json:"tags,omitempty"`
	ManagedKey       string            `json:"managedKey,omitempty"`
}

// EBSVolumeOutputs are the user-facing outputs produced by the driver.
type EBSVolumeOutputs struct {
	VolumeId         string `json:"volumeId"`
	ARN              string `json:"arn,omitempty"`
	AvailabilityZone string `json:"availabilityZone"`
	State            string `json:"state"`
	SizeGiB          int32  `json:"sizeGiB"`
	VolumeType       string `json:"volumeType"`
	Encrypted        bool   `json:"encrypted"`
}

// ObservedState captures the current AWS-side configuration of a volume.
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

// EBSVolumeState is the single atomic state object stored for each object key.
type EBSVolumeState struct {
	Desired            EBSVolumeSpec        `json:"desired"`
	Observed           ObservedState        `json:"observed"`
	Outputs            EBSVolumeOutputs     `json:"outputs"`
	Status             types.ResourceStatus `json:"status"`
	Mode               types.Mode           `json:"mode"`
	Error              string               `json:"error,omitempty"`
	Generation         int64                `json:"generation"`
	LastReconcile      string               `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
