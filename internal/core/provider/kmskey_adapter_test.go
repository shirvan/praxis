package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers/kmskey"
)

func TestKMSKeyAdapter_BuildKeyAndDecodeSpec(t *testing.T) {
	adapter := NewKMSKeyAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/alpha",
		"kind":"KMSKey",
		"metadata":{"name":"app-data"},
		"spec":{"region":"us-east-1","description":"app data key","enableKeyRotation":true,"tags":{"env":"dev"}}
	}`)

	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~app-data", key)
	assert.Equal(t, KeyScopeRegion, adapter.Scope())

	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed, ok := decoded.(kmskey.KMSKeySpec)
	require.True(t, ok)
	assert.Equal(t, "us-east-1", typed.Region)
	assert.Equal(t, "app-data", typed.Name)
	assert.Equal(t, "app data key", typed.Description)
	assert.True(t, typed.EnableKeyRotation)
	assert.Equal(t, map[string]string{"env": "dev"}, typed.Tags)
}

func TestKMSKeyAdapter_BuildKey_StripsAliasPrefix(t *testing.T) {
	adapter := NewKMSKeyAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/alpha",
		"kind":"KMSKey",
		"metadata":{"name":"alias/app-data"},
		"spec":{"region":"us-east-1"}
	}`)

	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~app-data", key)

	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed, ok := decoded.(kmskey.KMSKeySpec)
	require.True(t, ok)
	assert.Equal(t, "app-data", typed.Name)
}

func TestKMSKeyAdapter_BuildImportKey(t *testing.T) {
	adapter := NewKMSKeyAdapterWithAuth(nil)

	key, err := adapter.BuildImportKey("us-east-1", "app-data")
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~app-data", key)

	key, err = adapter.BuildImportKey("us-east-1", "alias/app-data")
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~app-data", key)
}

func TestKMSKeyAdapter_NormalizeOutputs(t *testing.T) {
	adapter := NewKMSKeyAdapterWithAuth(nil)
	out, err := adapter.NormalizeOutputs(kmskey.KMSKeyOutputs{
		ARN:       "arn:aws:kms:us-east-1:123:key/abc",
		KeyID:     "abc",
		AliasName: "alias/app-data",
	})
	require.NoError(t, err)
	assert.Equal(t, "arn:aws:kms:us-east-1:123:key/abc", out["arn"])
	assert.Equal(t, "abc", out["keyId"])
	assert.Equal(t, "alias/app-data", out["aliasName"])
}

func TestKMSKeyAdapter_NormalizeOutputs_OmitsEmptyFields(t *testing.T) {
	adapter := NewKMSKeyAdapterWithAuth(nil)
	out, err := adapter.NormalizeOutputs(kmskey.KMSKeyOutputs{KeyID: "abc"})
	require.NoError(t, err)
	assert.Equal(t, "abc", out["keyId"])
	assert.NotContains(t, out, "arn")
	assert.NotContains(t, out, "aliasName")
}
