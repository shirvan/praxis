// Package ekscluster implements the Praxis driver for AWS EKS clusters.
//
// This file defines the spec, outputs, observed-state, and reconciliation-state
// types that flow through the driver lifecycle (Provision → Reconcile → Delete).
// The spec is the user's desired configuration; the observed state is read from
// the EKS DescribeCluster API; the driver state couples both together with
// status tracking.
package ekscluster

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate Virtual Object service name used to register the AWS EKS cluster driver.
const ServiceName = "EKSCluster"

// EKSClusterSpec declares the user's desired configuration for an EKS cluster.
//
// Immutable fields (set at creation; changes surface as requires-replacement diffs):
//   - RoleArn:          IAM role the control plane assumes
//   - SubnetIds:        subnets the control-plane ENIs are placed in
//   - SecurityGroupIds: additional security groups for the control-plane ENIs
//
// Mutable fields (converged in place during reconciliation):
//   - Version:               Kubernetes control-plane version (upgrade only)
//   - EndpointPublicAccess:  whether the public API endpoint is enabled
//   - EndpointPrivateAccess: whether the private API endpoint is enabled
//   - PublicAccessCidrs:     CIDRs allowed to reach the public endpoint
//   - EnabledLoggingTypes:   control-plane log types shipped to CloudWatch Logs
//   - Tags:                  user-defined tags (praxis:-prefixed tags are reserved)
type EKSClusterSpec struct {
	Account               string            `json:"account,omitempty"`
	Region                string            `json:"region"`
	Name                  string            `json:"name"`
	RoleArn               string            `json:"roleArn"`
	SubnetIds             []string          `json:"subnetIds"`
	SecurityGroupIds      []string          `json:"securityGroupIds,omitempty"`
	Version               string            `json:"version,omitempty"`
	EndpointPublicAccess  bool              `json:"endpointPublicAccess"`
	EndpointPrivateAccess bool              `json:"endpointPrivateAccess"`
	PublicAccessCidrs     []string          `json:"publicAccessCidrs,omitempty"`
	EnabledLoggingTypes   []string          `json:"enabledLoggingTypes,omitempty"`
	Tags                  map[string]string `json:"tags,omitempty"`
	ManagedKey            string            `json:"managedKey,omitempty"`
}

// EKSClusterOutputs holds the values produced after provisioning an EKS cluster.
type EKSClusterOutputs struct {
	ARN             string `json:"arn"`
	Name            string `json:"name"`
	Status          string `json:"status"`
	Version         string `json:"version"`
	PlatformVersion string `json:"platformVersion,omitempty"`
	Endpoint        string `json:"endpoint,omitempty"`
}

// ObservedState captures the live configuration of an EKS cluster as read from
// the DescribeCluster API. It is compared against the spec during drift
// detection.
type ObservedState struct {
	ARN                   string            `json:"arn"`
	Name                  string            `json:"name"`
	Status                string            `json:"status"`
	Version               string            `json:"version"`
	PlatformVersion       string            `json:"platformVersion,omitempty"`
	Endpoint              string            `json:"endpoint,omitempty"`
	RoleArn               string            `json:"roleArn"`
	SubnetIds             []string          `json:"subnetIds"`
	SecurityGroupIds      []string          `json:"securityGroupIds,omitempty"`
	EndpointPublicAccess  bool              `json:"endpointPublicAccess"`
	EndpointPrivateAccess bool              `json:"endpointPrivateAccess"`
	PublicAccessCidrs     []string          `json:"publicAccessCidrs,omitempty"`
	EnabledLoggingTypes   []string          `json:"enabledLoggingTypes,omitempty"`
	Tags                  map[string]string `json:"tags,omitempty"`
}

// EKSClusterState is the single atomic state object persisted under
// drivers.StateKey in the Restate K/V store. It combines desired spec, observed
// state, outputs, lifecycle status, mode (managed/observed), error message,
// generation counter, and reconciliation scheduling metadata.
type EKSClusterState struct {
	Desired            EKSClusterSpec       `json:"desired"`
	Observed           ObservedState        `json:"observed"`
	Outputs            EKSClusterOutputs    `json:"outputs"`
	Status             types.ResourceStatus `json:"status"`
	Mode               types.Mode           `json:"mode"`
	Error              string               `json:"error,omitempty"`
	Generation         int64                `json:"generation"`
	LastReconcile      string               `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
