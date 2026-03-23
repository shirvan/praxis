package esm

import "github.com/shirvan/praxis/pkg/types"

const ServiceName = "EventSourceMapping"

type EventSourceMappingSpec struct {
	Account                        string              `json:"account,omitempty"`
	Region                         string              `json:"region"`
	FunctionName                   string              `json:"functionName"`
	EventSourceArn                 string              `json:"eventSourceArn"`
	Enabled                        bool                `json:"enabled"`
	BatchSize                      *int32              `json:"batchSize,omitempty"`
	MaximumBatchingWindowInSeconds *int32              `json:"maximumBatchingWindowInSeconds,omitempty"`
	StartingPosition               string              `json:"startingPosition,omitempty"`
	StartingPositionTimestamp      *string             `json:"startingPositionTimestamp,omitempty"`
	FilterCriteria                 *FilterCriteriaSpec `json:"filterCriteria,omitempty"`
	BisectBatchOnFunctionError     *bool               `json:"bisectBatchOnFunctionError,omitempty"`
	MaximumRetryAttempts           *int32              `json:"maximumRetryAttempts,omitempty"`
	MaximumRecordAgeInSeconds      *int32              `json:"maximumRecordAgeInSeconds,omitempty"`
	ParallelizationFactor          *int32              `json:"parallelizationFactor,omitempty"`
	TumblingWindowInSeconds        *int32              `json:"tumblingWindowInSeconds,omitempty"`
	DestinationConfig              *DestinationSpec    `json:"destinationConfig,omitempty"`
	ScalingConfig                  *ScalingSpec        `json:"scalingConfig,omitempty"`
	FunctionResponseTypes          []string            `json:"functionResponseTypes,omitempty"`
	ManagedKey                     string              `json:"managedKey,omitempty"`
}

type FilterCriteriaSpec struct {
	Filters []FilterSpec `json:"filters"`
}

type FilterSpec struct {
	Pattern string `json:"pattern"`
}

type DestinationSpec struct {
	OnFailure OnFailureSpec `json:"onFailure"`
}

type OnFailureSpec struct {
	DestinationArn string `json:"destinationArn"`
}

type ScalingSpec struct {
	MaximumConcurrency int32 `json:"maximumConcurrency"`
}

type EventSourceMappingOutputs struct {
	UUID           string `json:"uuid"`
	EventSourceArn string `json:"eventSourceArn"`
	FunctionArn    string `json:"functionArn"`
	State          string `json:"state"`
	LastModified   string `json:"lastModified"`
	BatchSize      int32  `json:"batchSize"`
}

type ObservedState struct {
	UUID                           string              `json:"uuid"`
	EventSourceArn                 string              `json:"eventSourceArn"`
	FunctionArn                    string              `json:"functionArn"`
	State                          string              `json:"state"`
	BatchSize                      int32               `json:"batchSize"`
	MaximumBatchingWindowInSeconds int32               `json:"maximumBatchingWindowInSeconds"`
	StartingPosition               string              `json:"startingPosition,omitempty"`
	FilterCriteria                 *FilterCriteriaSpec `json:"filterCriteria,omitempty"`
	BisectBatchOnFunctionError     bool                `json:"bisectBatchOnFunctionError"`
	MaximumRetryAttempts           *int32              `json:"maximumRetryAttempts,omitempty"`
	MaximumRecordAgeInSeconds      *int32              `json:"maximumRecordAgeInSeconds,omitempty"`
	ParallelizationFactor          int32               `json:"parallelizationFactor"`
	TumblingWindowInSeconds        int32               `json:"tumblingWindowInSeconds"`
	DestinationConfig              *DestinationSpec    `json:"destinationConfig,omitempty"`
	ScalingConfig                  *ScalingSpec        `json:"scalingConfig,omitempty"`
	FunctionResponseTypes          []string            `json:"functionResponseTypes,omitempty"`
	LastModified                   string              `json:"lastModified"`
}

type EventSourceMappingState struct {
	Desired            EventSourceMappingSpec    `json:"desired"`
	Observed           ObservedState             `json:"observed"`
	Outputs            EventSourceMappingOutputs `json:"outputs"`
	Status             types.ResourceStatus      `json:"status"`
	Mode               types.Mode                `json:"mode"`
	Error              string                    `json:"error,omitempty"`
	Generation         int64                     `json:"generation"`
	LastReconcile      string                    `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                      `json:"reconcileScheduled"`
}
