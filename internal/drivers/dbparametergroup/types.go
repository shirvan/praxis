// Package dbparametergroup manages AWS RDS DB Parameter Groups and DB Cluster
// Parameter Groups. A single driver handles both types, selected by the Type field.
package dbparametergroup

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
	Account     string            `json:"account,omitempty"`
	Region      string            `json:"region"`
	GroupName   string            `json:"groupName"`
	Type        string            `json:"type"`
	Family      string            `json:"family"`
	Description string            `json:"description,omitempty"`
	Parameters  map[string]string `json:"parameters,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`
	ManagedKey  string            `json:"managedKey,omitempty"`
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
	Region      string            `json:"region,omitempty"`
	GroupName   string            `json:"groupName"`
	ARN         string            `json:"arn"`
	Family      string            `json:"family"`
	Type        string            `json:"type"`
	Description string            `json:"description"`
	Parameters  map[string]string `json:"parameters"`
	Tags        map[string]string `json:"tags"`
	ManagedKey  string            `json:"managedKey,omitempty"`
}
