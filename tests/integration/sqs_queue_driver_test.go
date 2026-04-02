//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	sqssdk "github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"

	"github.com/shirvan/praxis/internal/drivers/sqs"
	"github.com/shirvan/praxis/pkg/types"
)

func TestSQSQueueProvision_CreatesStandardQueue(t *testing.T) {
	client, sqsClient := setupSQSQueueDriver(t)
	queueName := uniqueQueueName(t, false)
	key := fmt.Sprintf("us-east-1~%s", queueName)

	outputs, err := ingress.Object[sqs.SQSQueueSpec, sqs.SQSQueueOutputs](
		client, sqs.ServiceName, key, "Provision",
	).Request(t.Context(), sqs.SQSQueueSpec{
		Account:                       integrationAccountName,
		Region:                        "us-east-1",
		QueueName:                     queueName,
		VisibilityTimeout:             45,
		ReceiveMessageWaitTimeSeconds: 5,
		Tags:                          map[string]string{"env": "test"},
	})
	require.NoError(t, err)
	assert.Equal(t, queueName, outputs.QueueName)
	assert.NotEmpty(t, outputs.QueueUrl)
	assert.NotEmpty(t, outputs.QueueArn)

	attrs, err := sqsClient.GetQueueAttributes(context.Background(), &sqssdk.GetQueueAttributesInput{
		QueueUrl: aws.String(outputs.QueueUrl),
		AttributeNames: []sqstypes.QueueAttributeName{
			sqstypes.QueueAttributeNameQueueArn,
			sqstypes.QueueAttributeNameVisibilityTimeout,
			sqstypes.QueueAttributeNameReceiveMessageWaitTimeSeconds,
		},
	})
	require.NoError(t, err)
	assert.Equal(t, outputs.QueueArn, attrs.Attributes[string(sqstypes.QueueAttributeNameQueueArn)])
	assert.Equal(t, "45", attrs.Attributes[string(sqstypes.QueueAttributeNameVisibilityTimeout)])
	assert.Equal(t, "5", attrs.Attributes[string(sqstypes.QueueAttributeNameReceiveMessageWaitTimeSeconds)])
}

func TestSQSQueueProvision_CreatesFIFOQueue(t *testing.T) {
	client, sqsClient := setupSQSQueueDriver(t)
	queueName := uniqueQueueName(t, true)
	key := fmt.Sprintf("us-east-1~%s", queueName)

	outputs, err := ingress.Object[sqs.SQSQueueSpec, sqs.SQSQueueOutputs](
		client, sqs.ServiceName, key, "Provision",
	).Request(t.Context(), sqs.SQSQueueSpec{
		Account:                    integrationAccountName,
		Region:                     "us-east-1",
		QueueName:                  queueName,
		FifoQueue:                  true,
		ContentBasedDeduplication:  true,
		DeduplicationScope:         "messageGroup",
		FifoThroughputLimit:        "perMessageGroupId",
	})
	require.NoError(t, err)

	attrs, err := sqsClient.GetQueueAttributes(context.Background(), &sqssdk.GetQueueAttributesInput{
		QueueUrl: aws.String(outputs.QueueUrl),
		AttributeNames: []sqstypes.QueueAttributeName{
			sqstypes.QueueAttributeNameFifoQueue,
			sqstypes.QueueAttributeNameContentBasedDeduplication,
			sqstypes.QueueAttributeNameDeduplicationScope,
			sqstypes.QueueAttributeNameFifoThroughputLimit,
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "true", attrs.Attributes[string(sqstypes.QueueAttributeNameFifoQueue)])
	assert.Equal(t, "true", attrs.Attributes[string(sqstypes.QueueAttributeNameContentBasedDeduplication)])
	assert.Equal(t, "messageGroup", attrs.Attributes[string(sqstypes.QueueAttributeNameDeduplicationScope)])
	assert.Equal(t, "perMessageGroupId", attrs.Attributes[string(sqstypes.QueueAttributeNameFifoThroughputLimit)])
}

func TestSQSQueueImport_ByURL(t *testing.T) {
	client, sqsClient := setupSQSQueueDriver(t)
	queueName := uniqueQueueName(t, false)

	created, err := sqsClient.CreateQueue(context.Background(), &sqssdk.CreateQueueInput{
		QueueName: aws.String(queueName),
	})
	require.NoError(t, err)

	key := fmt.Sprintf("us-east-1~%s", queueName)
	outputs, err := ingress.Object[types.ImportRef, sqs.SQSQueueOutputs](
		client, sqs.ServiceName, key, "Import",
	).Request(t.Context(), types.ImportRef{
		ResourceID: aws.ToString(created.QueueUrl),
		Mode:       types.ModeObserved,
		Account:    integrationAccountName,
	})
	require.NoError(t, err)
	assert.Equal(t, queueName, outputs.QueueName)
	assert.Equal(t, aws.ToString(created.QueueUrl), outputs.QueueUrl)

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, sqs.ServiceName, key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, status.Mode)
	assert.Equal(t, types.StatusReady, status.Status)
}

func TestSQSQueueDelete_RemovesQueue(t *testing.T) {
	client, sqsClient := setupSQSQueueDriver(t)
	queueName := uniqueQueueName(t, false)
	key := fmt.Sprintf("us-east-1~%s", queueName)

	outputs, err := ingress.Object[sqs.SQSQueueSpec, sqs.SQSQueueOutputs](
		client, sqs.ServiceName, key, "Provision",
	).Request(t.Context(), sqs.SQSQueueSpec{
		Account:   integrationAccountName,
		Region:    "us-east-1",
		QueueName: queueName,
	})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](
		client, sqs.ServiceName, key, "Delete",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	_, err = sqsClient.GetQueueAttributes(context.Background(), &sqssdk.GetQueueAttributesInput{
		QueueUrl: aws.String(outputs.QueueUrl),
		AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn},
	})
	require.Error(t, err)
	assert.True(t, sqs.IsNotFound(err), "expected deleted queue to be reported as not found: %v", err)
}