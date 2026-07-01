package ecscluster

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/shirvan/praxis/pkg/types"
)

func TestServiceName(t *testing.T) {
	drv := NewECSClusterDriver(nil)
	assert.Equal(t, "ECSCluster", drv.ServiceName())
}

func baseSpec() ECSClusterSpec {
	return ECSClusterSpec{
		Region:            "us-east-1",
		Name:              "prod",
		ContainerInsights: "enabled",
	}
}

func TestApplyDefaults_TrimsAndInitializes(t *testing.T) {
	spec := applyDefaults(ECSClusterSpec{
		Region: "  us-east-1  ",
		Name:   "  prod  ",
	})
	assert.Equal(t, "us-east-1", spec.Region)
	assert.Equal(t, "prod", spec.Name)
	assert.Equal(t, "disabled", spec.ContainerInsights, "unset containerInsights defaults to disabled")
	assert.NotNil(t, spec.Tags)
}

func TestValidateSpec(t *testing.T) {
	assert.NoError(t, validateSpec(baseSpec()))

	noRegion := baseSpec()
	noRegion.Region = ""
	assert.Error(t, validateSpec(noRegion))

	noName := baseSpec()
	noName.Name = ""
	assert.Error(t, validateSpec(noName))

	badInsights := baseSpec()
	badInsights.ContainerInsights = "bogus"
	assert.Error(t, validateSpec(badInsights))

	disabled := baseSpec()
	disabled.ContainerInsights = "disabled"
	assert.NoError(t, validateSpec(disabled))
}

func TestSpecFromObserved_FiltersPraxisTags(t *testing.T) {
	obs := ObservedState{
		Name:              "prod",
		ContainerInsights: "enabled",
		CapacityProviders: []string{"FARGATE"},
		Tags:              map[string]string{"env": "prod", "praxis:managed-key": "us-east-1~prod"},
	}
	spec := specFromObserved(obs)
	assert.Equal(t, "prod", spec.Name)
	assert.Equal(t, "enabled", spec.ContainerInsights)
	assert.Equal(t, []string{"FARGATE"}, spec.CapacityProviders)
	assert.Equal(t, map[string]string{"env": "prod"}, spec.Tags, "praxis: tags should be filtered out")
}

func TestSpecFromObserved_DefaultsContainerInsights(t *testing.T) {
	spec := specFromObserved(ObservedState{Name: "prod"})
	assert.Equal(t, "disabled", spec.ContainerInsights)
}

func TestOutputsFromObserved(t *testing.T) {
	out := outputsFromObserved(ObservedState{
		ARN:    "arn:aws:ecs:us-east-1:123456789012:cluster/prod",
		Name:   "prod",
		Status: "ACTIVE",
	})
	assert.Equal(t, "arn:aws:ecs:us-east-1:123456789012:cluster/prod", out.ARN)
	assert.Equal(t, "prod", out.Name)
	assert.Equal(t, "ACTIVE", out.Status)
}

func TestDefaultImportMode(t *testing.T) {
	assert.Equal(t, types.ModeObserved, defaultImportMode(""))
	assert.Equal(t, types.ModeManaged, defaultImportMode(types.ModeManaged))
	assert.Equal(t, types.ModeObserved, defaultImportMode(types.ModeObserved))
}

func TestTagDiff_AddsRemovesPreservesManagedKey(t *testing.T) {
	desired := map[string]string{"env": "prod", "team": "core"}
	observed := map[string]string{"env": "dev", "old": "1", "praxis:managed-key": "k"}
	toAdd, toRemove := tagDiff(desired, observed, "k")

	assert.Equal(t, "prod", toAdd["env"], "changed value should be re-tagged")
	assert.Equal(t, "core", toAdd["team"], "new tag should be added")
	assert.NotContains(t, toAdd, "praxis:managed-key", "managed key already present, not re-added")
	assert.Equal(t, []string{"old"}, toRemove, "stale tag should be removed; managed key preserved")
}

func TestTagDiff_ManagedKeyNeverDiffed(t *testing.T) {
	// The managed-key marker is synthesized on both the desired and observed
	// sides, so it must never surface as an add or a removal — reconciling it as
	// drift would fight the create-time tagging on every pass.
	toAdd, toRemove := tagDiff(map[string]string{}, map[string]string{}, "us-east-1~prod")
	assert.NotContains(t, toAdd, "praxis:managed-key")
	assert.Empty(t, toAdd)
	assert.Empty(t, toRemove)
}
