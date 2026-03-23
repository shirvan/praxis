package vpc

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate Virtual Object name for VPCs.
const ServiceName = "VPC"

// VPCSpec is the desired state for a VPC.
type VPCSpec struct {
	Account            string            `json:"account,omitempty"`
	Region             string            `json:"region"`
	CidrBlock          string            `json:"cidrBlock"`
	EnableDnsHostnames bool              `json:"enableDnsHostnames"`
	EnableDnsSupport   bool              `json:"enableDnsSupport"`
	InstanceTenancy    string            `json:"instanceTenancy,omitempty"`
	Tags               map[string]string `json:"tags,omitempty"`
	ManagedKey         string            `json:"managedKey,omitempty"`
}

// VPCOutputs is produced after provisioning and stored in Restate K/V.
type VPCOutputs struct {
	VpcId              string `json:"vpcId"`
	ARN                string `json:"arn,omitempty"`
	CidrBlock          string `json:"cidrBlock"`
	State              string `json:"state"`
	EnableDnsHostnames bool   `json:"enableDnsHostnames"`
	EnableDnsSupport   bool   `json:"enableDnsSupport"`
	InstanceTenancy    string `json:"instanceTenancy"`
	OwnerId            string `json:"ownerId"`
	DhcpOptionsId      string `json:"dhcpOptionsId"`
	IsDefault          bool   `json:"isDefault"`
}

// ObservedState captures the actual configuration of a VPC from AWS Describe calls.
type ObservedState struct {
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

// VPCState is the single atomic state object stored under drivers.StateKey.
type VPCState struct {
	Desired            VPCSpec              `json:"desired"`
	Observed           ObservedState        `json:"observed"`
	Outputs            VPCOutputs           `json:"outputs"`
	Status             types.ResourceStatus `json:"status"`
	Mode               types.Mode           `json:"mode"`
	Error              string               `json:"error,omitempty"`
	Generation         int64                `json:"generation"`
	LastReconcile      string               `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
