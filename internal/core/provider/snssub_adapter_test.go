package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers/snssub"
)

func TestSNSSubscriptionAdapter_BuildKeyAndDecodeSpec(t *testing.T) {
	adapter := NewSNSSubscriptionAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"SNSSubscription",
		"metadata":{"name":"my-sub"},
		"spec":{
			"region":"us-east-1",
			"topicArn":"arn:aws:sns:us-east-1:123456789012:my-topic",
			"protocol":"sqs",
			"endpoint":"arn:aws:sqs:us-east-1:123456789012:my-queue"
		}
	}`)

	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~arn:aws:sns:us-east-1:123456789012:my-topic~sqs~arn:aws:sqs:us-east-1:123456789012:my-queue", key)
	assert.Equal(t, KeyScopeCustom, adapter.Scope())

	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed, ok := decoded.(snssub.SNSSubscriptionSpec)
	require.True(t, ok)
	assert.Equal(t, "us-east-1", typed.Region)
	assert.Equal(t, "arn:aws:sns:us-east-1:123456789012:my-topic", typed.TopicArn)
	assert.Equal(t, "sqs", typed.Protocol)
	assert.Equal(t, "arn:aws:sqs:us-east-1:123456789012:my-queue", typed.Endpoint)
}

func TestSNSSubscriptionAdapter_BuildImportKey(t *testing.T) {
	adapter := NewSNSSubscriptionAdapterWithAuth(nil)
	key, err := adapter.BuildImportKey("us-east-1", "arn:aws:sns:us-east-1:123:topic:sub-id")
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~arn:aws:sns:us-east-1:123:topic:sub-id", key)
}

func TestSNSSubscriptionAdapter_NormalizeOutputs(t *testing.T) {
	adapter := NewSNSSubscriptionAdapterWithAuth(nil)
	out, err := adapter.NormalizeOutputs(snssub.SNSSubscriptionOutputs{
		SubscriptionArn: "arn:aws:sns:us-east-1:123:topic:sub-id",
		TopicArn:        "arn:aws:sns:us-east-1:123:topic",
		Protocol:        "sqs",
		Endpoint:        "arn:aws:sqs:us-east-1:123:queue",
		Owner:           "123",
	})
	require.NoError(t, err)
	assert.Equal(t, "arn:aws:sns:us-east-1:123:topic:sub-id", out["subscriptionArn"])
	assert.Equal(t, "arn:aws:sns:us-east-1:123:topic", out["topicArn"])
	assert.Equal(t, "sqs", out["protocol"])
	assert.Equal(t, "arn:aws:sqs:us-east-1:123:queue", out["endpoint"])
	assert.Equal(t, "123", out["owner"])
}

func TestSNSSubscriptionAdapter_NormalizeOutputs_NoOwner(t *testing.T) {
	adapter := NewSNSSubscriptionAdapterWithAuth(nil)
	out, err := adapter.NormalizeOutputs(snssub.SNSSubscriptionOutputs{
		SubscriptionArn: "arn",
		TopicArn:        "topic-arn",
		Protocol:        "sqs",
		Endpoint:        "endpoint",
	})
	require.NoError(t, err)
	_, hasOwner := out["owner"]
	assert.False(t, hasOwner, "owner should be omitted when empty")
}

func TestSNSSubscriptionAdapter_Kind(t *testing.T) {
	adapter := NewSNSSubscriptionAdapterWithAuth(nil)
	assert.Equal(t, snssub.ServiceName, adapter.Kind())
	assert.Equal(t, snssub.ServiceName, adapter.ServiceName())
}

func TestSNSSubscriptionAdapter_Scope(t *testing.T) {
	adapter := NewSNSSubscriptionAdapterWithAuth(nil)
	assert.Equal(t, KeyScopeCustom, adapter.Scope())
}

func TestSNSSubscriptionAdapter_DecodeSpec_MissingRegion(t *testing.T) {
	adapter := NewSNSSubscriptionAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1","kind":"SNSSubscription",
		"metadata":{"name":"sub"},
		"spec":{"topicArn":"arn:aws:sns:us-east-1:123:topic","protocol":"sqs","endpoint":"arn:aws:sqs:us-east-1:123:q"}
	}`)
	_, err := adapter.DecodeSpec(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "region")
}

func TestSNSSubscriptionAdapter_DecodeSpec_MissingTopicArn(t *testing.T) {
	adapter := NewSNSSubscriptionAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1","kind":"SNSSubscription",
		"metadata":{"name":"sub"},
		"spec":{"region":"us-east-1","protocol":"sqs","endpoint":"arn:aws:sqs:us-east-1:123:q"}
	}`)
	_, err := adapter.DecodeSpec(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "topicArn")
}

func TestSNSSubscriptionAdapter_DecodeSpec_MissingProtocol(t *testing.T) {
	adapter := NewSNSSubscriptionAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1","kind":"SNSSubscription",
		"metadata":{"name":"sub"},
		"spec":{"region":"us-east-1","topicArn":"arn:aws:sns:us-east-1:123:topic","endpoint":"arn:aws:sqs:us-east-1:123:q"}
	}`)
	_, err := adapter.DecodeSpec(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "protocol")
}

func TestSNSSubscriptionAdapter_DecodeSpec_MissingEndpoint(t *testing.T) {
	adapter := NewSNSSubscriptionAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1","kind":"SNSSubscription",
		"metadata":{"name":"sub"},
		"spec":{"region":"us-east-1","topicArn":"arn:aws:sns:us-east-1:123:topic","protocol":"sqs"}
	}`)
	_, err := adapter.DecodeSpec(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "endpoint")
}
