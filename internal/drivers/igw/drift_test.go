package igw_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/shirvan/praxis/internal/drivers/igw"
)

func TestHasDrift_NoDrift(t *testing.T) {
	assert.False(t, igw.HasDrift(
		igw.IGWSpec{VpcId: "vpc-123", Tags: map[string]string{"env": "dev"}},
		igw.ObservedState{AttachedVpcId: "vpc-123", Tags: map[string]string{"env": "dev", "praxis:managed-key": "us-east-1~web-igw"}},
	))
}

func TestHasDrift_DetachedOrWrongVpc(t *testing.T) {
	assert.True(t, igw.HasDrift(igw.IGWSpec{VpcId: "vpc-123"}, igw.ObservedState{}))
	assert.True(t, igw.HasDrift(igw.IGWSpec{VpcId: "vpc-123"}, igw.ObservedState{AttachedVpcId: "vpc-999"}))
}

func TestHasDrift_TagChange(t *testing.T) {
	assert.True(t, igw.HasDrift(
		igw.IGWSpec{VpcId: "vpc-123", Tags: map[string]string{"env": "prod"}},
		igw.ObservedState{AttachedVpcId: "vpc-123", Tags: map[string]string{"env": "dev"}},
	))
}

func TestComputeFieldDiffs(t *testing.T) {
	diffs := igw.ComputeFieldDiffs(
		igw.IGWSpec{VpcId: "vpc-456", Tags: map[string]string{"env": "prod", "team": "infra"}},
		igw.ObservedState{AttachedVpcId: "vpc-123", Tags: map[string]string{"env": "dev", "praxis:managed-key": "x"}},
	)

	paths := map[string]bool{}
	for _, diff := range diffs {
		paths[diff.Path] = true
	}
	assert.True(t, paths["spec.vpcId"])
	assert.True(t, paths["tags.env"])
	assert.True(t, paths["tags.team"])
	for _, diff := range diffs {
		assert.NotContains(t, diff.Path, "praxis:")
	}
}
