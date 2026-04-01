package ecrpolicy

import "github.com/shirvan/praxis/pkg/types"

const ServiceName = "ECRLifecyclePolicy"

type ECRLifecyclePolicySpec struct {
	Account             string `json:"account,omitempty"`
	Region              string `json:"region"`
	RepositoryName      string `json:"repositoryName"`
	LifecyclePolicyText string `json:"lifecyclePolicyText"`
	ManagedKey          string `json:"managedKey,omitempty"`
}

type ECRLifecyclePolicyOutputs struct {
	RepositoryName string `json:"repositoryName"`
	RepositoryArn  string `json:"repositoryArn,omitempty"`
	RegistryId     string `json:"registryId,omitempty"`
}

type ObservedState struct {
	RepositoryName      string `json:"repositoryName"`
	RepositoryArn       string `json:"repositoryArn,omitempty"`
	RegistryId          string `json:"registryId,omitempty"`
	LifecyclePolicyText string `json:"lifecyclePolicyText"`
}

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
