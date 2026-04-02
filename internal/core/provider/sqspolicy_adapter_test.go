package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers/sqspolicy"
)

func TestSQSQueuePolicyAdapter_BuildKeyAndDecodeSpec(t *testing.T) {
	adapter := NewSQSQueuePolicyAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"SQSQueuePolicy",
		"metadata":{"name":"orders"},
		"spec":{"region":"us-east-1","policy":{"Version":"2012-10-17","Statement":[]}}
	}`)

	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~orders", key)
	assert.Equal(t, KeyScopeRegion, adapter.Scope())

	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed, ok := decoded.(sqspolicy.SQSQueuePolicySpec)
	require.True(t, ok)
	assert.Equal(t, "us-east-1", typed.Region)
	assert.Equal(t, "orders", typed.QueueName)
	assert.JSONEq(t, `{"Version":"2012-10-17","Statement":[]}`, typed.Policy)
}

func TestSQSQueuePolicyAdapter_BuildImportKey(t *testing.T) {
	adapter := NewSQSQueuePolicyAdapterWithAuth(nil)
	key, err := adapter.BuildImportKey("us-east-1", "https://sqs.us-east-1.amazonaws.com/123/orders")
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~orders", key)
}

func TestSQSQueuePolicyAdapter_NormalizeOutputs(t *testing.T) {
	adapter := NewSQSQueuePolicyAdapterWithAuth(nil)
	out, err := adapter.NormalizeOutputs(sqspolicy.SQSQueuePolicyOutputs{
		QueueUrl:  "https://sqs.us-east-1.amazonaws.com/123/orders",
		QueueArn:  "arn:aws:sqs:us-east-1:123:orders",
		QueueName: "orders",
	})
	require.NoError(t, err)
	assert.Equal(t, "https://sqs.us-east-1.amazonaws.com/123/orders", out["queueUrl"])
	assert.Equal(t, "arn:aws:sqs:us-east-1:123:orders", out["queueArn"])
	assert.Equal(t, "orders", out["queueName"])
}
