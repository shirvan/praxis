package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers/esm"
)

func TestESMAdapter_BuildKeyAndDecodeSpec(t *testing.T) {
	adapter := NewESMAdapter()
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"EventSourceMapping",
		"metadata":{"name":"orders-consumer"},
		"spec":{
			"region":"us-east-1",
			"functionName":"processor",
			"eventSourceArn":"arn:aws:sqs:us-east-1:123456789012:orders",
			"enabled":true
		}
	}`)
	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Contains(t, key, "us-east-1~processor~")
	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed := decoded.(esm.EventSourceMappingSpec)
	assert.Equal(t, "processor", typed.FunctionName)
	assert.Equal(t, "arn:aws:sqs:us-east-1:123456789012:orders", typed.EventSourceArn)
}

func TestESMAdapter_BuildImportKey(t *testing.T) {
	adapter := NewESMAdapter()
	key, err := adapter.BuildImportKey("us-east-1", "uuid-123")
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~uuid-123", key)
}

func TestESMAdapter_NormalizeOutputs(t *testing.T) {
	adapter := NewESMAdapter()
	out, err := adapter.NormalizeOutputs(esm.EventSourceMappingOutputs{UUID: "uuid-123", EventSourceArn: "arn:aws:sqs:us-east-1:123:q", FunctionArn: "arn:aws:lambda:us-east-1:123:function:processor", State: "Enabled", BatchSize: 10})
	require.NoError(t, err)
	assert.Equal(t, "uuid-123", out["uuid"])
	assert.Equal(t, int32(10), out["batchSize"])
	assert.Equal(t, "Enabled", out["state"])
}
