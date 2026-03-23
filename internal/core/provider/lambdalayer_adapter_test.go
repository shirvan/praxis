package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/praxiscloud/praxis/internal/drivers/lambdalayer"
)

func TestLambdaLayerAdapter_BuildKeyAndDecodeSpec(t *testing.T) {
	adapter := NewLambdaLayerAdapter()
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"LambdaLayer",
		"metadata":{"name":"deps"},
		"spec":{
			"region":"us-east-1",
			"compatibleRuntimes":["python3.12"],
			"code":{"zipFile":"Zm9v"}
		}
	}`)
	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~deps", key)
	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed := decoded.(lambdalayer.LambdaLayerSpec)
	assert.Equal(t, "deps", typed.LayerName)
	assert.Equal(t, "us-east-1", typed.Region)
	assert.Equal(t, []string{"python3.12"}, typed.CompatibleRuntimes)
}

func TestLambdaLayerAdapter_BuildImportKey(t *testing.T) {
	adapter := NewLambdaLayerAdapter()
	key, err := adapter.BuildImportKey("us-east-1", "deps")
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~deps", key)
}

func TestLambdaLayerAdapter_NormalizeOutputs(t *testing.T) {
	adapter := NewLambdaLayerAdapter()
	out, err := adapter.NormalizeOutputs(lambdalayer.LambdaLayerOutputs{LayerArn: "arn:aws:lambda:us-east-1:123:layer:deps", LayerVersionArn: "arn:aws:lambda:us-east-1:123:layer:deps:7", LayerName: "deps", Version: 7, CodeSha256: "abc"})
	require.NoError(t, err)
	assert.Equal(t, "deps", out["layerName"])
	assert.Equal(t, int64(7), out["version"])
	assert.Equal(t, "abc", out["codeSha256"])
}
