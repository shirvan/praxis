package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers/ecrpolicy"
)

func TestECRLifecyclePolicyAdapter_DecodeSpecAndBuildKey(t *testing.T) {
	adapter := NewECRLifecyclePolicyAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"ECRLifecyclePolicy",
		"metadata":{"name":"my-repo-lcp"},
		"spec":{
			"region":"us-east-1",
			"repositoryName":"my-repo",
			"lifecyclePolicyText":"{\"rules\":[]}"
		}
	}`)

	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~my-repo", key)

	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed, ok := decoded.(ecrpolicy.ECRLifecyclePolicySpec)
	require.True(t, ok)
	assert.Equal(t, "us-east-1", typed.Region)
	assert.Equal(t, "my-repo", typed.RepositoryName)
	assert.Equal(t, `{"rules":[]}`, typed.LifecyclePolicyText)
}

func TestECRLifecyclePolicyAdapter_BuildImportKey(t *testing.T) {
	adapter := NewECRLifecyclePolicyAdapterWithAuth(nil)
	key, err := adapter.BuildImportKey("us-east-1", "my-repo")
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~my-repo", key)
}

func TestECRLifecyclePolicyAdapter_Kind(t *testing.T) {
	adapter := NewECRLifecyclePolicyAdapterWithAuth(nil)
	assert.Equal(t, ecrpolicy.ServiceName, adapter.Kind())
	assert.Equal(t, ecrpolicy.ServiceName, adapter.ServiceName())
}

func TestECRLifecyclePolicyAdapter_Scope(t *testing.T) {
	adapter := NewECRLifecyclePolicyAdapterWithAuth(nil)
	assert.Equal(t, KeyScopeCustom, adapter.Scope())
}

func TestECRLifecyclePolicyAdapter_NormalizeOutputs(t *testing.T) {
	adapter := NewECRLifecyclePolicyAdapterWithAuth(nil)
	out, err := adapter.NormalizeOutputs(ecrpolicy.ECRLifecyclePolicyOutputs{
		RepositoryName: "my-repo",
		RepositoryArn:  "arn:aws:ecr:us-east-1:123456789012:repository/my-repo",
		RegistryId:     "123456789012",
	})
	require.NoError(t, err)
	assert.Equal(t, "my-repo", out["repositoryName"])
	assert.Equal(t, "arn:aws:ecr:us-east-1:123456789012:repository/my-repo", out["repositoryArn"])
	assert.Equal(t, "123456789012", out["registryId"])
}

func TestECRLifecyclePolicyAdapter_NormalizeOutputs_Minimal(t *testing.T) {
	adapter := NewECRLifecyclePolicyAdapterWithAuth(nil)
	out, err := adapter.NormalizeOutputs(ecrpolicy.ECRLifecyclePolicyOutputs{
		RepositoryName: "my-repo",
	})
	require.NoError(t, err)
	assert.Equal(t, "my-repo", out["repositoryName"])
	_, hasArn := out["repositoryArn"]
	assert.False(t, hasArn) // empty strings are not included
}

func TestECRLifecyclePolicyAdapter_DecodeSpec_MissingRegion(t *testing.T) {
	adapter := NewECRLifecyclePolicyAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"ECRLifecyclePolicy",
		"metadata":{"name":"lcp"},
		"spec":{"repositoryName":"my-repo","lifecyclePolicyText":"{\"rules\":[]}"}
	}`)
	_, err := adapter.DecodeSpec(raw)
	assert.Error(t, err)
}

func TestECRLifecyclePolicyAdapter_DecodeSpec_MissingRepositoryName(t *testing.T) {
	adapter := NewECRLifecyclePolicyAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"ECRLifecyclePolicy",
		"metadata":{"name":"lcp"},
		"spec":{"region":"us-east-1","lifecyclePolicyText":"{\"rules\":[]}"}
	}`)
	_, err := adapter.DecodeSpec(raw)
	assert.Error(t, err)
}
