package sg_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/shirvan/praxis/internal/drivers/sg"
)

func TestNormalize_Empty(t *testing.T) {
	spec := sg.SecurityGroupSpec{}
	rules := sg.Normalize(spec)
	assert.Empty(t, rules)
}

func TestNormalize_IngressAndEgress(t *testing.T) {
	spec := sg.SecurityGroupSpec{
		IngressRules: []sg.IngressRule{
			{Protocol: "TCP", FromPort: 80, ToPort: 80, CidrBlock: "0.0.0.0/0"},
			{Protocol: "tcp", FromPort: 443, ToPort: 443, CidrBlock: "10.0.0.0/8"},
		},
		EgressRules: []sg.EgressRule{
			{Protocol: "-1", FromPort: 0, ToPort: 0, CidrBlock: "0.0.0.0/0"},
		},
	}
	rules := sg.Normalize(spec)
	assert.Len(t, rules, 3)

	for _, r := range rules {
		assert.NotEqual(t, "TCP", r.Protocol, "protocol should be lowercased")
		assert.NotEqual(t, "-1", r.Protocol, "-1 should be normalized to 'all'")
	}
}

func TestNormalize_ProtocolNormalization(t *testing.T) {
	spec := sg.SecurityGroupSpec{
		IngressRules: []sg.IngressRule{
			{Protocol: "-1", FromPort: 0, ToPort: 0, CidrBlock: "0.0.0.0/0"},
		},
	}
	rules := sg.Normalize(spec)
	assert.Len(t, rules, 1)
	assert.Equal(t, "all", rules[0].Protocol)
}

func TestHasDrift_NoDrift(t *testing.T) {
	spec := sg.SecurityGroupSpec{
		IngressRules: []sg.IngressRule{
			{Protocol: "tcp", FromPort: 80, ToPort: 80, CidrBlock: "0.0.0.0/0"},
		},
		EgressRules: []sg.EgressRule{
			{Protocol: "-1", FromPort: 0, ToPort: 0, CidrBlock: "0.0.0.0/0"},
		},
		Tags: map[string]string{"env": "prod"},
	}
	obs := sg.ObservedState{
		IngressRules: []sg.NormalizedRule{
			{Direction: "ingress", Protocol: "tcp", FromPort: 80, ToPort: 80, Target: "cidr:0.0.0.0/0"},
		},
		EgressRules: []sg.NormalizedRule{
			{Direction: "egress", Protocol: "all", FromPort: 0, ToPort: 0, Target: "cidr:0.0.0.0/0"},
		},
		Tags: map[string]string{"env": "prod"},
	}
	assert.False(t, sg.HasDrift(spec, obs))
}

func TestHasDrift_RuleDrift(t *testing.T) {
	spec := sg.SecurityGroupSpec{
		IngressRules: []sg.IngressRule{
			{Protocol: "tcp", FromPort: 80, ToPort: 80, CidrBlock: "0.0.0.0/0"},
			{Protocol: "tcp", FromPort: 443, ToPort: 443, CidrBlock: "0.0.0.0/0"},
		},
	}
	obs := sg.ObservedState{
		IngressRules: []sg.NormalizedRule{
			{Direction: "ingress", Protocol: "tcp", FromPort: 80, ToPort: 80, Target: "cidr:0.0.0.0/0"},
		},
	}
	assert.True(t, sg.HasDrift(spec, obs))
}

func TestHasDrift_TagDrift(t *testing.T) {
	spec := sg.SecurityGroupSpec{
		Tags: map[string]string{"env": "prod"},
	}
	obs := sg.ObservedState{
		Tags: map[string]string{"env": "staging"},
	}
	assert.True(t, sg.HasDrift(spec, obs))
}

func TestHasDrift_EmptyTagsNoDrift(t *testing.T) {
	spec := sg.SecurityGroupSpec{
		Tags: map[string]string{},
	}
	obs := sg.ObservedState{
		Tags: nil,
	}
	assert.False(t, sg.HasDrift(spec, obs))
}

func TestHasDrift_OrderIndependent(t *testing.T) {
	spec := sg.SecurityGroupSpec{
		IngressRules: []sg.IngressRule{
			{Protocol: "tcp", FromPort: 443, ToPort: 443, CidrBlock: "0.0.0.0/0"},
			{Protocol: "tcp", FromPort: 80, ToPort: 80, CidrBlock: "0.0.0.0/0"},
		},
	}
	obs := sg.ObservedState{
		IngressRules: []sg.NormalizedRule{
			{Direction: "ingress", Protocol: "tcp", FromPort: 80, ToPort: 80, Target: "cidr:0.0.0.0/0"},
			{Direction: "ingress", Protocol: "tcp", FromPort: 443, ToPort: 443, Target: "cidr:0.0.0.0/0"},
		},
	}
	assert.False(t, sg.HasDrift(spec, obs), "rule order should not matter")
}

func TestHasDrift_ExtraObservedRule(t *testing.T) {
	spec := sg.SecurityGroupSpec{
		IngressRules: []sg.IngressRule{
			{Protocol: "tcp", FromPort: 80, ToPort: 80, CidrBlock: "0.0.0.0/0"},
		},
	}
	obs := sg.ObservedState{
		IngressRules: []sg.NormalizedRule{
			{Direction: "ingress", Protocol: "tcp", FromPort: 80, ToPort: 80, Target: "cidr:0.0.0.0/0"},
			{Direction: "ingress", Protocol: "tcp", FromPort: 22, ToPort: 22, Target: "cidr:0.0.0.0/0"},
		},
	}
	assert.True(t, sg.HasDrift(spec, obs), "extra observed rule should be drift")
}

func TestComputeDiff_NoChanges(t *testing.T) {
	rules := []sg.NormalizedRule{
		{Direction: "ingress", Protocol: "tcp", FromPort: 80, ToPort: 80, Target: "cidr:0.0.0.0/0"},
	}
	toAdd, toRemove := sg.ComputeDiff(rules, rules)
	assert.Empty(t, toAdd)
	assert.Empty(t, toRemove)
}

func TestComputeDiff_AddOnly(t *testing.T) {
	desired := []sg.NormalizedRule{
		{Direction: "ingress", Protocol: "tcp", FromPort: 80, ToPort: 80, Target: "cidr:0.0.0.0/0"},
		{Direction: "ingress", Protocol: "tcp", FromPort: 443, ToPort: 443, Target: "cidr:0.0.0.0/0"},
	}
	observed := []sg.NormalizedRule{
		{Direction: "ingress", Protocol: "tcp", FromPort: 80, ToPort: 80, Target: "cidr:0.0.0.0/0"},
	}
	toAdd, toRemove := sg.ComputeDiff(desired, observed)
	assert.Len(t, toAdd, 1)
	assert.Empty(t, toRemove)
	assert.Equal(t, int32(443), toAdd[0].FromPort)
}

func TestComputeDiff_RemoveOnly(t *testing.T) {
	desired := []sg.NormalizedRule{
		{Direction: "ingress", Protocol: "tcp", FromPort: 80, ToPort: 80, Target: "cidr:0.0.0.0/0"},
	}
	observed := []sg.NormalizedRule{
		{Direction: "ingress", Protocol: "tcp", FromPort: 80, ToPort: 80, Target: "cidr:0.0.0.0/0"},
		{Direction: "ingress", Protocol: "tcp", FromPort: 22, ToPort: 22, Target: "cidr:0.0.0.0/0"},
	}
	toAdd, toRemove := sg.ComputeDiff(desired, observed)
	assert.Empty(t, toAdd)
	assert.Len(t, toRemove, 1)
	assert.Equal(t, int32(22), toRemove[0].FromPort)
}

func TestComputeDiff_AddAndRemove(t *testing.T) {
	desired := []sg.NormalizedRule{
		{Direction: "ingress", Protocol: "tcp", FromPort: 80, ToPort: 80, Target: "cidr:0.0.0.0/0"},
		{Direction: "ingress", Protocol: "tcp", FromPort: 443, ToPort: 443, Target: "cidr:0.0.0.0/0"},
	}
	observed := []sg.NormalizedRule{
		{Direction: "ingress", Protocol: "tcp", FromPort: 80, ToPort: 80, Target: "cidr:0.0.0.0/0"},
		{Direction: "ingress", Protocol: "tcp", FromPort: 22, ToPort: 22, Target: "cidr:0.0.0.0/0"},
	}
	toAdd, toRemove := sg.ComputeDiff(desired, observed)
	assert.Len(t, toAdd, 1)
	assert.Len(t, toRemove, 1)
	assert.Equal(t, int32(443), toAdd[0].FromPort)
	assert.Equal(t, int32(22), toRemove[0].FromPort)
}

func TestComputeDiff_EmptyDesired(t *testing.T) {
	observed := []sg.NormalizedRule{
		{Direction: "ingress", Protocol: "tcp", FromPort: 80, ToPort: 80, Target: "cidr:0.0.0.0/0"},
	}
	toAdd, toRemove := sg.ComputeDiff(nil, observed)
	assert.Empty(t, toAdd)
	assert.Len(t, toRemove, 1)
}

func TestComputeDiff_EmptyObserved(t *testing.T) {
	desired := []sg.NormalizedRule{
		{Direction: "ingress", Protocol: "tcp", FromPort: 80, ToPort: 80, Target: "cidr:0.0.0.0/0"},
	}
	toAdd, toRemove := sg.ComputeDiff(desired, nil)
	assert.Len(t, toAdd, 1)
	assert.Empty(t, toRemove)
}

func TestComputeDiff_MixedDirections(t *testing.T) {
	desired := []sg.NormalizedRule{
		{Direction: "ingress", Protocol: "tcp", FromPort: 80, ToPort: 80, Target: "cidr:0.0.0.0/0"},
		{Direction: "egress", Protocol: "all", FromPort: 0, ToPort: 0, Target: "cidr:0.0.0.0/0"},
	}
	observed := []sg.NormalizedRule{
		{Direction: "egress", Protocol: "all", FromPort: 0, ToPort: 0, Target: "cidr:0.0.0.0/0"},
		{Direction: "ingress", Protocol: "tcp", FromPort: 22, ToPort: 22, Target: "cidr:0.0.0.0/0"},
	}
	toAdd, toRemove := sg.ComputeDiff(desired, observed)
	assert.Len(t, toAdd, 1)
	assert.Equal(t, "ingress", toAdd[0].Direction)
	assert.Equal(t, int32(80), toAdd[0].FromPort)
	assert.Len(t, toRemove, 1)
	assert.Equal(t, "ingress", toRemove[0].Direction)
	assert.Equal(t, int32(22), toRemove[0].FromPort)
}

func TestSplitByDirection(t *testing.T) {
	rules := []sg.NormalizedRule{
		{Direction: "ingress", Protocol: "tcp", FromPort: 80, ToPort: 80, Target: "cidr:0.0.0.0/0"},
		{Direction: "egress", Protocol: "all", FromPort: 0, ToPort: 0, Target: "cidr:0.0.0.0/0"},
		{Direction: "ingress", Protocol: "tcp", FromPort: 443, ToPort: 443, Target: "cidr:0.0.0.0/0"},
	}
	ingress, egress := sg.SplitByDirection(rules)
	assert.Len(t, ingress, 2)
	assert.Len(t, egress, 1)
}
