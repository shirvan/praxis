package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers/igw"
)

func TestIGWAdapter_BuildKeyAndDecodeSpec(t *testing.T) {
	adapter := NewIGWAdapter()
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"InternetGateway",
		"metadata":{"name":"web-igw"},
		"spec":{"region":"us-east-1","vpcId":"vpc-123","tags":{"env":"dev"}}
	}`)

	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~web-igw", key)
	assert.Equal(t, KeyScopeRegion, adapter.Scope())

	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed, ok := decoded.(igw.IGWSpec)
	require.True(t, ok)
	assert.Equal(t, "us-east-1", typed.Region)
	assert.Equal(t, "vpc-123", typed.VpcId)
	assert.Equal(t, "web-igw", typed.Tags["Name"])
	assert.Equal(t, "dev", typed.Tags["env"])
}

func TestIGWAdapter_BuildImportKey(t *testing.T) {
	adapter := NewIGWAdapter()
	key, err := adapter.BuildImportKey("us-east-1", "igw-123")
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~igw-123", key)
}

func TestIGWAdapter_NormalizeOutputs(t *testing.T) {
	adapter := NewIGWAdapter()
	out, err := adapter.NormalizeOutputs(igw.IGWOutputs{
		InternetGatewayId: "igw-123",
		VpcId:             "vpc-123",
		OwnerId:           "123456789012",
		State:             "available",
	})
	require.NoError(t, err)
	assert.Equal(t, "igw-123", out["internetGatewayId"])
	assert.Equal(t, "vpc-123", out["vpcId"])
	assert.Equal(t, "123456789012", out["ownerId"])
	assert.Equal(t, "available", out["state"])
}
