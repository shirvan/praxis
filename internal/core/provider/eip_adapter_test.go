package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/praxiscloud/praxis/internal/drivers/eip"
)

func TestEIPAdapter_DecodeSpecAndBuildKey(t *testing.T) {
	adapter := NewEIPAdapter()
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"ElasticIP",
		"metadata":{"name":"web-eip"},
		"spec":{
			"region":"us-east-1",
			"tags":{"env":"dev"}
		}
	}`)

	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~web-eip", key)
	assert.Equal(t, KeyScopeRegion, adapter.Scope())

	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed, ok := decoded.(eip.ElasticIPSpec)
	require.True(t, ok)
	assert.Equal(t, "us-east-1", typed.Region)
	assert.Equal(t, "vpc", typed.Domain)
	assert.Equal(t, "web-eip", typed.Tags["Name"])
	assert.Equal(t, "dev", typed.Tags["env"])
}

func TestEIPAdapter_BuildImportKey(t *testing.T) {
	adapter := NewEIPAdapter()
	key, err := adapter.BuildImportKey("us-east-1", "eipalloc-0abc123")
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~eipalloc-0abc123", key)
}

func TestEIPAdapter_Kind(t *testing.T) {
	adapter := NewEIPAdapter()
	assert.Equal(t, eip.ServiceName, adapter.Kind())
	assert.Equal(t, eip.ServiceName, adapter.ServiceName())
}

func TestEIPAdapter_Scope(t *testing.T) {
	adapter := NewEIPAdapter()
	assert.Equal(t, KeyScopeRegion, adapter.Scope())
}

func TestEIPAdapter_NormalizeOutputs(t *testing.T) {
	adapter := NewEIPAdapter()
	out, err := adapter.NormalizeOutputs(eip.ElasticIPOutputs{
		AllocationId:       "eipalloc-123",
		PublicIp:           "203.0.113.10",
		ARN:                "arn:aws:ec2:us-east-1:123456789012:elastic-ip/eipalloc-123",
		Domain:             "vpc",
		NetworkBorderGroup: "us-east-1",
	})
	require.NoError(t, err)
	assert.Equal(t, "eipalloc-123", out["allocationId"])
	assert.Equal(t, "203.0.113.10", out["publicIp"])
	assert.Equal(t, "vpc", out["domain"])
	assert.Equal(t, "us-east-1", out["networkBorderGroup"])
	assert.Equal(t, "arn:aws:ec2:us-east-1:123456789012:elastic-ip/eipalloc-123", out["arn"])
}
