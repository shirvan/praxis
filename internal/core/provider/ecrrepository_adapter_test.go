package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers/ecrrepo"
)

func TestECRRepositoryAdapter_DecodeSpecAndBuildKey(t *testing.T) {
	adapter := NewECRRepositoryAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"ECRRepository",
		"metadata":{"name":"my-app"},
		"spec":{
			"region":"us-east-1",
			"imageTagMutability":"IMMUTABLE",
			"tags":{"env":"dev"}
		}
	}`)

	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~my-app", key)

	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed, ok := decoded.(ecrrepo.ECRRepositorySpec)
	require.True(t, ok)
	assert.Equal(t, "us-east-1", typed.Region)
	assert.Equal(t, "my-app", typed.RepositoryName)
	assert.Equal(t, "IMMUTABLE", typed.ImageTagMutability)
	assert.Equal(t, "dev", typed.Tags["env"])
}

func TestECRRepositoryAdapter_BuildImportKey(t *testing.T) {
	adapter := NewECRRepositoryAdapterWithAuth(nil)
	key, err := adapter.BuildImportKey("us-east-1", "my-app")
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~my-app", key)
}

func TestECRRepositoryAdapter_Kind(t *testing.T) {
	adapter := NewECRRepositoryAdapterWithAuth(nil)
	assert.Equal(t, ecrrepo.ServiceName, adapter.Kind())
	assert.Equal(t, ecrrepo.ServiceName, adapter.ServiceName())
}

func TestECRRepositoryAdapter_Scope(t *testing.T) {
	adapter := NewECRRepositoryAdapterWithAuth(nil)
	assert.Equal(t, KeyScopeRegion, adapter.Scope())
}

func TestECRRepositoryAdapter_NormalizeOutputs(t *testing.T) {
	adapter := NewECRRepositoryAdapterWithAuth(nil)
	out, err := adapter.NormalizeOutputs(ecrrepo.ECRRepositoryOutputs{
		RepositoryArn:  "arn:aws:ecr:us-east-1:123456789012:repository/my-app",
		RepositoryName: "my-app",
		RepositoryUri:  "123456789012.dkr.ecr.us-east-1.amazonaws.com/my-app",
		RegistryId:     "123456789012",
	})
	require.NoError(t, err)
	assert.Equal(t, "arn:aws:ecr:us-east-1:123456789012:repository/my-app", out["repositoryArn"])
	assert.Equal(t, "my-app", out["repositoryName"])
	assert.Equal(t, "123456789012.dkr.ecr.us-east-1.amazonaws.com/my-app", out["repositoryUri"])
	assert.Equal(t, "123456789012", out["registryId"])
}

func TestECRRepositoryAdapter_DecodeSpec_MissingRegion(t *testing.T) {
	adapter := NewECRRepositoryAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"ECRRepository",
		"metadata":{"name":"my-app"},
		"spec":{"tags":{}}
	}`)
	_, err := adapter.DecodeSpec(raw)
	assert.Error(t, err)
}

func TestECRRepositoryAdapter_DecodeSpec_MissingName(t *testing.T) {
	adapter := NewECRRepositoryAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"ECRRepository",
		"metadata":{"name":""},
		"spec":{"region":"us-east-1"}
	}`)
	_, err := adapter.DecodeSpec(raw)
	assert.Error(t, err)
}
