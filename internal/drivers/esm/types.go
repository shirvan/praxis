// Package esm implements the Praxis driver for AWS Lambda Event Source Mappings.
// ESMs connect event sources (SQS, Kinesis, DynamoDB Streams, etc.) to Lambda
// functions and control batching, filtering, retry, and scaling behavior.
package esm

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate Virtual Object name for the Event Source Mapping driver.
const ServiceName = "EventSourceMapping"

// EventSourceMappingSpec defines the desired state for a Lambda Event Source Mapping.
// EventSourceArn and StartingPosition are immutable after creation.
type EventSourceMappingSpec struct {
	Account                        string              `json:"account,omitempty"`
	Region                         string              `json:"region"`
	FunctionName                   string              `json:"functionName"`                             // Target Lambda function name or ARN.
	EventSourceArn                 string              `json:"eventSourceArn"`                           // Immutable: ARN of the event source.
	Enabled                        bool                `json:"enabled"`                                  // Mutable: enable/disable the mapping.
	BatchSize                      *int32              `json:"batchSize,omitempty"`                      // Mutable: max records per batch.
	MaximumBatchingWindowInSeconds *int32              `json:"maximumBatchingWindowInSeconds,omitempty"` // Mutable: batching window.
	StartingPosition               string              `json:"startingPosition,omitempty"`               // Immutable: LATEST, TRIM_HORIZON, AT_TIMESTAMP.
	StartingPositionTimestamp      *string             `json:"startingPositionTimestamp,omitempty"`      // Immutable: timestamp for AT_TIMESTAMP.
	FilterCriteria                 *FilterCriteriaSpec `json:"filterCriteria,omitempty"`                 // Mutable: event filtering patterns.
	BisectBatchOnFunctionError     *bool               `json:"bisectBatchOnFunctionError,omitempty"`     // Mutable: bisect on error (streams).
	MaximumRetryAttempts           *int32              `json:"maximumRetryAttempts,omitempty"`           // Mutable: retry count (streams).
	MaximumRecordAgeInSeconds      *int32              `json:"maximumRecordAgeInSeconds,omitempty"`      // Mutable: max record age (streams).
	ParallelizationFactor          *int32              `json:"parallelizationFactor,omitempty"`          // Mutable: concurrent batches per shard.
	TumblingWindowInSeconds        *int32              `json:"tumblingWindowInSeconds,omitempty"`        // Mutable: tumbling window duration.
	DestinationConfig              *DestinationSpec    `json:"destinationConfig,omitempty"`              // Mutable: failure destination (DLQ).
	ScalingConfig                  *ScalingSpec        `json:"scalingConfig,omitempty"`                  // Mutable: max concurrency.
	FunctionResponseTypes          []string            `json:"functionResponseTypes,omitempty"`          // Mutable: e.g. ["ReportBatchItemFailures"].
	ManagedKey                     string              `json:"managedKey,omitempty"`                     // praxis:managed-key tag value.
}

// FilterCriteriaSpec defines event filter patterns for the mapping.
type FilterCriteriaSpec struct {
	Filters []FilterSpec `json:"filters"`
}

// FilterSpec is a single event filter pattern (JSON pattern matching).
type FilterSpec struct {
	Pattern string `json:"pattern"`
}

// DestinationSpec configures where failed records are sent.
type DestinationSpec struct {
	OnFailure OnFailureSpec `json:"onFailure"`
}

// OnFailureSpec defines the failure destination ARN (typically an SQS queue or SNS topic).
type OnFailureSpec struct {
	DestinationArn string `json:"destinationArn"`
}

// ScalingSpec controls the maximum number of concurrent mapping instances.
type ScalingSpec struct {
	MaximumConcurrency int32 `json:"maximumConcurrency"`
}

// EventSourceMappingOutputs are the user-facing outputs after provisioning.
type EventSourceMappingOutputs struct {
	UUID           string `json:"uuid"`
	EventSourceArn string `json:"eventSourceArn"`
	FunctionArn    string `json:"functionArn"`
	State          string `json:"state"`
	LastModified   string `json:"lastModified"`
	BatchSize      int32  `json:"batchSize"`
}

// ObservedState captures the last-observed AWS state from GetEventSourceMapping.
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

// EventSourceMappingState is the full durable state stored in the Restate Virtual Object.
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
