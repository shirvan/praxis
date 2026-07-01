package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers/secret"
)

func TestSecretsManagerSecretAdapter_BuildKeyAndDecodeSpec(t *testing.T) {
	adapter := NewSecretsManagerSecretAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"SecretsManagerSecret",
		"metadata":{"name":"db-password"},
		"spec":{"region":"us-east-1","description":"database password","secretString":"hunter2","tags":{"env":"dev"}}
	}`)

	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~db-password", key)
	assert.Equal(t, KeyScopeRegion, adapter.Scope())

	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed, ok := decoded.(secret.SecretsManagerSecretSpec)
	require.True(t, ok)
	assert.Equal(t, "us-east-1", typed.Region)
	assert.Equal(t, "db-password", typed.Name)
	assert.Equal(t, "database password", typed.Description)
	assert.Equal(t, "hunter2", typed.SecretString)
	assert.Equal(t, map[string]string{"env": "dev"}, typed.Tags)
}

func TestSecretsManagerSecretAdapter_DecodeSpec_MissingRegion(t *testing.T) {
	adapter := NewSecretsManagerSecretAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"kind":"SecretsManagerSecret",
		"metadata":{"name":"db-password"},
		"spec":{"secretString":"hunter2"}
	}`)

	_, err := adapter.DecodeSpec(raw)
	assert.ErrorContains(t, err, "spec.region is required")
}

func TestSecretsManagerSecretAdapter_BuildImportKey(t *testing.T) {
	adapter := NewSecretsManagerSecretAdapterWithAuth(nil)
	key, err := adapter.BuildImportKey("us-east-1", "db-password")
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~db-password", key)
}

func TestSecretsManagerSecretAdapter_NormalizeOutputs(t *testing.T) {
	adapter := NewSecretsManagerSecretAdapterWithAuth(nil)
	out, err := adapter.NormalizeOutputs(secret.SecretsManagerSecretOutputs{
		ARN:       "arn:aws:secretsmanager:us-east-1:123:secret:db-password-abc",
		Name:      "db-password",
		VersionID: "v1",
	})
	require.NoError(t, err)
	assert.Equal(t, "arn:aws:secretsmanager:us-east-1:123:secret:db-password-abc", out["arn"])
	assert.Equal(t, "db-password", out["name"])
	assert.Equal(t, "v1", out["versionId"])
	// Outputs flow into deployment state and expression hydration; the secret
	// value must never appear among them.
	assert.NotContains(t, out, "secretString")
	assert.NotContains(t, out, "value")
}

func TestSecretsManagerSecretAdapter_NormalizeOutputs_OmitsEmptyOptionals(t *testing.T) {
	adapter := NewSecretsManagerSecretAdapterWithAuth(nil)
	out, err := adapter.NormalizeOutputs(secret.SecretsManagerSecretOutputs{Name: "db-password"})
	require.NoError(t, err)
	assert.NotContains(t, out, "arn")
	assert.NotContains(t, out, "versionId")
}
