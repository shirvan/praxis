package loggroup

import "github.com/shirvan/praxis/pkg/types"

const ServiceName = "LogGroup"

type LogGroupSpec struct {
	Account         string            `json:"account,omitempty"`
	Region          string            `json:"region"`
	LogGroupName    string            `json:"logGroupName"`
	LogGroupClass   string            `json:"logGroupClass,omitempty"`
	RetentionInDays *int32            `json:"retentionInDays,omitempty"`
	KmsKeyID        string            `json:"kmsKeyId,omitempty"`
	Tags            map[string]string `json:"tags,omitempty"`
	ManagedKey      string            `json:"managedKey,omitempty"`
}

type LogGroupOutputs struct {
	ARN             string `json:"arn"`
	LogGroupName    string `json:"logGroupName"`
	LogGroupClass   string `json:"logGroupClass"`
	RetentionInDays int32  `json:"retentionInDays"`
	KmsKeyID        string `json:"kmsKeyId,omitempty"`
	CreationTime    int64  `json:"creationTime"`
	StoredBytes     int64  `json:"storedBytes"`
}

type ObservedState struct {
	ARN             string            `json:"arn"`
	LogGroupName    string            `json:"logGroupName"`
	LogGroupClass   string            `json:"logGroupClass"`
	RetentionInDays *int32            `json:"retentionInDays,omitempty"`
	KmsKeyID        string            `json:"kmsKeyId,omitempty"`
	CreationTime    int64             `json:"creationTime"`
	StoredBytes     int64             `json:"storedBytes"`
	Tags            map[string]string `json:"tags,omitempty"`
}

type LogGroupState struct {
	Desired            LogGroupSpec         `json:"desired"`
	Observed           ObservedState        `json:"observed"`
	Outputs            LogGroupOutputs      `json:"outputs"`
	Status             types.ResourceStatus `json:"status"`
	Mode               types.Mode           `json:"mode"`
	Error              string               `json:"error,omitempty"`
	Generation         int64                `json:"generation"`
	LastReconcile      string               `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
