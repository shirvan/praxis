package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLoad_Defaults(t *testing.T) {
	for _, k := range []string{
		"PRAXIS_LISTEN_ADDR",
		"PRAXIS_RESTATE_ENDPOINT",
		"PRAXIS_SCHEMA_DIR",
		"PRAXIS_POLICY_DIR",
	} {
		t.Setenv(k, "")
	}

	cfg := Load()

	assert.Equal(t, "0.0.0.0:9080", cfg.ListenAddr)
	assert.Equal(t, "http://localhost:8080", cfg.RestateEndpoint)
	assert.Equal(t, "./schemas", cfg.SchemaDir)
	assert.Empty(t, cfg.PolicyDir)
}

func TestLoad_EnvOverrides(t *testing.T) {
	t.Setenv("PRAXIS_LISTEN_ADDR", "127.0.0.1:9999")
	t.Setenv("PRAXIS_RESTATE_ENDPOINT", "http://restate:8080")
	t.Setenv("PRAXIS_SCHEMA_DIR", "/opt/schemas")
	t.Setenv("PRAXIS_POLICY_DIR", "/opt/policies")

	cfg := Load()

	assert.Equal(t, "127.0.0.1:9999", cfg.ListenAddr)
	assert.Equal(t, "http://restate:8080", cfg.RestateEndpoint)
	assert.Equal(t, "/opt/schemas", cfg.SchemaDir)
	assert.Equal(t, "/opt/policies", cfg.PolicyDir)
}

func TestLoad_PartialOverride(t *testing.T) {
	t.Setenv("PRAXIS_LISTEN_ADDR", "")

	cfg := Load()

	assert.Equal(t, "0.0.0.0:9080", cfg.ListenAddr, "empty string should fall back to default")
	assert.Equal(t, "http://localhost:8080", cfg.RestateEndpoint)
}
