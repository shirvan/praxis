package alb

import "github.com/praxiscloud/praxis/pkg/types"

const ServiceName = "ALB"

type ALBSpec struct {
	Account            string            `json:"account,omitempty"`
	Region             string            `json:"region"`
	Name               string            `json:"name"`
	Scheme             string            `json:"scheme"`
	IpAddressType      string            `json:"ipAddressType"`
	Subnets            []string          `json:"subnets,omitempty"`
	SubnetMappings     []SubnetMapping   `json:"subnetMappings,omitempty"`
	SecurityGroups     []string          `json:"securityGroups"`
	AccessLogs         *AccessLogConfig  `json:"accessLogs,omitempty"`
	DeletionProtection bool              `json:"deletionProtection"`
	IdleTimeout        int               `json:"idleTimeout"`
	Tags               map[string]string `json:"tags,omitempty"`
}

type SubnetMapping struct {
	SubnetId     string `json:"subnetId"`
	AllocationId string `json:"allocationId,omitempty"`
}

type AccessLogConfig struct {
	Enabled bool   `json:"enabled"`
	Bucket  string `json:"bucket"`
	Prefix  string `json:"prefix,omitempty"`
}

type ALBOutputs struct {
	LoadBalancerArn       string `json:"loadBalancerArn"`
	DnsName               string `json:"dnsName"`
	HostedZoneId          string `json:"hostedZoneId"`
	VpcId                 string `json:"vpcId"`
	CanonicalHostedZoneId string `json:"canonicalHostedZoneId"`
}

type ObservedState struct {
	LoadBalancerArn    string            `json:"loadBalancerArn"`
	DnsName            string            `json:"dnsName"`
	HostedZoneId       string            `json:"hostedZoneId"`
	Name               string            `json:"name"`
	Scheme             string            `json:"scheme"`
	VpcId              string            `json:"vpcId"`
	IpAddressType      string            `json:"ipAddressType"`
	Subnets            []string          `json:"subnets"`
	SecurityGroups     []string          `json:"securityGroups"`
	AccessLogs         *AccessLogConfig  `json:"accessLogs,omitempty"`
	DeletionProtection bool              `json:"deletionProtection"`
	IdleTimeout        int               `json:"idleTimeout"`
	Tags               map[string]string `json:"tags"`
	State              string            `json:"state"`
}

type ALBState struct {
	Desired            ALBSpec              `json:"desired"`
	Observed           ObservedState        `json:"observed"`
	Outputs            ALBOutputs           `json:"outputs"`
	Status             types.ResourceStatus `json:"status"`
	Mode               types.Mode           `json:"mode"`
	Error              string               `json:"error,omitempty"`
	Generation         int64                `json:"generation"`
	LastReconcile      string               `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
