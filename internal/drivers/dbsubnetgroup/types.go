package dbsubnetgroup

import "github.com/shirvan/praxis/pkg/types"

const ServiceName = "DBSubnetGroup"

type DBSubnetGroupSpec struct {
	Account     string            `json:"account,omitempty"`
	Region      string            `json:"region"`
	GroupName   string            `json:"groupName"`
	Description string            `json:"description"`
	SubnetIds   []string          `json:"subnetIds"`
	Tags        map[string]string `json:"tags,omitempty"`
}

type DBSubnetGroupOutputs struct {
	GroupName         string   `json:"groupName"`
	ARN               string   `json:"arn"`
	VpcId             string   `json:"vpcId"`
	SubnetIds         []string `json:"subnetIds"`
	AvailabilityZones []string `json:"availabilityZones"`
	Status            string   `json:"status"`
}

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
