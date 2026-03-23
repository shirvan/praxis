package dbparametergroup

import "github.com/praxiscloud/praxis/pkg/types"

const ServiceName = "DBParameterGroup"

const (
	TypeDB      = "db"
	TypeCluster = "cluster"
)

type DBParameterGroupSpec struct {
	Account     string            `json:"account,omitempty"`
	Region      string            `json:"region"`
	GroupName   string            `json:"groupName"`
	Type        string            `json:"type"`
	Family      string            `json:"family"`
	Description string            `json:"description,omitempty"`
	Parameters  map[string]string `json:"parameters,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`
}

type DBParameterGroupOutputs struct {
	GroupName string `json:"groupName"`
	ARN       string `json:"arn"`
	Family    string `json:"family"`
	Type      string `json:"type"`
}

type ObservedState struct {
	GroupName   string            `json:"groupName"`
	ARN         string            `json:"arn"`
	Family      string            `json:"family"`
	Type        string            `json:"type"`
	Description string            `json:"description"`
	Parameters  map[string]string `json:"parameters"`
	Tags        map[string]string `json:"tags"`
}

type DBParameterGroupState struct {
	Desired            DBParameterGroupSpec    `json:"desired"`
	Observed           ObservedState           `json:"observed"`
	Outputs            DBParameterGroupOutputs `json:"outputs"`
	Status             types.ResourceStatus    `json:"status"`
	Mode               types.Mode              `json:"mode"`
	Error              string                  `json:"error,omitempty"`
	Generation         int64                   `json:"generation"`
	LastReconcile      string                  `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                    `json:"reconcileScheduled"`
}
