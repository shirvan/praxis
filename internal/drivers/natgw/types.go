// Package natgw implements the Praxis driver for AWS NAT Gateways.
//
// A NAT Gateway enables instances in private subnets to connect to the
// internet or other AWS services without exposing them to inbound traffic.
// NAT Gateways can be "public" (requires an Elastic IP) or "private"
// (for VPC-to-VPC communication). All configuration except tags is immutable
// after creation; changing SubnetId or ConnectivityType requires replacement.
//
// The shared lifecycle kernel observes asynchronous readiness. A failed
// gateway remains visible in Error until the user explicitly deletes it and
// provisions again.
package natgw

// ServiceName is the Restate Virtual Object name used to register this driver.
const ServiceName = "NATGateway"

// NATGatewaySpec is the user-declared desired state for a NAT Gateway.
type NATGatewaySpec struct {
	Account          string            `json:"account,omitempty"`          // AWS account alias resolved to credentials.
	Region           string            `json:"region"`                     // AWS region.
	SubnetId         string            `json:"subnetId"`                   // Subnet to place the NAT Gateway in (immutable).
	ConnectivityType string            `json:"connectivityType,omitempty"` // "public" (default) or "private" (immutable).
	AllocationId     string            `json:"allocationId,omitempty"`     // Elastic IP allocation ID; required for public, forbidden for private.
	Tags             map[string]string `json:"tags,omitempty"`             // AWS resource tags.
	ManagedKey       string            `json:"managedKey,omitempty"`       // Ownership key as praxis:managed-key tag.
}

// NATGatewayOutputs holds AWS-assigned identifiers after provisioning.
type NATGatewayOutputs struct {
	NatGatewayId       string `json:"natGatewayId"`           // AWS-assigned ID (nat-xxxx).
	SubnetId           string `json:"subnetId"`               // Subnet the NAT GW resides in.
	VpcId              string `json:"vpcId"`                  // VPC the subnet belongs to.
	ConnectivityType   string `json:"connectivityType"`       // Resolved connectivity type.
	State              string `json:"state"`                  // NAT GW state from AWS.
	PublicIp           string `json:"publicIp,omitempty"`     // Public IP (public type only).
	PrivateIp          string `json:"privateIp"`              // Private IP always assigned.
	AllocationId       string `json:"allocationId,omitempty"` // Associated Elastic IP allocation.
	NetworkInterfaceId string `json:"networkInterfaceId"`     // ENI created by the NAT GW.
}

// ObservedState captures the live AWS configuration of a NAT Gateway.
// FailureCode and FailureMessage are populated when the NAT Gateway
// transitions to a failed state (e.g. "Gateway.NotAttached").
type ObservedState struct {
	NatGatewayId       string            `json:"natGatewayId"`
	SubnetId           string            `json:"subnetId"`
	VpcId              string            `json:"vpcId"`
	ConnectivityType   string            `json:"connectivityType"`
	State              string            `json:"state"`
	PublicIp           string            `json:"publicIp,omitempty"`
	PrivateIp          string            `json:"privateIp"`
	AllocationId       string            `json:"allocationId,omitempty"`
	NetworkInterfaceId string            `json:"networkInterfaceId"`
	FailureCode        string            `json:"failureCode,omitempty"`
	FailureMessage     string            `json:"failureMessage,omitempty"`
	Tags               map[string]string `json:"tags"`
}
