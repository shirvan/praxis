package igw

import "github.com/praxiscloud/praxis/pkg/types"

const ServiceName = "InternetGateway"

type IGWSpec struct {
	Account    string            `json:"account,omitempty"`
	Region     string            `json:"region"`
	VpcId      string            `json:"vpcId"`
	Tags       map[string]string `json:"tags,omitempty"`
	ManagedKey string            `json:"managedKey,omitempty"`
}

type IGWOutputs struct {
	InternetGatewayId string `json:"internetGatewayId"`
	VpcId             string `json:"vpcId"`
	OwnerId           string `json:"ownerId"`
	State             string `json:"state"`
}

type ObservedState struct {
	InternetGatewayId string            `json:"internetGatewayId"`
	AttachedVpcId     string            `json:"attachedVpcId"`
	OwnerId           string            `json:"ownerId"`
	Tags              map[string]string `json:"tags"`
}

type IGWState struct {
	Desired            IGWSpec              `json:"desired"`
	Observed           ObservedState        `json:"observed"`
	Outputs            IGWOutputs           `json:"outputs"`
	Status             types.ResourceStatus `json:"status"`
	Mode               types.Mode           `json:"mode"`
	Error              string               `json:"error,omitempty"`
	Generation         int64                `json:"generation"`
	LastReconcile      string               `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
