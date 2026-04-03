// Package ecrrepo implements the Praxis driver for AWS ECR Repository resources.
//
// This file defines the spec, outputs, observed-state, and reconciliation-state
// types that flow through the driver lifecycle (Provision → Reconcile → Delete).
// The spec is the user's desired configuration; the observed state is read from
// Amazon Elastic Container Registry (ECR); the driver state couples both together with status tracking.
package ecrrepo

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate Virtual Object service name used to register the AWS ECR Repository driver.
const ServiceName = "ECRRepository"

// ImageScanningConfiguration controls whether images are scanned on push to the ECR repository.
type ImageScanningConfiguration struct {
	ScanOnPush bool `json:"scanOnPush"`
}

// EncryptionConfiguration specifies the encryption type (AES256 or KMS) for the ECR repository.
type EncryptionConfiguration struct {
	EncryptionType string `json:"encryptionType"`
	KmsKey         string `json:"kmsKey,omitempty"`
}

// ECRRepositorySpec declares the user's desired configuration for a AWS ECR Repository.
// Fields are validated before any AWS call and mapped to Amazon Elastic Container Registry (ECR) API inputs.
type ECRRepositorySpec struct {
	Account                    string                      `json:"account,omitempty"`
	Region                     string                      `json:"region"`
	RepositoryName             string                      `json:"repositoryName"`
	ImageTagMutability         string                      `json:"imageTagMutability,omitempty"`
	ImageScanningConfiguration *ImageScanningConfiguration `json:"imageScanningConfiguration,omitempty"`
	EncryptionConfiguration    *EncryptionConfiguration    `json:"encryptionConfiguration,omitempty"`
	RepositoryPolicy           string                      `json:"repositoryPolicy,omitempty"`
	ForceDelete                bool                        `json:"forceDelete"`
	Tags                       map[string]string           `json:"tags,omitempty"`
	ManagedKey                 string                      `json:"managedKey,omitempty"`
}

// ECRRepositoryOutputs holds the values produced after provisioning a AWS ECR Repository.
// These outputs are stored in Restate K/V and can be referenced by
// downstream resources (e.g. listeners referencing an ALB ARN).
type ECRRepositoryOutputs struct {
	RepositoryArn  string `json:"repositoryArn"`
	RepositoryName string `json:"repositoryName"`
	RepositoryUri  string `json:"repositoryUri"`
	RegistryId     string `json:"registryId"`
}

// ObservedState captures the live configuration of a AWS ECR Repository
// as read from Amazon Elastic Container Registry (ECR). It is compared against the spec
// during drift detection.
type ObservedState struct {
	RepositoryArn              string                      `json:"repositoryArn"`
	RepositoryName             string                      `json:"repositoryName"`
	RepositoryUri              string                      `json:"repositoryUri"`
	RegistryId                 string                      `json:"registryId"`
	ImageTagMutability         string                      `json:"imageTagMutability,omitempty"`
	ImageScanningConfiguration *ImageScanningConfiguration `json:"imageScanningConfiguration,omitempty"`
	EncryptionConfiguration    *EncryptionConfiguration    `json:"encryptionConfiguration,omitempty"`
	RepositoryPolicy           string                      `json:"repositoryPolicy,omitempty"`
	Tags                       map[string]string           `json:"tags,omitempty"`
}

// ECRRepositoryState is the single atomic state object persisted under drivers.StateKey
// in the Restate K/V store. It combines desired spec, observed state,
// outputs, lifecycle status, mode (managed/observed), error message,
// generation counter, and reconciliation scheduling metadata.
type ECRRepositoryState struct {
	Desired            ECRRepositorySpec    `json:"desired"`
	Observed           ObservedState        `json:"observed"`
	Outputs            ECRRepositoryOutputs `json:"outputs"`
	Status             types.ResourceStatus `json:"status"`
	Mode               types.Mode           `json:"mode"`
	Error              string               `json:"error,omitempty"`
	Generation         int64                `json:"generation"`
	LastReconcile      string               `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
