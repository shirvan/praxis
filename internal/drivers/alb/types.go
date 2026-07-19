// Package alb implements the Praxis driver for AWS Application Load Balancer (ALB) resources.
//
// This file defines the spec, outputs, observed-state, and reconciliation-state
// types that flow through the driver lifecycle (Provision → Reconcile → Delete).
// The spec is the user's desired configuration; the observed state is read from
// Elastic Load Balancing v2; the driver state couples both together with status tracking.
package alb

// ServiceName is the Restate Virtual Object service name used to register the AWS Application Load Balancer (ALB) driver.
const ServiceName = "ALB"

// ALBSpec declares the user's desired configuration for a AWS Application Load Balancer (ALB).
// Fields are validated before any AWS call and mapped to Elastic Load Balancing v2 API inputs.
type ALBSpec struct {
	Account            string            `json:"account,omitempty"`
	Region             string            `json:"region"`
	ManagedKey         string            `json:"managedKey,omitempty"`
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

// SubnetMapping maps a subnet ID to an optional Elastic IP allocation for ALB/NLB placement.
type SubnetMapping struct {
	SubnetId     string `json:"subnetId"`
	AllocationId string `json:"allocationId,omitempty"`
}

// AccessLogConfig controls S3 access log delivery for the load balancer.
type AccessLogConfig struct {
	Enabled bool   `json:"enabled"`
	Bucket  string `json:"bucket"`
	Prefix  string `json:"prefix,omitempty"`
}

// ALBOutputs holds the values produced after provisioning a AWS Application Load Balancer (ALB).
// These outputs are stored in Restate K/V and can be referenced by
// downstream resources (e.g. listeners referencing an ALB ARN).
type ALBOutputs struct {
	LoadBalancerArn       string `json:"loadBalancerArn"`
	DnsName               string `json:"dnsName"`
	HostedZoneId          string `json:"hostedZoneId"`
	VpcId                 string `json:"vpcId"`
	CanonicalHostedZoneId string `json:"canonicalHostedZoneId"`
}

// ObservedState captures the live configuration of a AWS Application Load Balancer (ALB)
// as read from Elastic Load Balancing v2. It is compared against the spec
// during drift detection.
type ObservedState struct {
	Region             string            `json:"region,omitempty"`
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
