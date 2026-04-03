// Package dbparametergroup manages AWS RDS DB Parameter Groups and DB Cluster
// Parameter Groups. A single driver handles both types, selected by the Type field.
package dbparametergroup

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate Virtual Object name for DB Parameter Group resources.
const ServiceName = "DBParameterGroup"

const (
	// TypeDB selects a standard DB Parameter Group (rds:CreateDBParameterGroup).
	TypeDB = "db"
	// TypeCluster selects a Cluster Parameter Group (rds:CreateDBClusterParameterGroup).
	TypeCluster = "cluster"
)

// DBParameterGroupSpec defines the desired state of a DB Parameter Group.
type DBParameterGroupSpec struct {
	Account     string            `json:"account,omitempty"`     // Praxis account alias for credential resolution.
	Region      string            `json:"region"`                // AWS region.
	GroupName   string            `json:"groupName"`             // Immutable after creation.
	Type        string            `json:"type"`                  // "db" or "cluster"; immutable after creation.
	Family      string            `json:"family"`                // Engine family (e.g. "aurora-mysql8.0"); immutable.
	Description string            `json:"description,omitempty"` // Immutable after creation (AWS API limitation).
	Parameters  map[string]string `json:"parameters,omitempty"`  // Key-value parameter overrides; applied in batches of 20.
	Tags        map[string]string `json:"tags,omitempty"`        // User-managed tags; praxis: tags filtered out.
}

// DBParameterGroupOutputs holds the read-only outputs exposed after provisioning or import.
type DBParameterGroupOutputs struct {
	GroupName string `json:"groupName"`
	ARN       string `json:"arn"`
	Family    string `json:"family"`
	Type      string `json:"type"`
}

// ObservedState captures the live AWS state of the parameter group, including
// only user-modified parameters (Source="user").
type ObservedState struct {
	GroupName   string            `json:"groupName"`
	ARN         string            `json:"arn"`
	Family      string            `json:"family"`
	Type        string            `json:"type"`
	Description string            `json:"description"`
	Parameters  map[string]string `json:"parameters"`
	Tags        map[string]string `json:"tags"`
}

// DBParameterGroupState is the durable Restate state persisted per Virtual Object key.
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
