package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers/sqs"
)

func TestSQSAdapter_BuildKeyAndDecodeSpec(t *testing.T) {
	adapter := NewSQSAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"SQSQueue",
		"metadata":{"name":"orders"},
		"spec":{"region":"us-east-1","visibilityTimeout":30,"tags":{"env":"dev"}}
	}`)

	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~orders", key)
	assert.Equal(t, KeyScopeRegion, adapter.Scope())

	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed, ok := decoded.(sqs.SQSQueueSpec)
	require.True(t, ok)
	assert.Equal(t, "us-east-1", typed.Region)
	assert.Equal(t, "orders", typed.QueueName)
	assert.Equal(t, 30, typed.VisibilityTimeout)
	assert.Equal(t, map[string]string{"env": "dev"}, typed.Tags)
	assert.True(t, typed.SqsManagedSseEnabled)
}

func TestSQSAdapter_BuildImportKey(t *testing.T) {
	adapter := NewSQSAdapterWithAuth(nil)
	key, err := adapter.BuildImportKey("us-east-1", "https://sqs.us-east-1.amazonaws.com/123/orders")
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~orders", key)
}

func TestSQSAdapter_NormalizeOutputs(t *testing.T) {
	adapter := NewSQSAdapterWithAuth(nil)
	out, err := adapter.NormalizeOutputs(sqs.SQSQueueOutputs{
		QueueUrl:  "https://sqs.us-east-1.amazonaws.com/123/orders",
		QueueArn:  "arn:aws:sqs:us-east-1:123:orders",
		QueueName: "orders",
	})
	require.NoError(t, err)
	assert.Equal(t, "https://sqs.us-east-1.amazonaws.com/123/orders", out["queueUrl"])
	assert.Equal(t, "arn:aws:sqs:us-east-1:123:orders", out["queueArn"])
	assert.Equal(t, "orders", out["queueName"])
}
