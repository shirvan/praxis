package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers/dbparametergroup"
)

func TestDBParameterGroupAdapter_DecodeSpecAndBuildKey(t *testing.T) {
	adapter := NewDBParameterGroupAdapter()
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"DBParameterGroup",
		"metadata":{"name":"my-param-group"},
		"spec":{
			"region":"us-east-1",
			"type":"db",
			"family":"mysql8.0",
			"description":"My parameter group",
			"parameters":{"max_connections":"200"},
			"tags":{"env":"dev"}
		}
	}`)

	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~my-param-group", key)

	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed, ok := decoded.(dbparametergroup.DBParameterGroupSpec)
	require.True(t, ok)
	assert.Equal(t, "us-east-1", typed.Region)
	assert.Equal(t, "my-param-group", typed.GroupName)
	assert.Equal(t, "db", typed.Type)
	assert.Equal(t, "mysql8.0", typed.Family)
	assert.Equal(t, "My parameter group", typed.Description)
	assert.Equal(t, map[string]string{"max_connections": "200"}, typed.Parameters)
	assert.Equal(t, map[string]string{"env": "dev"}, typed.Tags)
}

func TestDBParameterGroupAdapter_BuildImportKey(t *testing.T) {
	adapter := NewDBParameterGroupAdapter()
	key, err := adapter.BuildImportKey("us-west-2", "my-group")
	require.NoError(t, err)
	assert.Equal(t, "us-west-2~my-group", key)
}

func TestDBParameterGroupAdapter_Kind(t *testing.T) {
	adapter := NewDBParameterGroupAdapter()
	assert.Equal(t, dbparametergroup.ServiceName, adapter.Kind())
	assert.Equal(t, dbparametergroup.ServiceName, adapter.ServiceName())
}

func TestDBParameterGroupAdapter_Scope(t *testing.T) {
	adapter := NewDBParameterGroupAdapter()
	assert.Equal(t, KeyScopeRegion, adapter.Scope())
}

func TestDBParameterGroupAdapter_NormalizeOutputs(t *testing.T) {
	adapter := NewDBParameterGroupAdapter()
	out, err := adapter.NormalizeOutputs(dbparametergroup.DBParameterGroupOutputs{
		GroupName: "my-group",
		ARN:       "arn:aws:rds:us-east-1:123:pg:my-group",
		Family:    "mysql8.0",
		Type:      "db",
	})
	require.NoError(t, err)
	assert.Equal(t, "my-group", out["groupName"])
	assert.Equal(t, "arn:aws:rds:us-east-1:123:pg:my-group", out["arn"])
	assert.Equal(t, "mysql8.0", out["family"])
	assert.Equal(t, "db", out["type"])
}
