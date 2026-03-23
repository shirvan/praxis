package ec2_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/shirvan/praxis/internal/drivers/ec2"
)

func TestHasDrift_NoDrift(t *testing.T) {
	spec := ec2.EC2InstanceSpec{
		InstanceType:     "t3.micro",
		SecurityGroupIds: []string{"sg-a", "sg-b"},
		Monitoring:       true,
		Tags:             map[string]string{"Name": "web", "env": "dev"},
	}
	obs := ec2.ObservedState{
		InstanceType:     "t3.micro",
		SecurityGroupIds: []string{"sg-b", "sg-a"},
		Monitoring:       true,
		State:            "running",
		Tags:             map[string]string{"Name": "web", "env": "dev", "praxis:managed-key": "us-east-1~web"},
	}
	assert.False(t, ec2.HasDrift(spec, obs))
}

func TestHasDrift_InstanceType(t *testing.T) {
	spec := ec2.EC2InstanceSpec{InstanceType: "t3.small"}
	obs := ec2.ObservedState{InstanceType: "t3.micro", State: "running"}
	assert.True(t, ec2.HasDrift(spec, obs))
}

func TestHasDrift_SecurityGroupsChanged(t *testing.T) {
	spec := ec2.EC2InstanceSpec{
		InstanceType:     "t3.micro",
		SecurityGroupIds: []string{"sg-a", "sg-b", "sg-c"},
		Tags:             map[string]string{},
	}
	obs := ec2.ObservedState{
		InstanceType:     "t3.micro",
		SecurityGroupIds: []string{"sg-a", "sg-b"},
		State:            "running",
		Tags:             map[string]string{},
	}
	assert.True(t, ec2.HasDrift(spec, obs))
}

func TestHasDrift_SecurityGroupsReordered(t *testing.T) {
	spec := ec2.EC2InstanceSpec{
		InstanceType:     "t3.micro",
		SecurityGroupIds: []string{"sg-b", "sg-a"},
		Tags:             map[string]string{},
	}
	obs := ec2.ObservedState{
		InstanceType:     "t3.micro",
		SecurityGroupIds: []string{"sg-a", "sg-b"},
		State:            "running",
		Tags:             map[string]string{},
	}
	assert.False(t, ec2.HasDrift(spec, obs))
}

func TestHasDrift_MonitoringChanged(t *testing.T) {
	spec := ec2.EC2InstanceSpec{InstanceType: "t3.micro", Monitoring: true, Tags: map[string]string{}}
	obs := ec2.ObservedState{InstanceType: "t3.micro", Monitoring: false, State: "running", Tags: map[string]string{}}
	assert.True(t, ec2.HasDrift(spec, obs))
}

func TestHasDrift_TagAdded(t *testing.T) {
	spec := ec2.EC2InstanceSpec{InstanceType: "t3.micro", Tags: map[string]string{"env": "prod"}}
	obs := ec2.ObservedState{InstanceType: "t3.micro", State: "running", Tags: map[string]string{}}
	assert.True(t, ec2.HasDrift(spec, obs))
}

func TestHasDrift_TagRemoved(t *testing.T) {
	spec := ec2.EC2InstanceSpec{InstanceType: "t3.micro", Tags: map[string]string{}}
	obs := ec2.ObservedState{InstanceType: "t3.micro", State: "running", Tags: map[string]string{"env": "prod"}}
	assert.True(t, ec2.HasDrift(spec, obs))
}

func TestHasDrift_TagValueChanged(t *testing.T) {
	spec := ec2.EC2InstanceSpec{InstanceType: "t3.micro", Tags: map[string]string{"env": "prod"}}
	obs := ec2.ObservedState{InstanceType: "t3.micro", State: "running", Tags: map[string]string{"env": "dev"}}
	assert.True(t, ec2.HasDrift(spec, obs))
}

func TestHasDrift_IgnoresTransientState(t *testing.T) {
	spec := ec2.EC2InstanceSpec{InstanceType: "t3.small"}
	obs := ec2.ObservedState{InstanceType: "t3.micro", State: "pending"}
	assert.False(t, ec2.HasDrift(spec, obs))
}

func TestHasDrift_TerminatedInstanceNoDrift(t *testing.T) {
	spec := ec2.EC2InstanceSpec{InstanceType: "t3.small"}
	obs := ec2.ObservedState{InstanceType: "t3.micro", State: "terminated"}
	assert.False(t, ec2.HasDrift(spec, obs))
}

func TestTagsMatch_NilAndEmpty(t *testing.T) {
	// HasDrift should treat both nil and empty tags as equivalent (no drift)
	spec := ec2.EC2InstanceSpec{InstanceType: "t3.micro", Tags: nil}
	obs := ec2.ObservedState{InstanceType: "t3.micro", State: "running", Tags: map[string]string{}}
	assert.False(t, ec2.HasDrift(spec, obs))

	spec2 := ec2.EC2InstanceSpec{InstanceType: "t3.micro", Tags: map[string]string{}}
	obs2 := ec2.ObservedState{InstanceType: "t3.micro", State: "running", Tags: nil}
	assert.False(t, ec2.HasDrift(spec2, obs2))
}

func TestTagsMatch_IgnoresPraxisTags(t *testing.T) {
	spec := ec2.EC2InstanceSpec{InstanceType: "t3.micro", Tags: map[string]string{"env": "dev"}}
	obs := ec2.ObservedState{
		InstanceType: "t3.micro",
		State:        "running",
		Tags:         map[string]string{"env": "dev", "praxis:managed-key": "us-east-1~web"},
	}
	assert.False(t, ec2.HasDrift(spec, obs))
}

func TestComputeFieldDiffs_ImmutableFields(t *testing.T) {
	diffs := ec2.ComputeFieldDiffs(
		ec2.EC2InstanceSpec{
			ImageId:  "ami-new",
			SubnetId: "subnet-new",
			KeyName:  "key-new",
		},
		ec2.ObservedState{
			ImageId:  "ami-old",
			SubnetId: "subnet-old",
			KeyName:  "key-old",
		},
	)
	paths := map[string]bool{}
	for _, d := range diffs {
		paths[d.Path] = true
	}
	assert.True(t, paths["spec.imageId (immutable, ignored)"])
	assert.True(t, paths["spec.subnetId (immutable, ignored)"])
	assert.True(t, paths["spec.keyName (immutable, ignored)"])
}

func TestComputeFieldDiffs_IgnoresPraxisTags(t *testing.T) {
	diffs := ec2.ComputeFieldDiffs(
		ec2.EC2InstanceSpec{Tags: map[string]string{"env": "dev"}},
		ec2.ObservedState{Tags: map[string]string{"env": "dev", "praxis:managed-key": "k"}},
	)
	for _, d := range diffs {
		assert.NotContains(t, d.Path, "praxis:")
	}
}

func TestComputeFieldDiffs_ReportsMutableAndImmutable(t *testing.T) {
	diffs := ec2.ComputeFieldDiffs(
		ec2.EC2InstanceSpec{
			ImageId:          "ami-0123456789abcdef0",
			InstanceType:     "t3.small",
			SubnetId:         "subnet-new",
			KeyName:          "key-new",
			SecurityGroupIds: []string{"sg-b"},
			Monitoring:       true,
			Tags:             map[string]string{"env": "prod"},
		},
		ec2.ObservedState{
			ImageId:          "ami-aaaaaaaa",
			InstanceType:     "t3.micro",
			SubnetId:         "subnet-old",
			KeyName:          "key-old",
			SecurityGroupIds: []string{"sg-a"},
			Monitoring:       false,
			Tags:             map[string]string{"env": "dev", "praxis:managed-key": "k"},
		},
	)
	assert.NotEmpty(t, diffs)

	paths := map[string]bool{}
	for _, diff := range diffs {
		paths[diff.Path] = true
	}
	assert.True(t, paths["spec.instanceType"])
	assert.True(t, paths["spec.securityGroupIds"])
	assert.True(t, paths["spec.monitoring"])
	assert.True(t, paths["spec.imageId (immutable, ignored)"])
	assert.True(t, paths["spec.subnetId (immutable, ignored)"])
	assert.True(t, paths["spec.keyName (immutable, ignored)"])
	assert.True(t, paths["tags.env"])
}
