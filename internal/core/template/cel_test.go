package template

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCELResolver_Resolve_Simple(t *testing.T) {
	vars := map[string]any{"env": "production", "region": "us-east-1"}
	r := NewCELResolver(vars)

	specs := map[string]json.RawMessage{
		"bucket": json.RawMessage(`{"region":"${cel:variables.region}","name":"app-${cel:variables.env}"}`),
	}

	result, err := r.Resolve(specs)
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(result["bucket"], &parsed))
	assert.Equal(t, "us-east-1", parsed["region"])
	assert.Equal(t, "app-production", parsed["name"])
}

func TestCELResolver_Resolve_NoPlaceholders(t *testing.T) {
	r := NewCELResolver(map[string]any{"env": "dev"})

	specs := map[string]json.RawMessage{
		"bucket": json.RawMessage(`{"region":"us-east-1"}`),
	}

	result, err := r.Resolve(specs)
	require.NoError(t, err)
	assert.JSONEq(t, `{"region":"us-east-1"}`, string(result["bucket"]))
}

func TestCELResolver_Resolve_MissingVariable(t *testing.T) {
	r := NewCELResolver(map[string]any{})

	specs := map[string]json.RawMessage{
		"bucket": json.RawMessage(`{"name":"${cel:variables.env}"}`),
	}

	_, err := r.Resolve(specs)
	require.Error(t, err)

	var tErrs TemplateErrors
	require.ErrorAs(t, err, &tErrs)
	assert.Equal(t, ErrCELEval, tErrs[0].Kind)
}

func TestCELResolver_Resolve_SyntaxError(t *testing.T) {
	r := NewCELResolver(map[string]any{"env": "dev"})

	specs := map[string]json.RawMessage{
		"bucket": json.RawMessage(`{"name":"${cel:variables.env +++ bad}"}`),
	}

	_, err := r.Resolve(specs)
	require.Error(t, err)

	var tErrs TemplateErrors
	require.ErrorAs(t, err, &tErrs)
	assert.True(t, tErrs[0].Kind == ErrCELParse || tErrs[0].Kind == ErrCELEval,
		"expected CEL parse or eval error, got %v", tErrs[0].Kind)
}

func TestCELResolver_Resolve_MultipleResources(t *testing.T) {
	vars := map[string]any{"env": "staging"}
	r := NewCELResolver(vars)

	specs := map[string]json.RawMessage{
		"a": json.RawMessage(`{"name":"a-${cel:variables.env}"}`),
		"b": json.RawMessage(`{"name":"b-${cel:variables.env}"}`),
	}

	result, err := r.Resolve(specs)
	require.NoError(t, err)
	assert.Len(t, result, 2)

	var a, b map[string]any
	require.NoError(t, json.Unmarshal(result["a"], &a))
	require.NoError(t, json.Unmarshal(result["b"], &b))
	assert.Equal(t, "a-staging", a["name"])
	assert.Equal(t, "b-staging", b["name"])
}
