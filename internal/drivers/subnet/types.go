// Package subnet implements the Praxis driver for AWS VPC Subnets.
//
// A Subnet is a range of IP addresses in a VPC, scoped to a single
// Availability Zone. Instances are launched into subnets. CidrBlock,
// AvailabilityZone, and VpcId are immutable after creation; only
// MapPublicIpOnLaunch and tags can be corrected via drift reconciliation.
package subnet

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate Virtual Object name used to register this driver.
const ServiceName = "Subnet"

// SubnetSpec is the user-declared desired state for a Subnet.
type SubnetSpec struct {
	Account             string            `json:"account,omitempty"`    // AWS account alias resolved to credentials.
	Region              string            `json:"region"`               // AWS region.
	VpcId               string            `json:"vpcId"`                // VPC that owns this subnet (immutable).
	CidrBlock           string            `json:"cidrBlock"`            // IPv4 CIDR range for the subnet (immutable).
	AvailabilityZone    string            `json:"availabilityZone"`     // AZ like "us-east-1a" (immutable).
	MapPublicIpOnLaunch bool              `json:"mapPublicIpOnLaunch"`  // Auto-assign public IPv4 on instance launch (mutable, maps to ModifySubnetAttribute).
	Tags                map[string]string `json:"tags,omitempty"`       // AWS resource tags.
	ManagedKey          string            `json:"managedKey,omitempty"` // Ownership key as praxis:managed-key tag.
}

// SubnetOutputs holds AWS-assigned identifiers after provisioning.
type SubnetOutputs struct {
	SubnetId            string `json:"subnetId"`            // AWS-assigned subnet ID (subnet-xxxx).
	ARN                 string `json:"arn,omitempty"`       // Synthesized ARN.
	VpcId               string `json:"vpcId"`               // Owning VPC.
	CidrBlock           string `json:"cidrBlock"`           // Subnet's CIDR block.
	AvailabilityZone    string `json:"availabilityZone"`    // AZ name (e.g. "us-east-1a").
	AvailabilityZoneId  string `json:"availabilityZoneId"`  // AZ ID (e.g. "use1-az1"), stable across accounts.
	MapPublicIpOnLaunch bool   `json:"mapPublicIpOnLaunch"` // Current public IP auto-assignment setting.
	State               string `json:"state"`               // Subnet state from AWS ("available", "pending").
	OwnerId             string `json:"ownerId"`             // AWS account ID.
	AvailableIpCount    int    `json:"availableIpCount"`    // Number of available IPs in the CIDR.
}

// ObservedState captures the live AWS configuration of a Subnet.
type ObservedState struct {
	SubnetId            string            `json:"subnetId"`
	VpcId               string            `json:"vpcId"`
	CidrBlock           string            `json:"cidrBlock"`
	AvailabilityZone    string            `json:"availabilityZone"`
	AvailabilityZoneId  string            `json:"availabilityZoneId"`
	MapPublicIpOnLaunch bool              `json:"mapPublicIpOnLaunch"`
	State               string            `json:"state"`
	OwnerId             string            `json:"ownerId"`
	AvailableIpCount    int               `json:"availableIpCount"`
	Tags                map[string]string `json:"tags"`
}

// SubnetState is the single atomic state object stored under drivers.StateKey.
type SubnetState struct {
	Desired            SubnetSpec           `json:"desired"`                 // User-declared target configuration.
	Observed           ObservedState        `json:"observed"`                // Last-known AWS state.
	Outputs            SubnetOutputs        `json:"outputs"`                 // Stable identifiers returned to callers.
	Status             types.ResourceStatus `json:"status"`                  // Lifecycle status.
	Mode               types.Mode           `json:"mode"`                    // Managed or Observed.
	Error              string               `json:"error,omitempty"`         // Error message when Status == Error.
	Generation         int64                `json:"generation"`              // Monotonically increasing counter.
	LastReconcile      string               `json:"lastReconcile,omitempty"` // RFC 3339 timestamp of last reconcile.
	ReconcileScheduled bool                 `json:"reconcileScheduled"`      // Deduplication flag for reconcile scheduling.
}
