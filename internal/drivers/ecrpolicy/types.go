// Package ecrpolicy implements the Praxis driver for AWS ECR Lifecycle Policy resources.
//
// This file defines the spec, outputs, observed-state, and reconciliation-state
// types that flow through the driver lifecycle (Provision → Reconcile → Delete).
// The spec is the user's desired configuration; the observed state is read from
// Amazon Elastic Container Registry (ECR); the driver state couples both together with status tracking.
package ecrpolicy

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate Virtual Object service name used to register the AWS ECR Lifecycle Policy driver.
const ServiceName = "ECRLifecyclePolicy"

// ECRLifecyclePolicySpec declares the user's desired configuration for a AWS ECR Lifecycle Policy.
// Fields are validated before any AWS call and mapped to Amazon Elastic Container Registry (ECR) API inputs.
type ECRLifecyclePolicySpec struct {
	Account             string `json:"account,omitempty"`
	Region              string `json:"region"`
	RepositoryName      string `json:"repositoryName"`
	LifecyclePolicyText string `json:"lifecyclePolicyText"`
	ManagedKey          string `json:"managedKey,omitempty"`
}

// ECRLifecyclePolicyOutputs holds the values produced after provisioning a AWS ECR Lifecycle Policy.
// These outputs are stored in Restate K/V and can be referenced by
// downstream resources (e.g. listeners referencing an ALB ARN).
type ECRLifecyclePolicyOutputs struct {
	RepositoryName string `json:"repositoryName"`
	RepositoryArn  string `json:"repositoryArn,omitempty"`
	RegistryId     string `json:"registryId,omitempty"`
}

// ObservedState captures the live configuration of a AWS ECR Lifecycle Policy
// as read from Amazon Elastic Container Registry (ECR). It is compared against the spec
// during drift detection.
type ObservedState struct {
	RepositoryName      string `json:"repositoryName"`
	RepositoryArn       string `json:"repositoryArn,omitempty"`
	RegistryId          string `json:"registryId,omitempty"`
	LifecyclePolicyText string `json:"lifecyclePolicyText"`
}

// ECRLifecyclePolicyState is the single atomic state object persisted under drivers.StateKey
// in the Restate K/V store. It combines desired spec, observed state,
// outputs, lifecycle status, mode (managed/observed), error message,
// generation counter, and reconciliation scheduling metadata.
type ECRLifecyclePolicyState struct {
	Desired            ECRLifecyclePolicySpec    `json:"desired"`
	Observed           ObservedState             `json:"observed"`
	Outputs            ECRLifecyclePolicyOutputs `json:"outputs"`
	Status             types.ResourceStatus      `json:"status"`
	Mode               types.Mode                `json:"mode"`
	Error              string                    `json:"error,omitempty"`
	Generation         int64                     `json:"generation"`
	LastReconcile      string                    `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                      `json:"reconcileScheduled"`
}
