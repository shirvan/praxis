package provider

import (
	"context"
	"errors"
	"testing"

	restate "github.com/restatedev/sdk-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers/ebs"
	"github.com/shirvan/praxis/internal/drivers/eip"
	"github.com/shirvan/praxis/internal/drivers/igw"
	"github.com/shirvan/praxis/internal/drivers/nacl"
	"github.com/shirvan/praxis/internal/drivers/natgw"
	"github.com/shirvan/praxis/internal/drivers/route53healthcheck"
	"github.com/shirvan/praxis/internal/drivers/routetable"
	"github.com/shirvan/praxis/internal/drivers/vpcpeering"
)

type ebsDirectLookupStub struct {
	ebs.EBSAPI
	observed ebs.ObservedState
	err      error
}

func (s ebsDirectLookupStub) DescribeVolume(context.Context, string) (ebs.ObservedState, error) {
	return s.observed, s.err
}

type eipDirectLookupStub struct {
	eip.EIPAPI
	observed eip.ObservedState
	err      error
}

func (s eipDirectLookupStub) DescribeAddress(context.Context, string) (eip.ObservedState, error) {
	return s.observed, s.err
}

type igwDirectLookupStub struct {
	igw.IGWAPI
	observed igw.ObservedState
	err      error
}

func (s igwDirectLookupStub) DescribeInternetGateway(context.Context, string) (igw.ObservedState, error) {
	return s.observed, s.err
}

type naclDirectLookupStub struct {
	nacl.NetworkACLAPI
	observed nacl.ObservedState
	err      error
}

func (s naclDirectLookupStub) DescribeNetworkACL(context.Context, string) (nacl.ObservedState, error) {
	return s.observed, s.err
}

type natgwDirectLookupStub struct {
	natgw.NATGatewayAPI
	observed natgw.ObservedState
	err      error
}

func (s natgwDirectLookupStub) DescribeNATGateway(context.Context, string) (natgw.ObservedState, error) {
	return s.observed, s.err
}

type routeTableDirectLookupStub struct {
	routetable.RouteTableAPI
	observed routetable.ObservedState
	err      error
}

func (s routeTableDirectLookupStub) DescribeRouteTable(context.Context, string) (routetable.ObservedState, error) {
	return s.observed, s.err
}

type vpcPeeringDirectLookupStub struct {
	vpcpeering.VPCPeeringAPI
	observed vpcpeering.ObservedState
	err      error
}

func (s vpcPeeringDirectLookupStub) DescribeVPCPeeringConnection(context.Context, string) (vpcpeering.ObservedState, error) {
	return s.observed, s.err
}

type healthCheckDirectLookupStub struct {
	route53healthcheck.HealthCheckAPI
	observed route53healthcheck.ObservedState
	err      error
}

func (s healthCheckDirectLookupStub) DescribeHealthCheck(context.Context, string) (route53healthcheck.ObservedState, error) {
	return s.observed, s.err
}

func TestDirectIDLookupProbes_MapObservedOutputs(t *testing.T) {
	t.Run("EBSVolume", func(t *testing.T) {
		probe := ebsLookupProbe(ebsDirectLookupStub{observed: ebs.ObservedState{
			VolumeId: "vol-123", Region: "us-west-2", AccountId: "123456789012",
			AvailabilityZone: "us-west-2a", State: "available", SizeGiB: 40,
			VolumeType: "gp3", Encrypted: true, Tags: map[string]string{"Name": "data", "env": "prod"},
		}})
		outputs, found, err := probe(nil, LookupFilter{ID: "vol-123", Name: "data", Tag: map[string]string{"env": "prod"}})
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, "arn:aws:ec2:us-west-2:123456789012:volume/vol-123", outputs.ARN)
		assert.Equal(t, int32(40), outputs.SizeGiB)
	})

	t.Run("ElasticIP", func(t *testing.T) {
		probe := eipLookupProbe(eipDirectLookupStub{observed: eip.ObservedState{
			AllocationId: "eipalloc-123", PublicIp: "203.0.113.4", Region: "us-west-2",
			AccountId: "123456789012", Domain: "vpc", NetworkBorderGroup: "us-west-2",
			Tags: map[string]string{"env": "prod"},
		}})
		outputs, found, err := probe(nil, LookupFilter{ID: "eipalloc-123", Tag: map[string]string{"env": "prod"}})
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, "203.0.113.4", outputs.PublicIp)
		assert.Equal(t, "arn:aws:ec2:us-west-2:123456789012:elastic-ip/eipalloc-123", outputs.ARN)
	})

	t.Run("InternetGateway", func(t *testing.T) {
		probe := igwLookupProbe(igwDirectLookupStub{observed: igw.ObservedState{
			InternetGatewayId: "igw-123", AttachedVpcId: "vpc-123", OwnerId: "123456789012",
			Tags: map[string]string{"env": "prod"},
		}})
		outputs, found, err := probe(nil, LookupFilter{ID: "igw-123", Tag: map[string]string{"env": "prod"}})
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, "vpc-123", outputs.VpcId)
		assert.Equal(t, "available", outputs.State)
	})

	t.Run("NetworkACL", func(t *testing.T) {
		probe := naclLookupProbe(naclDirectLookupStub{observed: nacl.ObservedState{
			NetworkAclId: "acl-123", VpcId: "vpc-123", IsDefault: true,
			Associations: []nacl.NetworkACLAssociation{{AssociationId: "aclassoc-123", SubnetId: "subnet-123"}},
			Tags:         map[string]string{"env": "prod"},
		}})
		outputs, found, err := probe(nil, LookupFilter{ID: "acl-123", Tag: map[string]string{"env": "prod"}})
		require.NoError(t, err)
		assert.True(t, found)
		assert.True(t, outputs.IsDefault)
		require.Len(t, outputs.Associations, 1)
	})

	t.Run("NATGateway", func(t *testing.T) {
		probe := natgwLookupProbe(natgwDirectLookupStub{observed: natgw.ObservedState{
			NatGatewayId: "nat-123", SubnetId: "subnet-123", VpcId: "vpc-123",
			ConnectivityType: "public", State: "available", PublicIp: "203.0.113.8",
			PrivateIp: "10.0.1.4", AllocationId: "eipalloc-123", NetworkInterfaceId: "eni-123",
			Tags: map[string]string{"env": "prod"},
		}})
		outputs, found, err := probe(nil, LookupFilter{ID: "nat-123", Tag: map[string]string{"env": "prod"}})
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, "203.0.113.8", outputs.PublicIp)
		assert.Equal(t, "eni-123", outputs.NetworkInterfaceId)
	})

	t.Run("RouteTable", func(t *testing.T) {
		probe := routeTableLookupProbe(routeTableDirectLookupStub{observed: routetable.ObservedState{
			RouteTableId: "rtb-123", VpcId: "vpc-123", OwnerId: "123456789012",
			Associations: []routetable.ObservedAssociation{{AssociationId: "rtbassoc-123", SubnetId: "subnet-123"}},
			Tags:         map[string]string{"env": "prod"},
		}})
		outputs, found, err := probe(nil, LookupFilter{ID: "rtb-123", Tag: map[string]string{"env": "prod"}})
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, "123456789012", outputs.OwnerId)
		require.Len(t, outputs.Associations, 1)
	})

	t.Run("VPCPeeringConnection", func(t *testing.T) {
		probe := vpcPeeringLookupProbe(vpcPeeringDirectLookupStub{observed: vpcpeering.ObservedState{
			VpcPeeringConnectionId: "pcx-123", RequesterVpcId: "vpc-1", AccepterVpcId: "vpc-2",
			RequesterCidrBlock: "10.0.0.0/16", AccepterCidrBlock: "10.1.0.0/16", Status: "active",
			RequesterOwnerId: "111111111111", AccepterOwnerId: "222222222222",
			Tags: map[string]string{"env": "prod"},
		}})
		outputs, found, err := probe(nil, LookupFilter{ID: "pcx-123", Tag: map[string]string{"env": "prod"}})
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, "vpc-2", outputs.AccepterVpcId)
		assert.Equal(t, "active", outputs.Status)
	})

	t.Run("Route53HealthCheck", func(t *testing.T) {
		probe := route53HealthCheckLookupProbe(healthCheckDirectLookupStub{observed: route53healthcheck.ObservedState{
			HealthCheckId: "hc-123", Tags: map[string]string{"env": "prod"},
		}})
		outputs, found, err := probe(nil, LookupFilter{ID: "hc-123", Tag: map[string]string{"env": "prod"}})
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, "hc-123", outputs.HealthCheckId)
	})
}

func TestDirectIDLookupProbe_RequiresID(t *testing.T) {
	_, _, err := ebsLookupProbe(ebsDirectLookupStub{})(nil, LookupFilter{Name: "data"})
	require.Error(t, err)
	assert.True(t, restate.IsTerminalError(err))
	assert.Equal(t, uint16(400), uint16(restate.ErrorCode(err)))

	_, _, err = route53HealthCheckLookupProbe(healthCheckDirectLookupStub{})(nil, LookupFilter{Tag: map[string]string{"env": "prod"}})
	require.Error(t, err)
	assert.True(t, restate.IsTerminalError(err))
	assert.Equal(t, uint16(400), uint16(restate.ErrorCode(err)))
}

func TestDirectIDLookupProbe_TagMismatchIsAbsent(t *testing.T) {
	probe := natgwLookupProbe(natgwDirectLookupStub{observed: natgw.ObservedState{
		NatGatewayId: "nat-123", Tags: map[string]string{"env": "dev"},
	}})
	_, found, err := probe(nil, LookupFilter{ID: "nat-123", Tag: map[string]string{"env": "prod"}})
	require.NoError(t, err)
	assert.False(t, found)
}

func TestDirectIDLookupProbe_NotFoundIsAbsent(t *testing.T) {
	probe := routeTableLookupProbe(routeTableDirectLookupStub{err: errors.New("resource not found")})
	_, found, err := probe(nil, LookupFilter{ID: "rtb-missing"})
	require.NoError(t, err)
	assert.False(t, found)
}

func TestDirectIDLookupProbe_ProviderErrorRemainsRetryable(t *testing.T) {
	want := errors.New("temporary provider failure")
	probe := vpcPeeringLookupProbe(vpcPeeringDirectLookupStub{err: want})
	_, _, err := probe(nil, LookupFilter{ID: "pcx-123"})
	assert.ErrorIs(t, err, want)
	assert.False(t, restate.IsTerminalError(err))
}
