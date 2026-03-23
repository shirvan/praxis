package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers/lambda"
)

func TestLambdaAdapter_BuildKeyAndDecodeSpec(t *testing.T) {
	adapter := NewLambdaAdapter()
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"LambdaFunction",
		"metadata":{"name":"processor"},
		"spec":{
			"region":"us-east-1",
			"role":"arn:aws:iam::123456789012:role/lambda-exec",
			"runtime":"python3.12",
			"handler":"main.handler",
			"code":{"zipFile":"Zm9v"}
		}
	}`)
	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~processor", key)
	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed := decoded.(lambda.LambdaFunctionSpec)
	assert.Equal(t, "processor", typed.FunctionName)
	assert.Equal(t, "us-east-1", typed.Region)
	assert.Equal(t, "python3.12", typed.Runtime)
}

func TestLambdaAdapter_BuildImportKey(t *testing.T) {
	adapter := NewLambdaAdapter()
	key, err := adapter.BuildImportKey("us-east-1", "processor")
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~processor", key)
}

func TestLambdaAdapter_NormalizeOutputs(t *testing.T) {
	adapter := NewLambdaAdapter()
	out, err := adapter.NormalizeOutputs(lambda.LambdaFunctionOutputs{FunctionArn: "arn:aws:lambda:us-east-1:123:function:processor", FunctionName: "processor", Version: "$LATEST", State: "Active"})
	require.NoError(t, err)
	assert.Equal(t, "processor", out["functionName"])
	assert.Equal(t, "Active", out["state"])
}