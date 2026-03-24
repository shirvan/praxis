package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers/route53healthcheck"
)

func TestRoute53HealthCheckAdapter_DecodeSpecAndBuildKey(t *testing.T) {
	adapter := NewRoute53HealthCheckAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"Route53HealthCheck",
		"metadata":{"name":"api-check"},
		"spec":{
			"type":"HTTPS",
			"fqdn":"api.example.com",
			"resourcePath":"/healthz",
			"port":443,
			"tags":{"env":"dev"}
		}
	}`)

	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "api-check", key)
	assert.Equal(t, KeyScopeGlobal, adapter.Scope())

	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed, ok := decoded.(route53healthcheck.HealthCheckSpec)
	require.True(t, ok)
	assert.Equal(t, "HTTPS", typed.Type)
	assert.Equal(t, "api.example.com", typed.FQDN)
	assert.Equal(t, "/healthz", typed.ResourcePath)
	assert.Equal(t, int32(443), typed.Port)
	assert.Equal(t, "dev", typed.Tags["env"])
}

func TestRoute53HealthCheckAdapter_NormalizeOutputsAndImportKey(t *testing.T) {
	adapter := NewRoute53HealthCheckAdapterWithAuth(nil)
	out, err := adapter.NormalizeOutputs(route53healthcheck.HealthCheckOutputs{HealthCheckId: "abcdef12-3456-7890-abcd-ef1234567890"})
	require.NoError(t, err)
	assert.Equal(t, "abcdef12-3456-7890-abcd-ef1234567890", out["healthCheckId"])

	key, err := adapter.BuildImportKey("us-east-1", "abcdef12-3456-7890-abcd-ef1234567890")
	require.NoError(t, err)
	assert.Equal(t, "abcdef12-3456-7890-abcd-ef1234567890", key)
}
