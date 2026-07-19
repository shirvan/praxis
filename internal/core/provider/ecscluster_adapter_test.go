package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers/ecscluster"
)

func TestECSClusterAdapter_BuildKeyAndDecodeSpec(t *testing.T) {
	adapter := NewECSClusterAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/alpha",
		"kind":"ECSCluster",
		"metadata":{"name":"services"},
		"spec":{"region":"us-east-1","containerInsights":"enabled","capacityProviders":["FARGATE"],"tags":{"env":"dev"}}
	}`)

	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~services", key)
	assert.Equal(t, KeyScopeRegion, adapter.Scope())

	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed, ok := decoded.(ecscluster.ECSClusterSpec)
	require.True(t, ok)
	assert.Equal(t, "us-east-1", typed.Region)
	assert.Equal(t, "services", typed.Name)
	assert.Equal(t, "enabled", typed.ContainerInsights)
	assert.Equal(t, []string{"FARGATE"}, typed.CapacityProviders)
	assert.Equal(t, map[string]string{"env": "dev"}, typed.Tags)
}

func TestECSClusterAdapter_DecodeSpec_MissingRegion(t *testing.T) {
	adapter := NewECSClusterAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/alpha",
		"kind":"ECSCluster",
		"metadata":{"name":"services"},
		"spec":{}
	}`)

	_, err := adapter.DecodeSpec(raw)
	assert.ErrorContains(t, err, "spec.region is required")
}

func TestECSClusterAdapter_BuildImportKey(t *testing.T) {
	adapter := NewECSClusterAdapterWithAuth(nil)
	key, err := adapter.BuildImportKey("us-east-1", "services")
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~services", key)
}

func TestECSClusterAdapter_NormalizeOutputs(t *testing.T) {
	adapter := NewECSClusterAdapterWithAuth(nil)
	out, err := adapter.NormalizeOutputs(ecscluster.ECSClusterOutputs{
		ARN:    "arn:aws:ecs:us-east-1:123:cluster/services",
		Name:   "services",
		Status: "ACTIVE",
	})
	require.NoError(t, err)
	assert.Equal(t, "arn:aws:ecs:us-east-1:123:cluster/services", out["arn"])
	assert.Equal(t, "services", out["name"])
	assert.Equal(t, "ACTIVE", out["status"])
}

func TestECSClusterAdapter_NormalizeOutputs_OmitsEmptyARN(t *testing.T) {
	adapter := NewECSClusterAdapterWithAuth(nil)
	out, err := adapter.NormalizeOutputs(ecscluster.ECSClusterOutputs{
		Name:   "services",
		Status: "ACTIVE",
	})
	require.NoError(t, err)
	assert.NotContains(t, out, "arn")
}
