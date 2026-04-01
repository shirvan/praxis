package ecrrepo

import "github.com/shirvan/praxis/pkg/types"

const ServiceName = "ECRRepository"

type ImageScanningConfiguration struct {
	ScanOnPush bool `json:"scanOnPush"`
}

type EncryptionConfiguration struct {
	EncryptionType string `json:"encryptionType"`
	KmsKey         string `json:"kmsKey,omitempty"`
}

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

type ECRRepositoryOutputs struct {
	RepositoryArn  string `json:"repositoryArn"`
	RepositoryName string `json:"repositoryName"`
	RepositoryUri  string `json:"repositoryUri"`
	RegistryId     string `json:"registryId"`
}

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
