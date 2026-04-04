package orchestrator

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/core/template"
)

func TestHydrateExprs_StringOutput(t *testing.T) {
	spec := json.RawMessage(`{"spec":{"sourceGroup":"${resources.sg.outputs.groupId}"}}`)

	resolved, err := HydrateExprs(spec, map[string]string{
		"spec.sourceGroup": "resources.sg.outputs.groupId",
	}, map[string]map[string]any{
		"sg": {"groupId": "sg-0abc123"},
	})
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(resolved, &parsed))
	assert.Equal(t, "sg-0abc123", parsed["spec"].(map[string]any)["sourceGroup"])
}

func TestHydrateExprs_IntegerOutput(t *testing.T) {
	spec := json.RawMessage(`{"spec":{"port":"${resources.db.outputs.port}"}}`)

	resolved, err := HydrateExprs(spec, map[string]string{
		"spec.port": "resources.db.outputs.port",
	}, map[string]map[string]any{
		"db": {"port": 5432},
	})
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(resolved, &parsed))
	assert.Equal(t, float64(5432), parsed["spec"].(map[string]any)["port"])
}

func TestHydrateExprs_BooleanOutput(t *testing.T) {
	spec := json.RawMessage(`{"spec":{"versioned":"${resources.bucket.outputs.versioned}"}}`)

	resolved, err := HydrateExprs(spec, map[string]string{
		"spec.versioned": "resources.bucket.outputs.versioned",
	}, map[string]map[string]any{
		"bucket": {"versioned": true},
	})
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(resolved, &parsed))
	assert.Equal(t, true, parsed["spec"].(map[string]any)["versioned"])
}

func TestHydrateExprs_ArrayOutput(t *testing.T) {
	spec := json.RawMessage(`{"spec":{"ruleIds":"${resources.sg.outputs.ruleIds}"}}`)

	resolved, err := HydrateExprs(spec, map[string]string{
		"spec.ruleIds": "resources.sg.outputs.ruleIds",
	}, map[string]map[string]any{
		"sg": {"ruleIds": []string{"r-1", "r-2"}},
	})
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(resolved, &parsed))
	assert.Equal(t, []any{"r-1", "r-2"}, parsed["spec"].(map[string]any)["ruleIds"])
}

func TestHydrateExprs_MissingOutput_ReturnsError(t *testing.T) {
	spec := json.RawMessage(`{"spec":{"port":"${resources.db.outputs.port}"}}`)

	resolved, err := HydrateExprs(spec, map[string]string{
		"spec.port": "resources.db.outputs.port",
	}, map[string]map[string]any{})
	require.Error(t, err)
	require.NotNil(t, resolved)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(resolved, &parsed))
	assert.Equal(t, "${resources.db.outputs.port}", parsed["spec"].(map[string]any)["port"])

	var tErrs template.TemplateErrors
	require.ErrorAs(t, err, &tErrs)
	assert.Equal(t, template.ErrExprUnresolved, tErrs[0].Kind)
}

func TestHydrateExprs_PartialHydration(t *testing.T) {
	spec := json.RawMessage(`{"spec":{"groupId":"${resources.sg.outputs.groupId}","port":"${resources.db.outputs.port}"}}`)

	resolved, err := HydrateExprs(spec, map[string]string{
		"spec.groupId": "resources.sg.outputs.groupId",
		"spec.port":    "resources.db.outputs.port",
	}, map[string]map[string]any{
		"sg": {"groupId": "sg-123"},
	})
	require.Error(t, err)
	require.NotNil(t, resolved)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(resolved, &parsed))
	specMap := parsed["spec"].(map[string]any)
	assert.Equal(t, "sg-123", specMap["groupId"])
	assert.Equal(t, "${resources.db.outputs.port}", specMap["port"])
}

func TestHydrateExprs_ArrayIndexedOutput(t *testing.T) {
	spec := json.RawMessage(`{"spec":{"recordName":"${resources.cert.outputs.dnsValidationRecords[0].resourceRecordName}"}}`)

	resolved, err := HydrateExprs(spec, map[string]string{
		"spec.recordName": "resources.cert.outputs.dnsValidationRecords[0].resourceRecordName",
	}, map[string]map[string]any{
		"cert": {
			"dnsValidationRecords": []any{
				map[string]any{
					"resourceRecordName":  "_abc.example.com.",
					"resourceRecordValue": "_xyz.acm-validations.aws.",
				},
				map[string]any{
					"resourceRecordName":  "_def.example.com.",
					"resourceRecordValue": "_uvw.acm-validations.aws.",
				},
			},
		},
	})
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(resolved, &parsed))
	assert.Equal(t, "_abc.example.com.", parsed["spec"].(map[string]any)["recordName"])
}

func TestHydrateExprs_ArrayIndexedOutput_SecondElement(t *testing.T) {
	spec := json.RawMessage(`{"spec":{"val":"${resources.cert.outputs.records[1].name}"}}`)

	resolved, err := HydrateExprs(spec, map[string]string{
		"spec.val": "resources.cert.outputs.records[1].name",
	}, map[string]map[string]any{
		"cert": {
			"records": []any{
				map[string]any{"name": "first"},
				map[string]any{"name": "second"},
			},
		},
	})
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(resolved, &parsed))
	assert.Equal(t, "second", parsed["spec"].(map[string]any)["val"])
}

func TestHydrateExprs_ArrayIndexedOutput_WholeElement(t *testing.T) {
	spec := json.RawMessage(`{"spec":{"record":"${resources.cert.outputs.records[0]}"}}`)

	resolved, err := HydrateExprs(spec, map[string]string{
		"spec.record": "resources.cert.outputs.records[0]",
	}, map[string]map[string]any{
		"cert": {
			"records": []any{
				map[string]any{"name": "first", "value": "v1"},
			},
		},
	})
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(resolved, &parsed))
	record := parsed["spec"].(map[string]any)["record"].(map[string]any)
	assert.Equal(t, "first", record["name"])
	assert.Equal(t, "v1", record["value"])
}

func TestHydrateExprs_ArrayIndexedOutput_OutOfRange(t *testing.T) {
	spec := json.RawMessage(`{"spec":{"val":"${resources.cert.outputs.records[5].name}"}}`)

	_, err := HydrateExprs(spec, map[string]string{
		"spec.val": "resources.cert.outputs.records[5].name",
	}, map[string]map[string]any{
		"cert": {
			"records": []any{
				map[string]any{"name": "only"},
			},
		},
	})
	require.Error(t, err)

	var tErrs template.TemplateErrors
	require.ErrorAs(t, err, &tErrs)
	assert.Equal(t, template.ErrExprUnresolved, tErrs[0].Kind)
}

func TestHydrateExprs_ArrayIndexedOutput_TypedSlice(t *testing.T) {
	// Drivers may return []map[string]any instead of []any.
	spec := json.RawMessage(`{"spec":{"val":"${resources.cert.outputs.records[0].name}"}}`)

	resolved, err := HydrateExprs(spec, map[string]string{
		"spec.val": "resources.cert.outputs.records[0].name",
	}, map[string]map[string]any{
		"cert": {
			"records": []map[string]any{
				{"name": "typed-slice-value"},
			},
		},
	})
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(resolved, &parsed))
	assert.Equal(t, "typed-slice-value", parsed["spec"].(map[string]any)["val"])
}
