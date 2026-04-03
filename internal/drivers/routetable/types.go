// Package routetable implements the Praxis driver for AWS VPC Route Tables.
//
// A Route Table contains a set of rules (routes) that determine where network
// traffic from subnets or gateways is directed. Each VPC has a main route table
// that cannot be deleted (the "main" association). Additional custom route
// tables can be created and associated with individual subnets.
//
// This driver manages creation, route convergence (individual routes via
// CreateRoute/DeleteRoute/ReplaceRoute), subnet associations, tag management,
// drift detection, and deletion. Routes with Origin=CreateRouteTable (the
// implicit VPC local route) and VGW-propagated routes are excluded from
// drift comparison since they are AWS-managed.
package routetable

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate Virtual Object name used to register this driver.
const ServiceName = "RouteTable"

// Route defines a single desired routing rule. Exactly one target field must
// be set; the driver validates this during spec normalization.
type Route struct {
	DestinationCidrBlock   string `json:"destinationCidrBlock"`             // IPv4 CIDR for traffic matching.
	GatewayId              string `json:"gatewayId,omitempty"`              // Internet or Virtual Private Gateway target.
	NatGatewayId           string `json:"natGatewayId,omitempty"`           // NAT Gateway target for private subnet outbound.
	VpcPeeringConnectionId string `json:"vpcPeeringConnectionId,omitempty"` // VPC Peering connection target.
	TransitGatewayId       string `json:"transitGatewayId,omitempty"`       // Transit Gateway target.
	NetworkInterfaceId     string `json:"networkInterfaceId,omitempty"`     // ENI target for appliance routing.
	VpcEndpointId          string `json:"vpcEndpointId,omitempty"`          // VPC Endpoint target (Gateway LB).
}

// Association declares that a subnet should be associated with this route table.
type Association struct {
	SubnetId string `json:"subnetId"` // Subnet to associate.
}

// RouteTableSpec is the user-declared desired state for a route table.
type RouteTableSpec struct {
	Account      string            `json:"account,omitempty"`      // AWS account alias.
	Region       string            `json:"region"`                 // AWS region.
	VpcId        string            `json:"vpcId"`                  // VPC that owns this route table.
	Routes       []Route           `json:"routes,omitempty"`       // Desired routing rules.
	Associations []Association     `json:"associations,omitempty"` // Desired subnet associations.
	Tags         map[string]string `json:"tags,omitempty"`         // AWS resource tags.
	ManagedKey   string            `json:"managedKey,omitempty"`   // Ownership key as praxis:managed-key tag.
}

// ObservedRoute extends Route with AWS-assigned metadata from DescribeRouteTables.
type ObservedRoute struct {
	DestinationCidrBlock   string `json:"destinationCidrBlock"`
	GatewayId              string `json:"gatewayId,omitempty"`
	NatGatewayId           string `json:"natGatewayId,omitempty"`
	VpcPeeringConnectionId string `json:"vpcPeeringConnectionId,omitempty"`
	TransitGatewayId       string `json:"transitGatewayId,omitempty"`
	NetworkInterfaceId     string `json:"networkInterfaceId,omitempty"`
	VpcEndpointId          string `json:"vpcEndpointId,omitempty"`
	State                  string `json:"state"`  // Route state: "active" or "blackhole".
	Origin                 string `json:"origin"` // How the route was created: "CreateRouteTable", "CreateRoute", "EnableVgwRoutePropagation".
}

// ObservedAssociation extends Association with AWS metadata.
type ObservedAssociation struct {
	AssociationId string `json:"associationId"` // AWS-assigned association ID (rtbassoc-xxxx).
	SubnetId      string `json:"subnetId"`      // Associated subnet.
	Main          bool   `json:"main"`          // Whether this is the VPC's main route table association.
}

// RouteTableOutputs holds AWS-assigned identifiers after provisioning.
type RouteTableOutputs struct {
	RouteTableId string                `json:"routeTableId"`           // AWS-assigned route table ID (rtb-xxxx).
	VpcId        string                `json:"vpcId"`                  // Owning VPC.
	OwnerId      string                `json:"ownerId"`                // AWS account ID.
	Routes       []ObservedRoute       `json:"routes,omitempty"`       // Current routes from AWS.
	Associations []ObservedAssociation `json:"associations,omitempty"` // Current associations.
}

// ObservedState captures the live AWS configuration of a route table.
type ObservedState struct {
	RouteTableId string                `json:"routeTableId"`
	VpcId        string                `json:"vpcId"`
	OwnerId      string                `json:"ownerId"`
	Routes       []ObservedRoute       `json:"routes"`
	Associations []ObservedAssociation `json:"associations"`
	Tags         map[string]string     `json:"tags"`
}

// RouteTableState is the single atomic state object stored under drivers.StateKey.
type RouteTableState struct {
	Desired            RouteTableSpec       `json:"desired"`                 // User-declared target configuration.
	Observed           ObservedState        `json:"observed"`                // Last-known AWS state.
	Outputs            RouteTableOutputs    `json:"outputs"`                 // Stable identifiers returned to callers.
	Status             types.ResourceStatus `json:"status"`                  // Lifecycle status.
	Mode               types.Mode           `json:"mode"`                    // Managed or Observed.
	Error              string               `json:"error,omitempty"`         // Error message when Status == Error.
	Generation         int64                `json:"generation"`              // Monotonically increasing counter.
	LastReconcile      string               `json:"lastReconcile,omitempty"` // RFC 3339 timestamp of last reconcile.
	ReconcileScheduled bool                 `json:"reconcileScheduled"`      // Deduplication flag for reconcile scheduling.
}
