package subnet

import "github.com/praxiscloud/praxis/pkg/types"

const ServiceName = "Subnet"

type SubnetSpec struct {
	Account             string            `json:"account,omitempty"`
	Region              string            `json:"region"`
	VpcId               string            `json:"vpcId"`
	CidrBlock           string            `json:"cidrBlock"`
	AvailabilityZone    string            `json:"availabilityZone"`
	MapPublicIpOnLaunch bool              `json:"mapPublicIpOnLaunch"`
	Tags                map[string]string `json:"tags,omitempty"`
	ManagedKey          string            `json:"managedKey,omitempty"`
}

type SubnetOutputs struct {
	SubnetId            string `json:"subnetId"`
	ARN                 string `json:"arn,omitempty"`
	VpcId               string `json:"vpcId"`
	CidrBlock           string `json:"cidrBlock"`
	AvailabilityZone    string `json:"availabilityZone"`
	AvailabilityZoneId  string `json:"availabilityZoneId"`
	MapPublicIpOnLaunch bool   `json:"mapPublicIpOnLaunch"`
	State               string `json:"state"`
	OwnerId             string `json:"ownerId"`
	AvailableIpCount    int    `json:"availableIpCount"`
}

type ObservedState struct {
	SubnetId            string            `json:"subnetId"`
	VpcId               string            `json:"vpcId"`
	CidrBlock           string            `json:"cidrBlock"`
	AvailabilityZone    string            `json:"availabilityZone"`
	AvailabilityZoneId  string            `json:"availabilityZoneId"`
	MapPublicIpOnLaunch bool              `json:"mapPublicIpOnLaunch"`
	State               string            `json:"state"`
	OwnerId             string            `json:"ownerId"`
	AvailableIpCount    int               `json:"availableIpCount"`
	Tags                map[string]string `json:"tags"`
}

type SubnetState struct {
	Desired            SubnetSpec           `json:"desired"`
	Observed           ObservedState        `json:"observed"`
	Outputs            SubnetOutputs        `json:"outputs"`
	Status             types.ResourceStatus `json:"status"`
	Mode               types.Mode           `json:"mode"`
	Error              string               `json:"error,omitempty"`
	Generation         int64                `json:"generation"`
	LastReconcile      string               `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
