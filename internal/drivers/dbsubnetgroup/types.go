// Package dbsubnetgroup manages AWS RDS DB Subnet Groups.
// A DB Subnet Group specifies which VPC subnets an RDS instance or Aurora cluster
// can be placed into. It must span at least two availability zones.
package dbsubnetgroup

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate Virtual Object name for DB Subnet Group resources.
const ServiceName = "DBSubnetGroup"

// DBSubnetGroupSpec defines the desired state of an RDS DB Subnet Group.
type DBSubnetGroupSpec struct {
	Account     string            `json:"account,omitempty"` // Praxis account alias for credential resolution.
	Region      string            `json:"region"`            // AWS region (inferred on import).
	GroupName   string            `json:"groupName"`         // Immutable after creation; the DB subnet group name.
	Description string            `json:"description"`       // Human-readable description, mutable.
	SubnetIds   []string          `json:"subnetIds"`         // Must contain at least 2 subnets across 2 AZs.
	Tags        map[string]string `json:"tags,omitempty"`    // User-managed tags; praxis: tags filtered out.
}

// DBSubnetGroupOutputs holds the read-only outputs exposed after provisioning or import.
type DBSubnetGroupOutputs struct {
	GroupName         string   `json:"groupName"`
	ARN               string   `json:"arn"`
	VpcId             string   `json:"vpcId"`             // VPC the subnets belong to.
	SubnetIds         []string `json:"subnetIds"`         // Sorted subnet IDs.
	AvailabilityZones []string `json:"availabilityZones"` // Derived from the subnets' AZs.
	Status            string   `json:"status"`            // AWS subnet group status (e.g. "Complete").
}

// ObservedState captures the live AWS state of the DB Subnet Group.
type ObservedState struct {
	GroupName         string            `json:"groupName"`
	ARN               string            `json:"arn"`
	Description       string            `json:"description"`
	VpcId             string            `json:"vpcId"`
	SubnetIds         []string          `json:"subnetIds"`
	AvailabilityZones []string          `json:"availabilityZones"`
	Status            string            `json:"status"`
	Tags              map[string]string `json:"tags"`
}

// DBSubnetGroupState is the durable Restate state persisted per Virtual Object key.
type DBSubnetGroupState struct {
	Desired            DBSubnetGroupSpec    `json:"desired"`
	Observed           ObservedState        `json:"observed"`
	Outputs            DBSubnetGroupOutputs `json:"outputs"`
	Status             types.ResourceStatus `json:"status"`
	Mode               types.Mode           `json:"mode"`
	Error              string               `json:"error,omitempty"`
	Generation         int64                `json:"generation"`
	LastReconcile      string               `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
