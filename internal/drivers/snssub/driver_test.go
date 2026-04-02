package snssub

import (
	"testing"

	"github.com/shirvan/praxis/pkg/types"
	"github.com/stretchr/testify/assert"
)

func TestServiceName(t *testing.T) {
	drv := NewSNSSubscriptionDriver(nil)
	assert.Equal(t, "SNSSubscription", drv.ServiceName())
}

func TestValidateProtocolConstraints_Lambda(t *testing.T) {
	assert.NoError(t, validateProtocolConstraints(SNSSubscriptionSpec{
		Protocol: "lambda",
		Endpoint: "arn:aws:lambda:us-east-1:123:function:my-func",
	}))
	assert.Error(t, validateProtocolConstraints(SNSSubscriptionSpec{
		Protocol: "lambda",
		Endpoint: "not-a-lambda-arn",
	}))
}

func TestValidateProtocolConstraints_SQS(t *testing.T) {
	assert.NoError(t, validateProtocolConstraints(SNSSubscriptionSpec{
		Protocol: "sqs",
		Endpoint: "arn:aws:sqs:us-east-1:123:my-queue",
	}))
	assert.Error(t, validateProtocolConstraints(SNSSubscriptionSpec{
		Protocol: "sqs",
		Endpoint: "not-a-sqs-arn",
	}))
}

func TestValidateProtocolConstraints_Firehose(t *testing.T) {
	assert.NoError(t, validateProtocolConstraints(SNSSubscriptionSpec{
		Protocol:            "firehose",
		Endpoint:            "arn:aws:firehose:us-east-1:123:deliverystream/stream",
		SubscriptionRoleArn: "arn:aws:iam::123:role/role",
	}))
	// Missing role
	assert.Error(t, validateProtocolConstraints(SNSSubscriptionSpec{
		Protocol: "firehose",
		Endpoint: "arn:aws:firehose:us-east-1:123:deliverystream/stream",
	}))
	// Bad endpoint
	assert.Error(t, validateProtocolConstraints(SNSSubscriptionSpec{
		Protocol:            "firehose",
		Endpoint:            "not-a-firehose-arn",
		SubscriptionRoleArn: "arn:aws:iam::123:role/role",
	}))
}

func TestValidateProtocolConstraints_Email(t *testing.T) {
	assert.NoError(t, validateProtocolConstraints(SNSSubscriptionSpec{
		Protocol: "email",
		Endpoint: "user@example.com",
	}))
	assert.Error(t, validateProtocolConstraints(SNSSubscriptionSpec{
		Protocol: "email",
		Endpoint: "not-an-email",
	}))
}

func TestValidateProtocolConstraints_EmailJSON(t *testing.T) {
	assert.NoError(t, validateProtocolConstraints(SNSSubscriptionSpec{
		Protocol: "email-json",
		Endpoint: "user@example.com",
	}))
}

func TestValidateProtocolConstraints_HTTP(t *testing.T) {
	assert.NoError(t, validateProtocolConstraints(SNSSubscriptionSpec{
		Protocol: "http",
		Endpoint: "http://example.com/hook",
	}))
	assert.Error(t, validateProtocolConstraints(SNSSubscriptionSpec{
		Protocol: "http",
		Endpoint: "https://example.com",
	}))
}

func TestValidateProtocolConstraints_HTTPS(t *testing.T) {
	assert.NoError(t, validateProtocolConstraints(SNSSubscriptionSpec{
		Protocol: "https",
		Endpoint: "https://example.com/hook",
	}))
	assert.Error(t, validateProtocolConstraints(SNSSubscriptionSpec{
		Protocol: "https",
		Endpoint: "http://example.com",
	}))
}

func TestValidateProtocolConstraints_SMS(t *testing.T) {
	assert.NoError(t, validateProtocolConstraints(SNSSubscriptionSpec{
		Protocol: "sms",
		Endpoint: "+15551234567",
	}))
	assert.Error(t, validateProtocolConstraints(SNSSubscriptionSpec{
		Protocol: "sms",
		Endpoint: "5551234567",
	}))
}

func TestValidateProtocolConstraints_RawMessageDelivery(t *testing.T) {
	// Supported for sqs
	assert.NoError(t, validateProtocolConstraints(SNSSubscriptionSpec{
		Protocol:           "sqs",
		Endpoint:           "arn:aws:sqs:us-east-1:123:q",
		RawMessageDelivery: true,
	}))
	// Not supported for email
	assert.Error(t, validateProtocolConstraints(SNSSubscriptionSpec{
		Protocol:           "email",
		Endpoint:           "user@example.com",
		RawMessageDelivery: true,
	}))
}

func TestValidateProtocolConstraints_DeliveryPolicy(t *testing.T) {
	// Supported for http
	assert.NoError(t, validateProtocolConstraints(SNSSubscriptionSpec{
		Protocol:       "http",
		Endpoint:       "http://example.com",
		DeliveryPolicy: `{"healthyRetryPolicy":{}}`,
	}))
	// Not supported for sqs
	assert.Error(t, validateProtocolConstraints(SNSSubscriptionSpec{
		Protocol:       "sqs",
		Endpoint:       "arn:aws:sqs:us-east-1:123:q",
		DeliveryPolicy: `{"healthyRetryPolicy":{}}`,
	}))
}

func TestSpecFromObserved_Standard(t *testing.T) {
	obs := ObservedState{
		SubscriptionArn:     "arn:aws:sns:us-east-1:123:my-topic:sub-id",
		TopicArn:            "arn:aws:sns:us-east-1:123:my-topic",
		Protocol:            "sqs",
		Endpoint:            "arn:aws:sqs:us-east-1:123:my-queue",
		FilterPolicy:        `{"event":["order"]}`,
		FilterPolicyScope:   "MessageAttributes",
		RawMessageDelivery:  true,
		SubscriptionRoleArn: "",
	}
	ref := types.ImportRef{Account: "prod"}

	spec := specFromObserved(obs, ref)
	assert.Equal(t, "prod", spec.Account)
	assert.Equal(t, "us-east-1", spec.Region)
	assert.Equal(t, "arn:aws:sns:us-east-1:123:my-topic", spec.TopicArn)
	assert.Equal(t, "sqs", spec.Protocol)
	assert.Equal(t, "arn:aws:sqs:us-east-1:123:my-queue", spec.Endpoint)
	assert.Equal(t, `{"event":["order"]}`, spec.FilterPolicy)
	assert.Equal(t, "MessageAttributes", spec.FilterPolicyScope)
	assert.True(t, spec.RawMessageDelivery)
	assert.Empty(t, spec.SubscriptionRoleArn)
}

func TestSpecFromObserved_RegionExtraction(t *testing.T) {
	obs := ObservedState{
		SubscriptionArn: "arn:aws:sns:ap-northeast-1:123:topic:sub-id",
	}
	spec := specFromObserved(obs, types.ImportRef{})
	assert.Equal(t, "ap-northeast-1", spec.Region)
}
