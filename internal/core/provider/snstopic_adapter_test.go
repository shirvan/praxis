package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers/snstopic"
)

func TestSNSTopicAdapter_BuildKeyAndDecodeSpec(t *testing.T) {
	adapter := NewSNSTopicAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"SNSTopic",
		"metadata":{"name":"alerts"},
		"spec":{
			"region":"us-east-1",
			"topicName":"alerts",
			"displayName":"Alert Topic",
			"fifoTopic":false,
			"tags":{"env":"prod"}
		}
	}`)

	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~alerts", key)
	assert.Equal(t, KeyScopeRegion, adapter.Scope())

	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed, ok := decoded.(snstopic.SNSTopicSpec)
	require.True(t, ok)
	assert.Equal(t, "us-east-1", typed.Region)
	assert.Equal(t, "alerts", typed.TopicName)
	assert.Equal(t, "Alert Topic", typed.DisplayName)
	assert.False(t, typed.FifoTopic)
}

func TestSNSTopicAdapter_BuildKey_TopicNameFromMetadata(t *testing.T) {
	adapter := NewSNSTopicAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"SNSTopic",
		"metadata":{"name":"my-topic"},
		"spec":{
			"region":"us-west-2",
			"tags":{}
		}
	}`)
	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "us-west-2~my-topic", key)
}

func TestSNSTopicAdapter_BuildImportKey(t *testing.T) {
	adapter := NewSNSTopicAdapterWithAuth(nil)
	key, err := adapter.BuildImportKey("us-east-1", "my-topic")
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~my-topic", key)
}

func TestSNSTopicAdapter_BuildImportKey_FromARN(t *testing.T) {
	adapter := NewSNSTopicAdapterWithAuth(nil)
	key, err := adapter.BuildImportKey("us-east-1", "arn:aws:sns:us-east-1:123456789012:my-topic")
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~my-topic", key)
}

func TestSNSTopicAdapter_NormalizeOutputs(t *testing.T) {
	adapter := NewSNSTopicAdapterWithAuth(nil)
	out, err := adapter.NormalizeOutputs(snstopic.SNSTopicOutputs{
		TopicArn:  "arn:aws:sns:us-east-1:123456789012:my-topic",
		TopicName: "my-topic",
		Owner:     "123456789012",
	})
	require.NoError(t, err)
	assert.Equal(t, "arn:aws:sns:us-east-1:123456789012:my-topic", out["topicArn"])
	assert.Equal(t, "my-topic", out["topicName"])
	assert.Equal(t, "123456789012", out["owner"])
}

func TestSNSTopicAdapter_NormalizeOutputs_NoOwner(t *testing.T) {
	adapter := NewSNSTopicAdapterWithAuth(nil)
	out, err := adapter.NormalizeOutputs(snstopic.SNSTopicOutputs{
		TopicArn:  "arn:aws:sns:us-east-1:123:topic",
		TopicName: "topic",
	})
	require.NoError(t, err)
	_, hasOwner := out["owner"]
	assert.False(t, hasOwner, "owner should be omitted when empty")
}

func TestSNSTopicAdapter_Kind(t *testing.T) {
	adapter := NewSNSTopicAdapterWithAuth(nil)
	assert.Equal(t, snstopic.ServiceName, adapter.Kind())
	assert.Equal(t, snstopic.ServiceName, adapter.ServiceName())
}

func TestSNSTopicAdapter_Scope(t *testing.T) {
	adapter := NewSNSTopicAdapterWithAuth(nil)
	assert.Equal(t, KeyScopeRegion, adapter.Scope())
}

func TestSNSTopicAdapter_DecodeSpec_MissingRegion(t *testing.T) {
	adapter := NewSNSTopicAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1","kind":"SNSTopic",
		"metadata":{"name":"topic"},
		"spec":{"topicName":"topic"}
	}`)
	_, err := adapter.DecodeSpec(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "region")
}

func TestSNSTopicAdapter_DecodeSpec_MissingName(t *testing.T) {
	adapter := NewSNSTopicAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1","kind":"SNSTopic",
		"metadata":{"name":""},
		"spec":{"region":"us-east-1"}
	}`)
	_, err := adapter.DecodeSpec(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name")
}
