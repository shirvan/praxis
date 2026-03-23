package routetable

import "github.com/shirvan/praxis/pkg/types"

const ServiceName = "RouteTable"

type Route struct {
	DestinationCidrBlock   string `json:"destinationCidrBlock"`
	GatewayId              string `json:"gatewayId,omitempty"`
	NatGatewayId           string `json:"natGatewayId,omitempty"`
	VpcPeeringConnectionId string `json:"vpcPeeringConnectionId,omitempty"`
	TransitGatewayId       string `json:"transitGatewayId,omitempty"`
	NetworkInterfaceId     string `json:"networkInterfaceId,omitempty"`
	VpcEndpointId          string `json:"vpcEndpointId,omitempty"`
}

type Association struct {
	SubnetId string `json:"subnetId"`
}

type RouteTableSpec struct {
	Account      string            `json:"account,omitempty"`
	Region       string            `json:"region"`
	VpcId        string            `json:"vpcId"`
	Routes       []Route           `json:"routes,omitempty"`
	Associations []Association     `json:"associations,omitempty"`
	Tags         map[string]string `json:"tags,omitempty"`
	ManagedKey   string            `json:"managedKey,omitempty"`
}

type ObservedRoute struct {
	DestinationCidrBlock   string `json:"destinationCidrBlock"`
	GatewayId              string `json:"gatewayId,omitempty"`
	NatGatewayId           string `json:"natGatewayId,omitempty"`
	VpcPeeringConnectionId string `json:"vpcPeeringConnectionId,omitempty"`
	TransitGatewayId       string `json:"transitGatewayId,omitempty"`
	NetworkInterfaceId     string `json:"networkInterfaceId,omitempty"`
	VpcEndpointId          string `json:"vpcEndpointId,omitempty"`
	State                  string `json:"state"`
	Origin                 string `json:"origin"`
}

type ObservedAssociation struct {
	AssociationId string `json:"associationId"`
	SubnetId      string `json:"subnetId"`
	Main          bool   `json:"main"`
}

type RouteTableOutputs struct {
	RouteTableId string                `json:"routeTableId"`
	VpcId        string                `json:"vpcId"`
	OwnerId      string                `json:"ownerId"`
	Routes       []ObservedRoute       `json:"routes,omitempty"`
	Associations []ObservedAssociation `json:"associations,omitempty"`
}

type ObservedState struct {
	RouteTableId string                `json:"routeTableId"`
	VpcId        string                `json:"vpcId"`
	OwnerId      string                `json:"ownerId"`
	Routes       []ObservedRoute       `json:"routes"`
	Associations []ObservedAssociation `json:"associations"`
	Tags         map[string]string     `json:"tags"`
}

type RouteTableState struct {
	Desired            RouteTableSpec       `json:"desired"`
	Observed           ObservedState        `json:"observed"`
	Outputs            RouteTableOutputs    `json:"outputs"`
	Status             types.ResourceStatus `json:"status"`
	Mode               types.Mode           `json:"mode"`
	Error              string               `json:"error,omitempty"`
	Generation         int64                `json:"generation"`
	LastReconcile      string               `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
