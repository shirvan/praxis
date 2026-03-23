package route53zone_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/shirvan/praxis/internal/drivers/route53zone"
)

func TestHasDrift_NoDrift(t *testing.T) {
	assert.False(t, route53zone.HasDrift(
		route53zone.HostedZoneSpec{Name: "example.com", Comment: "test zone", Tags: map[string]string{"env": "dev"}},
		route53zone.ObservedState{Name: "example.com", Comment: "test zone", Tags: map[string]string{"env": "dev", "praxis:managed-key": "example.com"}},
	))
}

func TestHasDrift_CommentChange(t *testing.T) {
	assert.True(t, route53zone.HasDrift(
		route53zone.HostedZoneSpec{Name: "example.com", Comment: "new comment", Tags: map[string]string{}},
		route53zone.ObservedState{Name: "example.com", Comment: "old comment", Tags: map[string]string{}},
	))
}

func TestHasDrift_TagChange(t *testing.T) {
	assert.True(t, route53zone.HasDrift(
		route53zone.HostedZoneSpec{Name: "example.com", Tags: map[string]string{"env": "prod"}},
		route53zone.ObservedState{Name: "example.com", Tags: map[string]string{"env": "dev"}},
	))
}

func TestHasDrift_VPCChange(t *testing.T) {
	assert.True(t, route53zone.HasDrift(
		route53zone.HostedZoneSpec{
			Name:      "example.com",
			IsPrivate: true,
			VPCs:      []route53zone.HostedZoneVPC{{VpcId: "vpc-123", VpcRegion: "us-east-1"}, {VpcId: "vpc-456", VpcRegion: "us-west-2"}},
			Tags:      map[string]string{},
		},
		route53zone.ObservedState{
			Name:      "example.com",
			IsPrivate: true,
			VPCs:      []route53zone.HostedZoneVPC{{VpcId: "vpc-123", VpcRegion: "us-east-1"}},
			Tags:      map[string]string{},
		},
	))
}

func TestHasDrift_PraxisTagsIgnored(t *testing.T) {
	assert.False(t, route53zone.HasDrift(
		route53zone.HostedZoneSpec{Name: "example.com", Tags: map[string]string{"env": "dev"}},
		route53zone.ObservedState{Name: "example.com", Tags: map[string]string{"env": "dev", "praxis:managed-key": "example.com"}},
	))
}

func TestComputeFieldDiffs_MultipleChanges(t *testing.T) {
	diffs := route53zone.ComputeFieldDiffs(
		route53zone.HostedZoneSpec{Name: "example.com", Comment: "new", Tags: map[string]string{"env": "prod", "team": "infra"}},
		route53zone.ObservedState{Name: "example.com", Comment: "old", Tags: map[string]string{"env": "dev", "praxis:managed-key": "x"}},
	)
	paths := map[string]bool{}
	for _, diff := range diffs {
		paths[diff.Path] = true
	}
	assert.True(t, paths["spec.comment"])
	assert.True(t, paths["tags.env"])
	assert.True(t, paths["tags.team"])
	for _, diff := range diffs {
		assert.NotContains(t, diff.Path, "praxis:")
	}
}

func TestComputeFieldDiffs_ImmutableNameReported(t *testing.T) {
	diffs := route53zone.ComputeFieldDiffs(
		route53zone.HostedZoneSpec{Name: "new.com", Tags: map[string]string{}},
		route53zone.ObservedState{Name: "old.com", Tags: map[string]string{}},
	)
	found := false
	for _, diff := range diffs {
		if diff.Path == "spec.name (immutable, ignored)" {
			found = true
		}
	}
	assert.True(t, found)
}
