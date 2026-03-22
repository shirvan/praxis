package routetable

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHasDrift_NoDrift(t *testing.T) {
	desired, err := normalizeSpec(RouteTableSpec{
		VpcId:        "vpc-123",
		Region:       "us-east-1",
		Routes:       []Route{{DestinationCidrBlock: "0.0.0.0/0", GatewayId: "igw-123"}},
		Associations: []Association{{SubnetId: "subnet-123"}},
		Tags:         map[string]string{"Name": "public-rt"},
	})
	require.NoError(t, err)
	observed := ObservedState{
		VpcId: "vpc-123",
		Routes: []ObservedRoute{
			{DestinationCidrBlock: "10.0.0.0/16", GatewayId: "local", Origin: "CreateRouteTable", State: "active"},
			{DestinationCidrBlock: "0.0.0.0/0", GatewayId: "igw-123", Origin: "CreateRoute", State: "active"},
		},
		Associations: []ObservedAssociation{{AssociationId: "rtbassoc-123", SubnetId: "subnet-123"}},
		Tags:         map[string]string{"Name": "public-rt", "praxis:managed-key": "vpc-123~public-rt"},
	}
	assert.False(t, HasDrift(desired, observed))
}

func TestHasDrift_RouteTargetChanged(t *testing.T) {
	desired := RouteTableSpec{Routes: []Route{{DestinationCidrBlock: "0.0.0.0/0", GatewayId: "igw-123"}}}
	observed := ObservedState{Routes: []ObservedRoute{{DestinationCidrBlock: "0.0.0.0/0", NatGatewayId: "nat-123", Origin: "CreateRoute", State: "active"}}}
	assert.True(t, HasDrift(desired, observed))
}

func TestHasDrift_LocalAndPropagatedIgnored(t *testing.T) {
	desired := RouteTableSpec{}
	observed := ObservedState{Routes: []ObservedRoute{
		{DestinationCidrBlock: "10.0.0.0/16", GatewayId: "local", Origin: "CreateRouteTable", State: "active"},
		{DestinationCidrBlock: "192.168.0.0/16", GatewayId: "vgw-123", Origin: "EnableVgwRoutePropagation", State: "active"},
	}}
	assert.False(t, HasDrift(desired, observed))
}

func TestComputeFieldDiffs_AssociationAdded(t *testing.T) {
	diffs := ComputeFieldDiffs(
		RouteTableSpec{Associations: []Association{{SubnetId: "subnet-123"}}},
		ObservedState{},
	)
	require.Len(t, diffs, 1)
	assert.Equal(t, "spec.associations[subnet-123]", diffs[0].Path)
}

func TestNormalizeRoute(t *testing.T) {
	assert.Equal(t, "0.0.0.0/0|gateway:igw-123", NormalizeRoute(Route{DestinationCidrBlock: "0.0.0.0/0", GatewayId: "igw-123"}))
}
