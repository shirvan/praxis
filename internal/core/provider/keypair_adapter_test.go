package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/praxiscloud/praxis/internal/drivers/keypair"
)

func TestKeyPairAdapter_DecodeSpecAndBuildKey(t *testing.T) {
	adapter := NewKeyPairAdapter()
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"KeyPair",
		"metadata":{"name":"web-key"},
		"spec":{
			"region":"us-east-1",
			"tags":{"env":"dev"}
		}
	}`)

	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~web-key", key)
	assert.Equal(t, KeyScopeRegion, adapter.Scope())

	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed, ok := decoded.(keypair.KeyPairSpec)
	require.True(t, ok)
	assert.Equal(t, "us-east-1", typed.Region)
	assert.Equal(t, "web-key", typed.KeyName)
	assert.Equal(t, "ed25519", typed.KeyType)
	assert.Equal(t, "dev", typed.Tags["env"])
}

func TestKeyPairAdapter_BuildImportKey(t *testing.T) {
	adapter := NewKeyPairAdapter()
	key, err := adapter.BuildImportKey("us-east-1", "web-key")
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~web-key", key)
}

func TestKeyPairAdapter_Kind(t *testing.T) {
	adapter := NewKeyPairAdapter()
	assert.Equal(t, keypair.ServiceName, adapter.Kind())
	assert.Equal(t, keypair.ServiceName, adapter.ServiceName())
}

func TestKeyPairAdapter_Scope(t *testing.T) {
	adapter := NewKeyPairAdapter()
	assert.Equal(t, KeyScopeRegion, adapter.Scope())
}

func TestKeyPairAdapter_NormalizeOutputs(t *testing.T) {
	adapter := NewKeyPairAdapter()
	out, err := adapter.NormalizeOutputs(keypair.KeyPairOutputs{
		KeyName:            "web-key",
		KeyPairId:          "key-123",
		KeyFingerprint:     "aa:bb:cc",
		KeyType:            "ed25519",
		PrivateKeyMaterial: "pem-data",
	})
	require.NoError(t, err)
	assert.Equal(t, "web-key", out["keyName"])
	assert.Equal(t, "key-123", out["keyPairId"])
	assert.Equal(t, "aa:bb:cc", out["keyFingerprint"])
	assert.Equal(t, "ed25519", out["keyType"])
	assert.Equal(t, "pem-data", out["privateKeyMaterial"])
}