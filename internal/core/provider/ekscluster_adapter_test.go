package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers/ekscluster"
)

func TestEKSClusterAdapter_BuildKeyAndDecodeSpec(t *testing.T) {
	adapter := NewEKSClusterAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"EKSCluster",
		"metadata":{"name":"prod-cluster"},
		"spec":{"region":"us-east-1","roleArn":"arn:aws:iam::123:role/eks","subnetIds":["subnet-1","subnet-2"],"endpointPublicAccess":true,"tags":{"env":"prod"}}
	}`)

	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~prod-cluster", key)
	assert.Equal(t, KeyScopeRegion, adapter.Scope())

	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed, ok := decoded.(ekscluster.EKSClusterSpec)
	require.True(t, ok)
	assert.Equal(t, "us-east-1", typed.Region)
	assert.Equal(t, "prod-cluster", typed.Name)
	assert.Equal(t, "arn:aws:iam::123:role/eks", typed.RoleArn)
	assert.Equal(t, []string{"subnet-1", "subnet-2"}, typed.SubnetIds)
	assert.True(t, typed.EndpointPublicAccess)
	assert.Equal(t, map[string]string{"env": "prod"}, typed.Tags)
}

func TestEKSClusterAdapter_DecodeSpec_MissingRegion(t *testing.T) {
	adapter := NewEKSClusterAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"kind":"EKSCluster",
		"metadata":{"name":"prod-cluster"},
		"spec":{"roleArn":"arn:aws:iam::123:role/eks"}
	}`)

	_, err := adapter.DecodeSpec(raw)
	assert.ErrorContains(t, err, "spec.region is required")
}

func TestEKSClusterAdapter_BuildImportKey(t *testing.T) {
	adapter := NewEKSClusterAdapterWithAuth(nil)
	key, err := adapter.BuildImportKey("us-east-1", "prod-cluster")
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~prod-cluster", key)
}

func TestEKSClusterAdapter_NormalizeOutputs(t *testing.T) {
	adapter := NewEKSClusterAdapterWithAuth(nil)
	out, err := adapter.NormalizeOutputs(ekscluster.EKSClusterOutputs{
		ARN:             "arn:aws:eks:us-east-1:123:cluster/prod-cluster",
		Name:            "prod-cluster",
		Status:          "ACTIVE",
		Version:         "1.29",
		PlatformVersion: "eks.5",
		Endpoint:        "https://example.eks.amazonaws.com",
	})
	require.NoError(t, err)
	assert.Equal(t, "arn:aws:eks:us-east-1:123:cluster/prod-cluster", out["arn"])
	assert.Equal(t, "prod-cluster", out["name"])
	assert.Equal(t, "ACTIVE", out["status"])
	assert.Equal(t, "1.29", out["version"])
	assert.Equal(t, "eks.5", out["platformVersion"])
	assert.Equal(t, "https://example.eks.amazonaws.com", out["endpoint"])
}

func TestEKSClusterAdapter_NormalizeOutputs_OmitsEmptyOptionals(t *testing.T) {
	adapter := NewEKSClusterAdapterWithAuth(nil)
	out, err := adapter.NormalizeOutputs(ekscluster.EKSClusterOutputs{
		Name:   "prod-cluster",
		Status: "ACTIVE",
	})
	require.NoError(t, err)
	assert.NotContains(t, out, "arn")
	assert.NotContains(t, out, "platformVersion")
	assert.NotContains(t, out, "endpoint")
}
