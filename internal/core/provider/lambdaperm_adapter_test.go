package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers/lambdaperm"
)

func TestLambdaPermissionAdapter_BuildKeyAndDecodeSpec(t *testing.T) {
	adapter := NewLambdaPermissionAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"LambdaPermission",
		"metadata":{"name":"allow-s3"},
		"spec":{
			"region":"us-east-1",
			"functionName":"processor",
			"principal":"s3.amazonaws.com"
		}
	}`)
	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~processor~allow-s3", key)
	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed := decoded.(lambdaperm.LambdaPermissionSpec)
	assert.Equal(t, "allow-s3", typed.StatementId)
	assert.Equal(t, "processor", typed.FunctionName)
	assert.Equal(t, "s3.amazonaws.com", typed.Principal)
}

func TestLambdaPermissionAdapter_BuildImportKey(t *testing.T) {
	adapter := NewLambdaPermissionAdapterWithAuth(nil)
	key, err := adapter.BuildImportKey("us-east-1", "processor~allow-s3")
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~processor~allow-s3", key)
}

func TestLambdaPermissionAdapter_NormalizeOutputs(t *testing.T) {
	adapter := NewLambdaPermissionAdapterWithAuth(nil)
	out, err := adapter.NormalizeOutputs(lambdaperm.LambdaPermissionOutputs{StatementId: "allow-s3", FunctionName: "processor", Statement: `{"Sid":"allow-s3"}`})
	require.NoError(t, err)
	assert.Equal(t, "allow-s3", out["statementId"])
	assert.Equal(t, "processor", out["functionName"])
}
