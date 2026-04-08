// Package ec2 implements the Praxis driver for AWS EC2 instances.
//
// This driver manages the full lifecycle of EC2 instances: provisioning (RunInstances),
// in-place updates (instance type, security groups, monitoring, tags), drift detection
// and correction during reconciliation, import of existing instances, and termination.
//
// The driver is registered as a Restate Virtual Object named "EC2Instance". Each object
// key corresponds to one managed EC2 instance. State is persisted in Restate's durable
// key-value store under a single "state" key containing the full EC2InstanceState struct.
package ec2

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate Virtual Object name used to register this driver.
// All handler invocations (Provision, Import, Delete, Reconcile, GetStatus, GetOutputs)
// are dispatched to the Virtual Object identified by this name.
const ServiceName = "EC2Instance"

// EC2InstanceSpec is the user-declared desired state for an EC2 instance.
// It maps directly to the "spec" section of a Praxis resource manifest.
//
// Immutable fields (cannot be changed after creation — changes are noted as informational diffs):
//   - ImageId:  the AMI to launch from (maps to RunInstances ImageId)
//   - SubnetId: the VPC subnet placement (maps to RunInstances SubnetId)
//   - KeyName:  the SSH key pair name (maps to RunInstances KeyName)
//
// Mutable fields (can be updated in-place on an existing instance):
//   - InstanceType:       EC2 instance type (e.g. "t3.micro"); requires stop/modify/start cycle
//   - SecurityGroupIds:   VPC security groups attached to the instance's primary ENI
//   - Monitoring:         detailed CloudWatch monitoring (true=enabled, false=basic)
//   - Tags:               user-defined key-value tags (praxis:-prefixed tags are reserved)
//
// Other fields:
//   - Account:            Praxis account alias used to resolve AWS credentials
//   - Region:             AWS region for the instance (required)
//   - UserData:           base64-encoded user data script passed at launch
//   - IamInstanceProfile: IAM instance profile name or ARN attached at launch
//   - RootVolume:         root EBS volume configuration (size, type, encryption) — set at launch only
//   - ManagedKey:         unique idempotency key (typically metadata.name) used to prevent duplicate instances
type EC2InstanceSpec struct {
	Account            string            `json:"account,omitempty"`
	Region             string            `json:"region"`
	ImageId            string            `json:"imageId"`
	InstanceType       string            `json:"instanceType"`
	KeyName            string            `json:"keyName,omitempty"`
	SubnetId           string            `json:"subnetId"`
	SecurityGroupIds   []string          `json:"securityGroupIds,omitempty"`
	UserData           string            `json:"userData,omitempty"`
	IamInstanceProfile string            `json:"iamInstanceProfile,omitempty"`
	RootVolume         *RootVolumeSpec   `json:"rootVolume,omitempty"`
	Monitoring         bool              `json:"monitoring"`
	Tags               map[string]string `json:"tags,omitempty"`
	ManagedKey         string            `json:"managedKey,omitempty"`
}

// RootVolumeSpec configures the root EBS volume attached to the instance at launch.
// These values are immutable after instance creation — changes require instance replacement.
//   - SizeGiB:    volume size in GiB (maps to RunInstances BlockDeviceMappings EBS VolumeSize)
//   - VolumeType: EBS volume type, e.g. "gp3", "io1" (maps to EBS VolumeType)
//   - Encrypted:  whether the volume is encrypted at rest (maps to EBS Encrypted)
type RootVolumeSpec struct {
	SizeGiB    int32  `json:"sizeGiB"`
	VolumeType string `json:"volumeType"`
	Encrypted  bool   `json:"encrypted"`
}

// EC2InstanceOutputs are the user-facing outputs produced after provisioning or import.
// These are returned to callers and stored in state for downstream reference (e.g. stack outputs).
type EC2InstanceOutputs struct {
	InstanceId       string `json:"instanceId"`                // AWS-assigned instance ID (i-xxxxx)
	PrivateIpAddress string `json:"privateIpAddress"`          // Primary private IPv4 address
	PublicIpAddress  string `json:"publicIpAddress,omitempty"` // Public IPv4 (empty if none assigned)
	PrivateDnsName   string `json:"privateDnsName"`            // Internal DNS hostname
	ARN              string `json:"arn"`                       // Instance ARN (currently not populated by EC2 describe)
	State            string `json:"state"`                     // EC2 instance state: pending, running, stopped, etc.
	SubnetId         string `json:"subnetId"`                  // VPC subnet the instance is placed in
	VpcId            string `json:"vpcId"`                     // VPC the instance belongs to
}

// ObservedState captures the live AWS-side configuration of an EC2 instance.
// Populated by DescribeInstance and used for drift detection by comparing against EC2InstanceSpec.
// Includes both mutable fields (used for drift) and immutable fields (informational only).
type ObservedState struct {
	InstanceId          string            `json:"instanceId"`
	ImageId             string            `json:"imageId"`
	InstanceType        string            `json:"instanceType"`
	KeyName             string            `json:"keyName"`
	SubnetId            string            `json:"subnetId"`
	VpcId               string            `json:"vpcId"`
	SecurityGroupIds    []string          `json:"securityGroupIds"`   // Sorted for deterministic comparison
	IamInstanceProfile  string            `json:"iamInstanceProfile"` // Extracted profile name (not the full ARN)
	Monitoring          bool              `json:"monitoring"`         // true if detailed monitoring is enabled
	State               string            `json:"state"`              // EC2 state: running, stopped, terminated, etc.
	PrivateIpAddress    string            `json:"privateIpAddress"`
	PublicIpAddress     string            `json:"publicIpAddress"`
	PrivateDnsName      string            `json:"privateDnsName"`
	RootVolumeType      string            `json:"rootVolumeType"`      // EBS type fetched via DescribeVolumes
	RootVolumeSizeGiB   int32             `json:"rootVolumeSizeGiB"`   // Root volume size from DescribeVolumes
	RootVolumeEncrypted bool              `json:"rootVolumeEncrypted"` // Encryption status from DescribeVolumes
	Tags                map[string]string `json:"tags"`                // All tags including praxis:-prefixed ones
}

// EC2InstanceState is the single atomic state object persisted in Restate's K/V store
// under the drivers.StateKey ("state") for each Virtual Object key.
//
// This struct is the source of truth for the driver's view of the resource. It includes:
//   - Desired:            the last spec submitted by the user via Provision
//   - Observed:           the last AWS-side snapshot from DescribeInstance
//   - Outputs:            the user-facing outputs derived from Observed
//   - Status:             lifecycle status (Provisioning, Ready, Error, Deleting, Deleted)
//   - Mode:               managed (full control) or observed (read-only drift reporting)
//   - Error:              human-readable error message (set when Status=Error)
//   - Generation:         monotonically increasing counter incremented on each Provision/Import
//   - LastReconcile:      RFC3339 timestamp of the last completed reconcile loop
//   - ReconcileScheduled: guard preventing duplicate reconcile timers from being enqueued
type EC2InstanceState struct {
	Desired            EC2InstanceSpec      `json:"desired"`
	Observed           ObservedState        `json:"observed"`
	Outputs            EC2InstanceOutputs   `json:"outputs"`
	Status             types.ResourceStatus `json:"status"`
	Mode               types.Mode           `json:"mode"`
	Error              string               `json:"error,omitempty"`
	Generation         int64                `json:"generation"`
	LastReconcile      string               `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                 `json:"reconcileScheduled"`
	LateInitDone       bool                 `json:"lateInitDone,omitempty"`
}
