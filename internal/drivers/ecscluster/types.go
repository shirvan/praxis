// Package ecscluster implements the Praxis driver for AWS ECS clusters.
//
// This file defines the spec, outputs, observed-state, and reconciliation-state
// types that flow through the driver lifecycle (Provision → Reconcile → Delete).
// The spec is the user's desired configuration; the observed state is read from
// the ECS DescribeClusters API; the driver state couples both together with
// status tracking.
package ecscluster

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate Virtual Object service name used to register the AWS ECS cluster driver.
const ServiceName = "ECSCluster"

// ECSClusterSpec declares the user's desired configuration for an ECS cluster.
//
// An ECS cluster has no immutable spec fields beyond its identity (region +
// name); every configurable attribute is converged in place during
// reconciliation.
//
// Mutable fields:
//   - ContainerInsights: CloudWatch Container Insights toggle ("enabled"|"disabled")
//   - CapacityProviders: capacity providers associated with the cluster
//   - Tags:              user-defined tags (praxis:-prefixed tags are reserved)
type ECSClusterSpec struct {
	Account           string            `json:"account,omitempty"`
	Region            string            `json:"region"`
	Name              string            `json:"name"`
	ContainerInsights string            `json:"containerInsights,omitempty"`
	CapacityProviders []string          `json:"capacityProviders,omitempty"`
	Tags              map[string]string `json:"tags,omitempty"`
	ManagedKey        string            `json:"managedKey,omitempty"`
}

// ECSClusterOutputs holds the values produced after provisioning an ECS cluster.
type ECSClusterOutputs struct {
	ARN    string `json:"arn"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// ObservedState captures the live configuration of an ECS cluster as read from
// the DescribeClusters API. It is compared against the spec during drift
// detection.
type ObservedState struct {
	ARN               string            `json:"arn"`
	Name              string            `json:"name"`
	Status            string            `json:"status"`
	ContainerInsights string            `json:"containerInsights,omitempty"`
	CapacityProviders []string          `json:"capacityProviders,omitempty"`
	Tags              map[string]string `json:"tags,omitempty"`
}

// ECSClusterState is the single atomic state object persisted under
// drivers.StateKey in the Restate K/V store. It combines desired spec, observed
// state, outputs, lifecycle status, mode (managed/observed), error message,
// generation counter, and reconciliation scheduling metadata.
type ECSClusterState struct {
	Desired            ECSClusterSpec       `json:"desired"`
	Observed           ObservedState        `json:"observed"`
	Outputs            ECSClusterOutputs    `json:"outputs"`
	Status             types.ResourceStatus `json:"status"`
	Mode               types.Mode           `json:"mode"`
	Error              string               `json:"error,omitempty"`
	Generation         int64                `json:"generation"`
	LastReconcile      string               `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
