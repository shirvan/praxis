package alb

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/shirvan/praxis/pkg/types"
)

func TestServiceName(t *testing.T) {
	drv := NewALBDriver(nil)
	assert.Equal(t, "ALB", drv.ServiceName())
}

func TestSpecFromObserved(t *testing.T) {
	obs := ObservedState{
		Name:               "my-alb",
		Scheme:             "internet-facing",
		IpAddressType:      "ipv4",
		Subnets:            []string{"subnet-1", "subnet-2"},
		SecurityGroups:     []string{"sg-1"},
		DeletionProtection: false,
		IdleTimeout:        60,
		Tags:               map[string]string{"env": "dev", "praxis:managed-key": "us-east-1~my-alb"},
	}
	spec := specFromObserved(obs)
	assert.Equal(t, obs.Name, spec.Name)
	assert.Equal(t, obs.Scheme, spec.Scheme)
	assert.Equal(t, obs.IpAddressType, spec.IpAddressType)
	assert.Equal(t, obs.Subnets, spec.Subnets)
	assert.Equal(t, obs.SecurityGroups, spec.SecurityGroups)
	assert.Equal(t, obs.DeletionProtection, spec.DeletionProtection)
	assert.Equal(t, obs.IdleTimeout, spec.IdleTimeout)
	assert.Equal(t, map[string]string{"env": "dev"}, spec.Tags, "praxis: tags should be filtered out")
}

func TestDefaultImportMode(t *testing.T) {
	assert.Equal(t, types.ModeObserved, defaultImportMode(""))
	assert.Equal(t, types.ModeManaged, defaultImportMode(types.ModeManaged))
	assert.Equal(t, types.ModeObserved, defaultImportMode(types.ModeObserved))
}

func TestApplyDefaults(t *testing.T) {
	spec := applyDefaults(ALBSpec{Name: "  my-alb  ", Region: "  us-east-1  "})
	assert.Equal(t, "my-alb", spec.Name)
	assert.Equal(t, "us-east-1", spec.Region)
	assert.Equal(t, "internet-facing", spec.Scheme)
	assert.Equal(t, "ipv4", spec.IpAddressType)
	assert.Equal(t, 60, spec.IdleTimeout)
	assert.NotNil(t, spec.Tags)
}

func TestApplyDefaults_PreservesExplicit(t *testing.T) {
	spec := applyDefaults(ALBSpec{Scheme: "internal", IpAddressType: "dualstack", IdleTimeout: 120})
	assert.Equal(t, "internal", spec.Scheme)
	assert.Equal(t, "dualstack", spec.IpAddressType)
	assert.Equal(t, 120, spec.IdleTimeout)
}

func TestValidateSpec(t *testing.T) {
	base := applyDefaults(ALBSpec{
		Region:         "us-east-1",
		Name:           "my-alb",
		Subnets:        []string{"subnet-1", "subnet-2"},
		SecurityGroups: []string{"sg-1"},
	})
	assert.NoError(t, validateSpec(base))

	noRegion := base
	noRegion.Region = ""
	assert.Error(t, validateSpec(noRegion))

	noName := base
	noName.Name = ""
	assert.Error(t, validateSpec(noName))

	noSubnets := base
	noSubnets.Subnets = nil
	assert.Error(t, validateSpec(noSubnets))

	oneSubnet := base
	oneSubnet.Subnets = []string{"subnet-1"}
	assert.Error(t, validateSpec(oneSubnet))

	noSG := base
	noSG.SecurityGroups = nil
	assert.Error(t, validateSpec(noSG))

	subnetMappings := base
	subnetMappings.Subnets = nil
	subnetMappings.SubnetMappings = []SubnetMapping{{SubnetId: "subnet-1"}, {SubnetId: "subnet-2"}}
	assert.NoError(t, validateSpec(subnetMappings))

	oneMapping := base
	oneMapping.Subnets = nil
	oneMapping.SubnetMappings = []SubnetMapping{{SubnetId: "subnet-1"}}
	assert.Error(t, validateSpec(oneMapping))
}

func TestHasImmutableChange(t *testing.T) {
	spec := ALBSpec{Scheme: "internet-facing"}
	obs := ObservedState{Scheme: "internet-facing"}
	assert.False(t, hasImmutableChange(spec, obs))

	schemeChanged := spec
	schemeChanged.Scheme = "internal"
	assert.True(t, hasImmutableChange(schemeChanged, obs))
}

func TestOutputsFromObserved(t *testing.T) {
	obs := ObservedState{
		LoadBalancerArn: "arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/app/my-alb/1234",
		DnsName:         "my-alb-1234.us-east-1.elb.amazonaws.com",
		HostedZoneId:    "Z35SXDOTRQ7X7K",
		VpcId:           "vpc-123",
	}
	out := outputsFromObserved(obs)
	assert.Equal(t, obs.LoadBalancerArn, out.LoadBalancerArn)
	assert.Equal(t, obs.DnsName, out.DnsName)
	assert.Equal(t, obs.HostedZoneId, out.HostedZoneId)
	assert.Equal(t, obs.VpcId, out.VpcId)
	assert.Equal(t, obs.HostedZoneId, out.CanonicalHostedZoneId)
}

func TestMapsEqual(t *testing.T) {
	assert.True(t, mapsEqual(map[string]string{"a": "1"}, map[string]string{"a": "1"}))
	assert.False(t, mapsEqual(map[string]string{"a": "1"}, map[string]string{"a": "2"}))
	assert.False(t, mapsEqual(map[string]string{"a": "1"}, map[string]string{"b": "1"}))
	assert.False(t, mapsEqual(map[string]string{"a": "1"}, map[string]string{}))
}
