// Package nlb implements the Praxis driver for AWS Network Load Balancer (NLB) resources.
//
// This file defines the spec, outputs, observed-state, and reconciliation-state
// types that flow through the driver lifecycle (Provision → Reconcile → Delete).
// The spec is the user's desired configuration; the observed state is read from
// Elastic Load Balancing v2; the driver state couples both together with status tracking.
package nlb

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate Virtual Object service name used to register the AWS Network Load Balancer (NLB) driver.
const ServiceName = "NLB"

// NLBSpec declares the user's desired configuration for a AWS Network Load Balancer (NLB).
// Fields are validated before any AWS call and mapped to Elastic Load Balancing v2 API inputs.
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

// SubnetMapping maps a subnet ID to an optional Elastic IP allocation for ALB/NLB placement.
type SubnetMapping struct {
	SubnetId     string `json:"subnetId"`
	AllocationId string `json:"allocationId,omitempty"`
}

// NLBOutputs holds the values produced after provisioning a AWS Network Load Balancer (NLB).
// These outputs are stored in Restate K/V and can be referenced by
// downstream resources (e.g. listeners referencing an ALB ARN).
type NLBOutputs struct {
	LoadBalancerArn       string `json:"loadBalancerArn"`
	DnsName               string `json:"dnsName"`
	HostedZoneId          string `json:"hostedZoneId"`
	VpcId                 string `json:"vpcId"`
	CanonicalHostedZoneId string `json:"canonicalHostedZoneId"`
}

// ObservedState captures the live configuration of a AWS Network Load Balancer (NLB)
// as read from Elastic Load Balancing v2. It is compared against the spec
// during drift detection.
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

// NLBState is the single atomic state object persisted under drivers.StateKey
// in the Restate K/V store. It combines desired spec, observed state,
// outputs, lifecycle status, mode (managed/observed), error message,
// generation counter, and reconciliation scheduling metadata.
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
