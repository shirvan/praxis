package nlb

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/shirvan/praxis/pkg/types"
)

func TestServiceName(t *testing.T) {
	drv := NewNLBDriver(nil)
	assert.Equal(t, "NLB", drv.ServiceName())
}

func TestSpecFromObserved(t *testing.T) {
	obs := ObservedState{
		Name:                   "my-nlb",
		Scheme:                 "internet-facing",
		IpAddressType:          "ipv4",
		Subnets:                []string{"subnet-1", "subnet-2"},
		CrossZoneLoadBalancing: true,
		DeletionProtection:     false,
		Tags:                   map[string]string{"env": "dev", "praxis:managed-key": "us-east-1~my-nlb"},
	}
	spec := specFromObserved(obs)
	assert.Equal(t, obs.Name, spec.Name)
	assert.Equal(t, obs.Scheme, spec.Scheme)
	assert.Equal(t, obs.IpAddressType, spec.IpAddressType)
	assert.Equal(t, obs.Subnets, spec.Subnets)
	assert.Equal(t, obs.CrossZoneLoadBalancing, spec.CrossZoneLoadBalancing)
	assert.Equal(t, obs.DeletionProtection, spec.DeletionProtection)
	assert.Equal(t, map[string]string{"env": "dev"}, spec.Tags, "praxis: tags should be filtered out")
}

func TestDefaultImportMode(t *testing.T) {
	assert.Equal(t, types.ModeObserved, defaultImportMode(""))
	assert.Equal(t, types.ModeManaged, defaultImportMode(types.ModeManaged))
	assert.Equal(t, types.ModeObserved, defaultImportMode(types.ModeObserved))
}

func TestApplyDefaults(t *testing.T) {
	spec := applyDefaults(NLBSpec{Name: "  my-nlb  ", Region: "  us-east-1  "})
	assert.Equal(t, "my-nlb", spec.Name)
	assert.Equal(t, "us-east-1", spec.Region)
	assert.Equal(t, "internet-facing", spec.Scheme)
	assert.Equal(t, "ipv4", spec.IpAddressType)
	assert.NotNil(t, spec.Tags)
}

func TestApplyDefaults_PreservesExplicit(t *testing.T) {
	spec := applyDefaults(NLBSpec{Scheme: "internal", IpAddressType: "dualstack"})
	assert.Equal(t, "internal", spec.Scheme)
	assert.Equal(t, "dualstack", spec.IpAddressType)
}

func TestValidateSpec(t *testing.T) {
	base := applyDefaults(NLBSpec{
		Region:  "us-east-1",
		Name:    "my-nlb",
		Subnets: []string{"subnet-1"},
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

	subnetMappings := base
	subnetMappings.Subnets = nil
	subnetMappings.SubnetMappings = []SubnetMapping{{SubnetId: "subnet-1"}}
	assert.NoError(t, validateSpec(subnetMappings))
}

func TestHasImmutableChange(t *testing.T) {
	spec := NLBSpec{Scheme: "internet-facing"}
	obs := ObservedState{Scheme: "internet-facing"}
	assert.False(t, hasImmutableChange(spec, obs))

	schemeChanged := spec
	schemeChanged.Scheme = "internal"
	assert.True(t, hasImmutableChange(schemeChanged, obs))
}

func TestOutputsFromObserved(t *testing.T) {
	obs := ObservedState{
		LoadBalancerArn: "arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/net/my-nlb/1234",
		DnsName:         "my-nlb-1234.us-east-1.elb.amazonaws.com",
		HostedZoneId:    "Z26RNL4JYFTOTI",
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
