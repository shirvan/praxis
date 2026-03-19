package vpc_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/praxiscloud/praxis/internal/drivers/vpc"
)

func TestHasDrift_NoDrift(t *testing.T) {
	spec := vpc.VPCSpec{
		EnableDnsHostnames: true,
		EnableDnsSupport:   true,
		Tags:               map[string]string{"Name": "my-vpc", "env": "dev"},
	}
	obs := vpc.ObservedState{
		State:              "available",
		EnableDnsHostnames: true,
		EnableDnsSupport:   true,
		Tags:               map[string]string{"Name": "my-vpc", "env": "dev", "praxis:managed-key": "us-east-1~my-vpc"},
	}
	assert.False(t, vpc.HasDrift(spec, obs))
}

func TestHasDrift_DnsHostnamesChanged(t *testing.T) {
	spec := vpc.VPCSpec{
		EnableDnsHostnames: true,
		EnableDnsSupport:   true,
	}
	obs := vpc.ObservedState{
		State:              "available",
		EnableDnsHostnames: false,
		EnableDnsSupport:   true,
	}
	assert.True(t, vpc.HasDrift(spec, obs))
}

func TestHasDrift_DnsSupportChanged(t *testing.T) {
	spec := vpc.VPCSpec{
		EnableDnsHostnames: false,
		EnableDnsSupport:   false,
	}
	obs := vpc.ObservedState{
		State:              "available",
		EnableDnsHostnames: false,
		EnableDnsSupport:   true,
	}
	assert.True(t, vpc.HasDrift(spec, obs))
}

func TestHasDrift_TagAdded(t *testing.T) {
	spec := vpc.VPCSpec{
		EnableDnsSupport: true,
		Tags:             map[string]string{"env": "prod"},
	}
	obs := vpc.ObservedState{
		State:            "available",
		EnableDnsSupport: true,
		Tags:             map[string]string{},
	}
	assert.True(t, vpc.HasDrift(spec, obs))
}

func TestHasDrift_TagRemoved(t *testing.T) {
	spec := vpc.VPCSpec{
		EnableDnsSupport: true,
		Tags:             map[string]string{},
	}
	obs := vpc.ObservedState{
		State:            "available",
		EnableDnsSupport: true,
		Tags:             map[string]string{"env": "prod"},
	}
	assert.True(t, vpc.HasDrift(spec, obs))
}

func TestHasDrift_TagValueChanged(t *testing.T) {
	spec := vpc.VPCSpec{
		EnableDnsSupport: true,
		Tags:             map[string]string{"env": "prod"},
	}
	obs := vpc.ObservedState{
		State:            "available",
		EnableDnsSupport: true,
		Tags:             map[string]string{"env": "dev"},
	}
	assert.True(t, vpc.HasDrift(spec, obs))
}

func TestHasDrift_IgnoresPendingState(t *testing.T) {
	spec := vpc.VPCSpec{
		EnableDnsHostnames: true,
		EnableDnsSupport:   false,
	}
	obs := vpc.ObservedState{
		State:              "pending",
		EnableDnsHostnames: false,
		EnableDnsSupport:   true,
	}
	assert.False(t, vpc.HasDrift(spec, obs))
}

func TestHasDrift_IgnoresImmutableCidr(t *testing.T) {
	spec := vpc.VPCSpec{
		CidrBlock:        "10.0.0.0/16",
		EnableDnsSupport: true,
		Tags:             map[string]string{},
	}
	obs := vpc.ObservedState{
		State:            "available",
		CidrBlock:        "172.31.0.0/16",
		EnableDnsSupport: true,
		Tags:             map[string]string{},
	}
	assert.False(t, vpc.HasDrift(spec, obs))
}

func TestHasDrift_IgnoresImmutableTenancy(t *testing.T) {
	spec := vpc.VPCSpec{
		InstanceTenancy:  "dedicated",
		EnableDnsSupport: true,
		Tags:             map[string]string{},
	}
	obs := vpc.ObservedState{
		State:            "available",
		InstanceTenancy:  "default",
		EnableDnsSupport: true,
		Tags:             map[string]string{},
	}
	assert.False(t, vpc.HasDrift(spec, obs))
}

func TestHasDrift_NilAndEmptyTags(t *testing.T) {
	spec := vpc.VPCSpec{EnableDnsSupport: true, Tags: nil}
	obs := vpc.ObservedState{State: "available", EnableDnsSupport: true, Tags: map[string]string{}}
	assert.False(t, vpc.HasDrift(spec, obs))

	spec2 := vpc.VPCSpec{EnableDnsSupport: true, Tags: map[string]string{}}
	obs2 := vpc.ObservedState{State: "available", EnableDnsSupport: true, Tags: nil}
	assert.False(t, vpc.HasDrift(spec2, obs2))
}

func TestHasDrift_IgnoresPraxisTags(t *testing.T) {
	spec := vpc.VPCSpec{
		EnableDnsSupport: true,
		Tags:             map[string]string{"env": "dev"},
	}
	obs := vpc.ObservedState{
		State:            "available",
		EnableDnsSupport: true,
		Tags:             map[string]string{"env": "dev", "praxis:managed-key": "us-east-1~my-vpc"},
	}
	assert.False(t, vpc.HasDrift(spec, obs))
}

func TestComputeFieldDiffs_NoChanges(t *testing.T) {
	diffs := vpc.ComputeFieldDiffs(
		vpc.VPCSpec{
			CidrBlock:          "10.0.0.0/16",
			EnableDnsHostnames: true,
			EnableDnsSupport:   true,
			InstanceTenancy:    "default",
			Tags:               map[string]string{"env": "prod"},
		},
		vpc.ObservedState{
			CidrBlock:          "10.0.0.0/16",
			EnableDnsHostnames: true,
			EnableDnsSupport:   true,
			InstanceTenancy:    "default",
			Tags:               map[string]string{"env": "prod"},
		},
	)
	assert.Empty(t, diffs)
}

func TestComputeFieldDiffs_MutableChanges(t *testing.T) {
	diffs := vpc.ComputeFieldDiffs(
		vpc.VPCSpec{
			CidrBlock:          "10.0.0.0/16",
			EnableDnsHostnames: true,
			EnableDnsSupport:   false,
			InstanceTenancy:    "default",
			Tags:               map[string]string{"env": "prod"},
		},
		vpc.ObservedState{
			CidrBlock:          "10.0.0.0/16",
			EnableDnsHostnames: false,
			EnableDnsSupport:   true,
			InstanceTenancy:    "default",
			Tags:               map[string]string{"env": "dev"},
		},
	)

	paths := map[string]bool{}
	for _, d := range diffs {
		paths[d.Path] = true
	}
	assert.True(t, paths["spec.enableDnsHostnames"])
	assert.True(t, paths["spec.enableDnsSupport"])
	assert.True(t, paths["tags.env"])
}

func TestComputeFieldDiffs_ImmutableFields(t *testing.T) {
	diffs := vpc.ComputeFieldDiffs(
		vpc.VPCSpec{
			CidrBlock:       "10.0.0.0/16",
			InstanceTenancy: "dedicated",
		},
		vpc.ObservedState{
			CidrBlock:       "172.31.0.0/16",
			InstanceTenancy: "default",
		},
	)

	paths := map[string]bool{}
	for _, d := range diffs {
		paths[d.Path] = true
	}
	assert.True(t, paths["spec.cidrBlock (immutable, requires replacement)"])
	assert.True(t, paths["spec.instanceTenancy (immutable, ignored)"])
}

func TestComputeFieldDiffs_IgnoresPraxisTags(t *testing.T) {
	diffs := vpc.ComputeFieldDiffs(
		vpc.VPCSpec{Tags: map[string]string{"env": "dev"}},
		vpc.ObservedState{Tags: map[string]string{"env": "dev", "praxis:managed-key": "k"}},
	)
	for _, d := range diffs {
		assert.NotContains(t, d.Path, "praxis:")
	}
}

func TestComputeFieldDiffs_TagAddedRemovedChanged(t *testing.T) {
	diffs := vpc.ComputeFieldDiffs(
		vpc.VPCSpec{Tags: map[string]string{"env": "prod", "team": "infra"}},
		vpc.ObservedState{Tags: map[string]string{"env": "dev", "old": "value"}},
	)

	paths := map[string]bool{}
	for _, d := range diffs {
		paths[d.Path] = true
	}
	assert.True(t, paths["tags.env"], "changed tag value")
	assert.True(t, paths["tags.team"], "added tag")
	assert.True(t, paths["tags.old"], "removed tag")
}

func TestComputeFieldDiffs_DefaultTenancyMatches(t *testing.T) {
	diffs := vpc.ComputeFieldDiffs(
		vpc.VPCSpec{InstanceTenancy: ""},
		vpc.ObservedState{InstanceTenancy: "default"},
	)
	for _, d := range diffs {
		assert.NotContains(t, d.Path, "instanceTenancy")
	}
}
