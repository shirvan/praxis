// Package vpc implements the Praxis driver for Amazon Virtual Private Clouds.
//
// A VPC is an isolated network within an AWS region. It provides the foundation
// for security groups, subnets, route tables, and internet gateways. Each VPC
// owns a primary CIDR block that cannot be changed after creation.
//
// This driver manages creation, DNS attribute configuration, tag convergence,
// drift detection, and deletion. CidrBlock and InstanceTenancy are immutable
// after creation; only DNS settings and tags can be corrected via drift
// reconciliation.
package vpc

// ServiceName is the Restate Virtual Object name for VPCs.
const ServiceName = "VPC"

// VPCSpec is the desired state for a VPC.
type VPCSpec struct {
	Account            string            `json:"account,omitempty"`         // AWS account alias resolved to credentials.
	Region             string            `json:"region"`                    // AWS region for the VPC (e.g. "us-east-1").
	CidrBlock          string            `json:"cidrBlock"`                 // Primary IPv4 CIDR block (immutable after creation).
	EnableDnsHostnames bool              `json:"enableDnsHostnames"`        // Whether instances get public DNS hostnames (mutable, maps to EC2 ModifyVpcAttribute).
	EnableDnsSupport   bool              `json:"enableDnsSupport"`          // Whether the VPC has DNS resolution enabled (mutable, maps to EC2 ModifyVpcAttribute).
	InstanceTenancy    string            `json:"instanceTenancy,omitempty"` // "default" or "dedicated" (immutable after creation).
	Tags               map[string]string `json:"tags,omitempty"`            // AWS resource tags; praxis:-prefixed tags are reserved.
	ManagedKey         string            `json:"managedKey,omitempty"`      // Ownership key stored as praxis:managed-key tag for conflict detection.
}

// VPCOutputs is produced after provisioning and stored in Restate K/V.
// Contains both user-facing values and AWS-assigned metadata.
type VPCOutputs struct {
	VpcId              string `json:"vpcId"`              // AWS-assigned VPC identifier (vpc-xxxx).
	ARN                string `json:"arn,omitempty"`      // Synthesized VPC ARN.
	CidrBlock          string `json:"cidrBlock"`          // Primary IPv4 CIDR block.
	State              string `json:"state"`              // VPC state from AWS ("available", "pending").
	EnableDnsHostnames bool   `json:"enableDnsHostnames"` // Resolved DNS hostnames setting.
	EnableDnsSupport   bool   `json:"enableDnsSupport"`   // Resolved DNS support setting.
	InstanceTenancy    string `json:"instanceTenancy"`    // Tenancy model.
	OwnerId            string `json:"ownerId"`            // AWS account ID that owns the VPC.
	DhcpOptionsId      string `json:"dhcpOptionsId"`      // Associated DHCP options set.
	IsDefault          bool   `json:"isDefault"`          // Whether this is the region's default VPC.
}

// ObservedState captures the actual configuration of a VPC from AWS Describe calls.
// Three separate EC2 API calls are needed: DescribeVpcs (core attributes) plus
// two DescribeVpcAttribute calls for EnableDnsHostnames and EnableDnsSupport.
type ObservedState struct {
	Region             string            `json:"region,omitempty"`
	VpcId              string            `json:"vpcId"`
	CidrBlock          string            `json:"cidrBlock"`
	State              string            `json:"state"`
	EnableDnsHostnames bool              `json:"enableDnsHostnames"`
	EnableDnsSupport   bool              `json:"enableDnsSupport"`
	InstanceTenancy    string            `json:"instanceTenancy"`
	OwnerId            string            `json:"ownerId"`
	DhcpOptionsId      string            `json:"dhcpOptionsId"`
	IsDefault          bool              `json:"isDefault"`
	Tags               map[string]string `json:"tags"`
}
