package resolver

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRestateSSMResolver_ResolveWithFetcher_TracksSensitivePaths(t *testing.T) {
	resolver := NewRestateSSMResolver(NewSSMResolver(&mockSSMClient{}))
	specs := map[string]json.RawMessage{
		"db": json.RawMessage(`{"spec":{"password":"ssm:///praxis/dev/db-password?sensitive=true","region":"ssm:///praxis/dev/region"}}`),
	}

	resolved, sensitive, err := resolver.resolveWithFetcher(specs, func(paths []string) (map[string]string, error) {
		assert.ElementsMatch(t, []string{"/praxis/dev/db-password", "/praxis/dev/region"}, paths)
		return map[string]string{
			"/praxis/dev/db-password": "secret123",
			"/praxis/dev/region":      "us-east-1",
		}, nil
	})
	require.NoError(t, err)
	require.True(t, sensitive.Contains("db", "spec.password"))
	require.False(t, sensitive.Contains("db", "spec.region"))

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(resolved["db"], &parsed))
	spec := parsed["spec"].(map[string]any)
	assert.Equal(t, "secret123", spec["password"])
	assert.Equal(t, "us-east-1", spec["region"])
}

func TestRestateSSMResolver_ResolveWithFetcher_MissingParameter(t *testing.T) {
	resolver := NewRestateSSMResolver(NewSSMResolver(&mockSSMClient{}))
	specs := map[string]json.RawMessage{
		"db": json.RawMessage(`{"spec":{"password":"ssm:///praxis/dev/missing?sensitive=true"}}`),
	}

	_, _, err := resolver.resolveWithFetcher(specs, func(paths []string) (map[string]string, error) {
		return map[string]string{}, nil
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found in resolved cache")
}

func TestRestateSSMResolver_ResolveWithFetcher_MixedSensitivity(t *testing.T) {
	resolver := NewRestateSSMResolver(NewSSMResolver(&mockSSMClient{}))
	specs := map[string]json.RawMessage{
		"app": json.RawMessage(`{"spec":{"username":"ssm:///praxis/dev/user","password":"ssm:///praxis/dev/pass?sensitive=true","nested":{"token":"ssm:///praxis/dev/token?sensitive=true"}}}`),
	}

	_, sensitive, err := resolver.resolveWithFetcher(specs, func(paths []string) (map[string]string, error) {
		return map[string]string{
			"/praxis/dev/user":  "alice",
			"/praxis/dev/pass":  "secret",
			"/praxis/dev/token": "token-value",
		}, nil
	})
	require.NoError(t, err)
	assert.True(t, sensitive.Contains("app", "spec.password"))
	assert.True(t, sensitive.Contains("app", "spec.nested.token"))
	assert.False(t, sensitive.Contains("app", "spec.username"))
}
