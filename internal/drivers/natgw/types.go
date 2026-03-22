package natgw

import "github.com/praxiscloud/praxis/pkg/types"

const ServiceName = "NATGateway"

type NATGatewaySpec struct {
	Account          string            `json:"account,omitempty"`
	Region           string            `json:"region"`
	SubnetId         string            `json:"subnetId"`
	ConnectivityType string            `json:"connectivityType,omitempty"`
	AllocationId     string            `json:"allocationId,omitempty"`
	Tags             map[string]string `json:"tags,omitempty"`
	ManagedKey       string            `json:"managedKey,omitempty"`
}

type NATGatewayOutputs struct {
	NatGatewayId       string `json:"natGatewayId"`
	SubnetId           string `json:"subnetId"`
	VpcId              string `json:"vpcId"`
	ConnectivityType   string `json:"connectivityType"`
	State              string `json:"state"`
	PublicIp           string `json:"publicIp,omitempty"`
	PrivateIp          string `json:"privateIp"`
	AllocationId       string `json:"allocationId,omitempty"`
	NetworkInterfaceId string `json:"networkInterfaceId"`
}

type ObservedState struct {
	NatGatewayId       string            `json:"natGatewayId"`
	SubnetId           string            `json:"subnetId"`
	VpcId              string            `json:"vpcId"`
	ConnectivityType   string            `json:"connectivityType"`
	State              string            `json:"state"`
	PublicIp           string            `json:"publicIp,omitempty"`
	PrivateIp          string            `json:"privateIp"`
	AllocationId       string            `json:"allocationId,omitempty"`
	NetworkInterfaceId string            `json:"networkInterfaceId"`
	FailureCode        string            `json:"failureCode,omitempty"`
	FailureMessage     string            `json:"failureMessage,omitempty"`
	Tags               map[string]string `json:"tags"`
}

type NATGatewayState struct {
	Desired            NATGatewaySpec       `json:"desired"`
	Observed           ObservedState        `json:"observed"`
	Outputs            NATGatewayOutputs    `json:"outputs"`
	Status             types.ResourceStatus `json:"status"`
	Mode               types.Mode           `json:"mode"`
	Error              string               `json:"error,omitempty"`
	Generation         int64                `json:"generation"`
	LastReconcile      string               `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
