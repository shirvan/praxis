package snstopic

import (
	"testing"

	"github.com/shirvan/praxis/pkg/types"
	"github.com/stretchr/testify/assert"
)

func TestServiceName(t *testing.T) {
	drv := NewSNSTopicDriver(nil)
	assert.Equal(t, "SNSTopic", drv.ServiceName())
}

func TestSpecFromObserved_Standard(t *testing.T) {
	obs := ObservedState{
		TopicArn:                  "arn:aws:sns:us-east-1:123456789012:my-topic",
		TopicName:                 "my-topic",
		DisplayName:               "My Topic",
		FifoTopic:                 false,
		ContentBasedDeduplication: false,
		Policy:                    `{"Version":"2012-10-17"}`,
		DeliveryPolicy:            "",
		KmsMasterKeyId:            "alias/my-key",
		Owner:                     "123456789012",
		Tags:                      map[string]string{"env": "prod", "praxis:managed-key": "us-east-1~my-topic"},
	}
	ref := types.ImportRef{Account: "production"}

	spec := specFromObserved(obs, ref)
	assert.Equal(t, "production", spec.Account)
	assert.Equal(t, "us-east-1", spec.Region)
	assert.Equal(t, "my-topic", spec.TopicName)
	assert.Equal(t, "My Topic", spec.DisplayName)
	assert.False(t, spec.FifoTopic)
	assert.False(t, spec.ContentBasedDeduplication)
	assert.Equal(t, `{"Version":"2012-10-17"}`, spec.Policy)
	assert.Empty(t, spec.DeliveryPolicy)
	assert.Equal(t, "alias/my-key", spec.KmsMasterKeyId)
	// Praxis tags should be filtered out
	assert.Equal(t, map[string]string{"env": "prod"}, spec.Tags)
}

func TestSpecFromObserved_Fifo(t *testing.T) {
	obs := ObservedState{
		TopicArn:                  "arn:aws:sns:eu-west-1:123456789012:alerts.fifo",
		TopicName:                 "alerts.fifo",
		FifoTopic:                 true,
		ContentBasedDeduplication: true,
		Tags:                      map[string]string{},
	}
	ref := types.ImportRef{}

	spec := specFromObserved(obs, ref)
	assert.Equal(t, "eu-west-1", spec.Region)
	assert.Equal(t, "alerts.fifo", spec.TopicName)
	assert.True(t, spec.FifoTopic)
	assert.True(t, spec.ContentBasedDeduplication)
}

func TestSpecFromObserved_NilTags(t *testing.T) {
	obs := ObservedState{
		TopicArn:  "arn:aws:sns:us-east-1:123456789012:test",
		TopicName: "test",
		Tags:      nil,
	}
	spec := specFromObserved(obs, types.ImportRef{})
	assert.Equal(t, map[string]string{}, spec.Tags)
}

func TestSpecFromObserved_RegionExtraction(t *testing.T) {
	obs := ObservedState{
		TopicArn:  "arn:aws:sns:ap-southeast-2:123456789012:my-topic",
		TopicName: "my-topic",
		Tags:      map[string]string{},
	}
	spec := specFromObserved(obs, types.ImportRef{})
	assert.Equal(t, "ap-southeast-2", spec.Region)
}
