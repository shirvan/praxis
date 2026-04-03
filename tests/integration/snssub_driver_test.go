//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	snssdk "github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"

	"github.com/shirvan/praxis/internal/drivers/snssub"
	"github.com/shirvan/praxis/pkg/types"
)

func TestSNSSubscription_Provision(t *testing.T) {
	t.Parallel()
	client, snsClient := setupSNSSubDriver(t)
	topicName := uniqueTopicName(t)
	topicArn := createTopicDirect(t, snsClient, topicName)
	queueName := fmt.Sprintf("sub-queue-%s", topicName)
	queueArn := createSQSQueueARNDirect(t, queueName)
	key := fmt.Sprintf("us-east-1~%s~sqs~%s", topicName, queueName)

	outputs, err := ingress.Object[snssub.SNSSubscriptionSpec, snssub.SNSSubscriptionOutputs](
		client, snssub.ServiceName, key, "Provision",
	).Request(t.Context(), snssub.SNSSubscriptionSpec{
		Account:  integrationAccountName,
		Region:   "us-east-1",
		TopicArn: topicArn,
		Protocol: "sqs",
		Endpoint: queueArn,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, outputs.SubscriptionArn)
	assert.Equal(t, topicArn, outputs.TopicArn)
	assert.Equal(t, "sqs", outputs.Protocol)
	assert.Equal(t, queueArn, outputs.Endpoint)

	// Verify subscription exists in LocalStack
	subs, err := snsClient.ListSubscriptionsByTopic(context.Background(), &snssdk.ListSubscriptionsByTopicInput{
		TopicArn: aws.String(topicArn),
	})
	require.NoError(t, err)
	require.NotEmpty(t, subs.Subscriptions, "subscription should exist")
}

func TestSNSSubscription_Import(t *testing.T) {
	t.Parallel()
	client, snsClient := setupSNSSubDriver(t)
	topicName := uniqueTopicName(t)
	topicArn := createTopicDirect(t, snsClient, topicName)
	queueName := fmt.Sprintf("sub-queue-%s", topicName)
	queueArn := createSQSQueueARNDirect(t, queueName)

	// Create subscription directly
	subOut, err := snsClient.Subscribe(context.Background(), &snssdk.SubscribeInput{
		TopicArn:              aws.String(topicArn),
		Protocol:              aws.String("sqs"),
		Endpoint:              aws.String(queueArn),
		ReturnSubscriptionArn: true,
	})
	require.NoError(t, err)

	key := fmt.Sprintf("us-east-1~%s~sqs~%s", topicName, queueName)
	outputs, err := ingress.Object[types.ImportRef, snssub.SNSSubscriptionOutputs](
		client, snssub.ServiceName, key, "Import",
	).Request(t.Context(), types.ImportRef{
		ResourceID: aws.ToString(subOut.SubscriptionArn),
		Mode:       types.ModeObserved,
		Account:    integrationAccountName,
	})
	require.NoError(t, err)
	assert.Equal(t, aws.ToString(subOut.SubscriptionArn), outputs.SubscriptionArn)
	assert.Equal(t, topicArn, outputs.TopicArn)

	// Verify status is observed after import
	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, snssub.ServiceName, key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, status.Mode)
}

func TestSNSSubscription_Delete(t *testing.T) {
	t.Parallel()
	client, snsClient := setupSNSSubDriver(t)
	topicName := uniqueTopicName(t)
	topicArn := createTopicDirect(t, snsClient, topicName)
	queueName := fmt.Sprintf("sub-queue-%s", topicName)
	queueArn := createSQSQueueARNDirect(t, queueName)
	key := fmt.Sprintf("us-east-1~%s~sqs~%s", topicName, queueName)

	outputs, err := ingress.Object[snssub.SNSSubscriptionSpec, snssub.SNSSubscriptionOutputs](
		client, snssub.ServiceName, key, "Provision",
	).Request(t.Context(), snssub.SNSSubscriptionSpec{
		Account:  integrationAccountName,
		Region:   "us-east-1",
		TopicArn: topicArn,
		Protocol: "sqs",
		Endpoint: queueArn,
	})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](
		client, snssub.ServiceName, key, "Delete",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	// Verify subscription is gone
	_, err = snsClient.GetSubscriptionAttributes(context.Background(), &snssdk.GetSubscriptionAttributesInput{
		SubscriptionArn: aws.String(outputs.SubscriptionArn),
	})
	require.Error(t, err, "subscription should be deleted from LocalStack")
}

func TestSNSSubscription_GetStatus(t *testing.T) {
	t.Parallel()
	client, snsClient := setupSNSSubDriver(t)
	topicName := uniqueTopicName(t)
	topicArn := createTopicDirect(t, snsClient, topicName)
	queueName := fmt.Sprintf("sub-queue-%s", topicName)
	queueArn := createSQSQueueARNDirect(t, queueName)
	key := fmt.Sprintf("us-east-1~%s~sqs~%s", topicName, queueName)

	_, err := ingress.Object[snssub.SNSSubscriptionSpec, snssub.SNSSubscriptionOutputs](
		client, snssub.ServiceName, key, "Provision",
	).Request(t.Context(), snssub.SNSSubscriptionSpec{
		Account:  integrationAccountName,
		Region:   "us-east-1",
		TopicArn: topicArn,
		Protocol: "sqs",
		Endpoint: queueArn,
	})
	require.NoError(t, err)

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, snssub.ServiceName, key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeManaged, status.Mode)
	assert.Greater(t, status.Generation, int64(0))
}
