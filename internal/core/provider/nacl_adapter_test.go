package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/praxiscloud/praxis/internal/drivers/nacl"
)

func TestNetworkACLAdapter_BuildKey(t *testing.T) {
	adapter := NewNetworkACLAdapter()
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"NetworkACL",
		"metadata":{"name":"public-nacl"},
		"spec":{
			"region":"us-east-1",
			"vpcId":"vpc-123",
			"ingressRules":[],
			"egressRules":[],
			"tags":{"env":"dev"}
		}
	}`)

	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "vpc-123~public-nacl", key)
	assert.Equal(t, KeyScopeCustom, adapter.Scope())
}

func TestNetworkACLAdapter_DecodeSpecAndNormalizeOutputs(t *testing.T) {
	adapter := NewNetworkACLAdapter()
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"NetworkACL",
		"metadata":{"name":"public-nacl"},
		"spec":{
			"region":"us-east-1",
			"vpcId":"vpc-123",
			"ingressRules":[],
			"egressRules":[]
		}
	}`)

	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed, ok := decoded.(nacl.NetworkACLSpec)
	require.True(t, ok)
	assert.Equal(t, "public-nacl", typed.Tags["Name"])

	out, err := adapter.NormalizeOutputs(nacl.NetworkACLOutputs{NetworkAclId: "acl-123", VpcId: "vpc-123", IsDefault: false})
	require.NoError(t, err)
	assert.Equal(t, "acl-123", out["networkAclId"])
	assert.Equal(t, "vpc-123", out["vpcId"])
	assert.Equal(t, false, out["isDefault"])
}

func TestNetworkACLAdapter_BuildImportKey(t *testing.T) {
	adapter := NewNetworkACLAdapter()
	key, err := adapter.BuildImportKey("us-east-1", "acl-123")
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~acl-123", key)
}
