package nlb

import "github.com/praxiscloud/praxis/pkg/types"

const ServiceName = "NLB"

type NLBSpec struct {
	Account                string            `json:"account,omitempty"`
	Region                 string            `json:"region"`
	Name                   string            `json:"name"`
	Scheme                 string            `json:"scheme"`
	IpAddressType          string            `json:"ipAddressType"`
	Subnets                []string          `json:"subnets,omitempty"`
	SubnetMappings         []SubnetMapping   `json:"subnetMappings,omitempty"`
	CrossZoneLoadBalancing bool              `json:"crossZoneLoadBalancing"`
	DeletionProtection     bool              `json:"deletionProtection"`
	Tags                   map[string]string `json:"tags,omitempty"`
}

type SubnetMapping struct {
	SubnetId     string `json:"subnetId"`
	AllocationId string `json:"allocationId,omitempty"`
}

type NLBOutputs struct {
	LoadBalancerArn       string `json:"loadBalancerArn"`
	DnsName               string `json:"dnsName"`
	HostedZoneId          string `json:"hostedZoneId"`
	VpcId                 string `json:"vpcId"`
	CanonicalHostedZoneId string `json:"canonicalHostedZoneId"`
}

type ObservedState struct {
	LoadBalancerArn        string            `json:"loadBalancerArn"`
	DnsName                string            `json:"dnsName"`
	HostedZoneId           string            `json:"hostedZoneId"`
	Name                   string            `json:"name"`
	Scheme                 string            `json:"scheme"`
	VpcId                  string            `json:"vpcId"`
	IpAddressType          string            `json:"ipAddressType"`
	Subnets                []string          `json:"subnets"`
	CrossZoneLoadBalancing bool              `json:"crossZoneLoadBalancing"`
	DeletionProtection     bool              `json:"deletionProtection"`
	Tags                   map[string]string `json:"tags"`
	State                  string            `json:"state"`
}

type NLBState struct {
	Desired            NLBSpec              `json:"desired"`
	Observed           ObservedState        `json:"observed"`
	Outputs            NLBOutputs           `json:"outputs"`
	Status             types.ResourceStatus `json:"status"`
	Mode               types.Mode           `json:"mode"`
	Error              string               `json:"error,omitempty"`
	Generation         int64                `json:"generation"`
	LastReconcile      string               `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
