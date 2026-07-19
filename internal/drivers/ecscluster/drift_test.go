package ecscluster

import (
	"github.com/shirvan/praxis/internal/drivers"
	"testing"

	"github.com/stretchr/testify/assert"
)

func inSyncSpecObserved() (ECSClusterSpec, ObservedState) {
	spec := ECSClusterSpec{
		Region:            "us-east-1",
		Name:              "prod",
		ContainerInsights: "enabled",
		CapacityProviders: []string{"FARGATE", "FARGATE_SPOT"},
		Tags:              map[string]string{"env": "prod"},
	}
	observed := ObservedState{
		ARN:               "arn:aws:ecs:us-east-1:123456789012:cluster/prod",
		Name:              "prod",
		Status:            "ACTIVE",
		ContainerInsights: "enabled",
		CapacityProviders: []string{"FARGATE_SPOT", "FARGATE"}, // order-insensitive
		Tags:              map[string]string{"env": "prod", "praxis:managed-key": "us-east-1~prod"},
	}
	return spec, observed
}

func TestHasDrift_InSync(t *testing.T) {
	spec, observed := inSyncSpecObserved()
	assert.False(t, HasDrift(spec, observed), "in-sync spec/observed should not drift")
	assert.Empty(t, ComputeFieldDiffs(spec, observed))
}

func TestHasDrift_ContainerInsights(t *testing.T) {
	spec, observed := inSyncSpecObserved()
	spec.ContainerInsights = "disabled"
	assert.True(t, HasDrift(spec, observed))
	assert.Contains(t, pathsOf(ComputeFieldDiffs(spec, observed)), "spec.containerInsights")
}

func TestHasDrift_EmptyContainerInsightsMatchesDisabled(t *testing.T) {
	spec, observed := inSyncSpecObserved()
	spec.ContainerInsights = ""
	observed.ContainerInsights = "disabled"
	assert.False(t, HasDrift(spec, observed), "an unset desired value normalizes to the AWS default (disabled)")
}

func TestHasDrift_CapacityProviders(t *testing.T) {
	spec, observed := inSyncSpecObserved()
	spec.CapacityProviders = []string{"FARGATE"}
	assert.True(t, HasDrift(spec, observed))
	assert.Contains(t, pathsOf(ComputeFieldDiffs(spec, observed)), "spec.capacityProviders")
}

func TestHasDrift_Tags(t *testing.T) {
	spec, observed := inSyncSpecObserved()
	spec.Tags = map[string]string{"env": "staging"}
	assert.True(t, HasDrift(spec, observed))
	assert.Contains(t, pathsOf(ComputeFieldDiffs(spec, observed)), "tags.env")
}

func TestNormalizeContainerInsights(t *testing.T) {
	assert.Equal(t, "disabled", normalizeContainerInsights(""))
	assert.Equal(t, "enabled", normalizeContainerInsights("enabled"))
}

func TestStringSetEqual(t *testing.T) {
	assert.True(t, stringSetEqual([]string{"a", "b"}, []string{"b", "a"}))
	assert.False(t, stringSetEqual([]string{"a"}, []string{"a", "b"}))
	assert.True(t, stringSetEqual(nil, nil))
}

func pathsOf(diffs []drivers.FieldDiff) []string {
	out := make([]string, 0, len(diffs))
	for _, d := range diffs {
		out = append(out, d.Path)
	}
	return out
}
