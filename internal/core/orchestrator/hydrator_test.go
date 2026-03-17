package orchestrator

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/praxiscloud/praxis/internal/core/template"
)

func TestHydrateCEL_StringOutput(t *testing.T) {
	spec := json.RawMessage(`{"spec":{"sourceGroup":"${cel:resources.sg.outputs.groupId}"}}`)

	resolved, err := HydrateCEL(spec, map[string]string{
		"spec.sourceGroup": "resources.sg.outputs.groupId",
	}, map[string]map[string]any{
		"sg": {"groupId": "sg-0abc123"},
	}, nil)
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(resolved, &parsed))
	assert.Equal(t, "sg-0abc123", parsed["spec"].(map[string]any)["sourceGroup"])
}

func TestHydrateCEL_IntegerOutput(t *testing.T) {
	spec := json.RawMessage(`{"spec":{"port":"${cel:resources.db.outputs.port}"}}`)

	resolved, err := HydrateCEL(spec, map[string]string{
		"spec.port": "resources.db.outputs.port",
	}, map[string]map[string]any{
		"db": {"port": 5432},
	}, nil)
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(resolved, &parsed))
	assert.Equal(t, float64(5432), parsed["spec"].(map[string]any)["port"])
}

func TestHydrateCEL_BooleanOutput(t *testing.T) {
	spec := json.RawMessage(`{"spec":{"versioned":"${cel:resources.bucket.outputs.versioned}"}}`)

	resolved, err := HydrateCEL(spec, map[string]string{
		"spec.versioned": "resources.bucket.outputs.versioned",
	}, map[string]map[string]any{
		"bucket": {"versioned": true},
	}, nil)
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(resolved, &parsed))
	assert.Equal(t, true, parsed["spec"].(map[string]any)["versioned"])
}

func TestHydrateCEL_ArrayOutput(t *testing.T) {
	spec := json.RawMessage(`{"spec":{"ruleIds":"${cel:resources.sg.outputs.ruleIds}"}}`)

	resolved, err := HydrateCEL(spec, map[string]string{
		"spec.ruleIds": "resources.sg.outputs.ruleIds",
	}, map[string]map[string]any{
		"sg": {"ruleIds": []string{"r-1", "r-2"}},
	}, nil)
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(resolved, &parsed))
	assert.Equal(t, []any{"r-1", "r-2"}, parsed["spec"].(map[string]any)["ruleIds"])
}

func TestHydrateCEL_MissingOutput_ReturnsError(t *testing.T) {
	spec := json.RawMessage(`{"spec":{"port":"${cel:resources.db.outputs.port}"}}`)

	resolved, err := HydrateCEL(spec, map[string]string{
		"spec.port": "resources.db.outputs.port",
	}, map[string]map[string]any{}, nil)
	require.Error(t, err)
	require.NotNil(t, resolved)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(resolved, &parsed))
	assert.Equal(t, "${cel:resources.db.outputs.port}", parsed["spec"].(map[string]any)["port"])

	var tErrs template.TemplateErrors
	require.ErrorAs(t, err, &tErrs)
	assert.Equal(t, template.ErrCELUnresolved, tErrs[0].Kind)
}

func TestHydrateCEL_PartialHydration(t *testing.T) {
	spec := json.RawMessage(`{"spec":{"groupId":"${cel:resources.sg.outputs.groupId}","port":"${cel:resources.db.outputs.port}"}}`)

	resolved, err := HydrateCEL(spec, map[string]string{
		"spec.groupId": "resources.sg.outputs.groupId",
		"spec.port":    "resources.db.outputs.port",
	}, map[string]map[string]any{
		"sg": {"groupId": "sg-123"},
	}, nil)
	require.Error(t, err)
	require.NotNil(t, resolved)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(resolved, &parsed))
	specMap := parsed["spec"].(map[string]any)
	assert.Equal(t, "sg-123", specMap["groupId"])
	assert.Equal(t, "${cel:resources.db.outputs.port}", specMap["port"])
}
