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
		Account:                   integrationAccountName,
		Region:                    "us-east-1",
		QueueName:                 queueName,
		FifoQueue:                 true,
		ContentBasedDeduplication: true,
		DeduplicationScope:        "messageGroup",
		FifoThroughputLimit:       "perMessageGroupId",
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
		QueueUrl:       aws.String(outputs.QueueUrl),
		AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn},
	})
	require.Error(t, err)
	assert.True(t, sqs.IsNotFound(err), "expected deleted queue to be reported as not found: %v", err)
}

func TestSQSQueueProvision_UpdatePreservesSeparateQueuePolicy(t *testing.T) {
	client, sqsClient := setupSQSQueueDriver(t)
	queueName := uniqueQueueName(t, false)
	key := fmt.Sprintf("us-east-1~%s", queueName)
	spec := sqs.SQSQueueSpec{
		Account: integrationAccountName, Region: "us-east-1", QueueName: queueName,
		VisibilityTimeout: 30, Tags: map[string]string{"env": "initial"},
	}

	outputs, err := ingress.Object[sqs.SQSQueueSpec, sqs.SQSQueueOutputs](
		client, sqs.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)
	policy := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"sqs:GetQueueAttributes","Resource":%q}]}`, outputs.QueueArn)
	_, err = sqsClient.SetQueueAttributes(t.Context(), &sqssdk.SetQueueAttributesInput{
		QueueUrl: aws.String(outputs.QueueUrl), Attributes: map[string]string{"Policy": policy},
	})
	require.NoError(t, err)
	baseline, err := sqsClient.GetQueueAttributes(t.Context(), &sqssdk.GetQueueAttributesInput{
		QueueUrl:       aws.String(outputs.QueueUrl),
		AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNamePolicy},
	})
	require.NoError(t, err)
	if baseline.Attributes[string(sqstypes.QueueAttributeNamePolicy)] == "" {
		t.Skip("Moto accepts SetQueueAttributes(Policy) but does not persist/return it; request-shape ownership is covered by the stateful driver test")
	}
	_, err = sqsClient.SetQueueAttributes(t.Context(), &sqssdk.SetQueueAttributesInput{
		QueueUrl: aws.String(outputs.QueueUrl), Attributes: map[string]string{"VisibilityTimeout": "60"},
	})
	require.NoError(t, err)
	control, err := sqsClient.GetQueueAttributes(t.Context(), &sqssdk.GetQueueAttributesInput{
		QueueUrl:       aws.String(outputs.QueueUrl),
		AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNamePolicy},
	})
	require.NoError(t, err)
	if control.Attributes[string(sqstypes.QueueAttributeNamePolicy)] == "" {
		t.Skip("Moto clears Policy on an unrelated direct SetQueueAttributes call; it cannot verify AWS policy preservation")
	}

	spec.VisibilityTimeout = 75
	spec.Tags = map[string]string{"env": "updated"}
	_, err = ingress.Object[sqs.SQSQueueSpec, sqs.SQSQueueOutputs](
		client, sqs.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)

	attrs, err := sqsClient.GetQueueAttributes(t.Context(), &sqssdk.GetQueueAttributesInput{
		QueueUrl: aws.String(outputs.QueueUrl), AttributeNames: []sqstypes.QueueAttributeName{
			sqstypes.QueueAttributeNameVisibilityTimeout, sqstypes.QueueAttributeNamePolicy,
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "75", attrs.Attributes[string(sqstypes.QueueAttributeNameVisibilityTimeout)])
	assert.JSONEq(t, policy, attrs.Attributes[string(sqstypes.QueueAttributeNamePolicy)],
		"SQSQueue convergence must not absorb or overwrite the separate SQSQueuePolicy resource")
	tags, err := sqsClient.ListQueueTags(t.Context(), &sqssdk.ListQueueTagsInput{QueueUrl: aws.String(outputs.QueueUrl)})
	require.NoError(t, err)
	assert.Equal(t, key, tags.Tags["praxis:managed-key"])
}

func TestSQSQueueImport_ByARN(t *testing.T) {
	client, sqsClient := setupSQSQueueDriver(t)
	queueName := uniqueQueueName(t, false)
	created, err := sqsClient.CreateQueue(t.Context(), &sqssdk.CreateQueueInput{QueueName: aws.String(queueName)})
	require.NoError(t, err)
	attrs, err := sqsClient.GetQueueAttributes(t.Context(), &sqssdk.GetQueueAttributesInput{
		QueueUrl:       aws.String(aws.ToString(created.QueueUrl)),
		AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn},
	})
	require.NoError(t, err)
	queueARN := attrs.Attributes[string(sqstypes.QueueAttributeNameQueueArn)]

	key := fmt.Sprintf("us-east-1~%s", queueName)
	outputs, err := ingress.Object[types.ImportRef, sqs.SQSQueueOutputs](
		client, sqs.ServiceName, key, "Import",
	).Request(t.Context(), types.ImportRef{ResourceID: queueARN, Account: integrationAccountName})
	require.NoError(t, err)
	assert.Equal(t, queueARN, outputs.QueueArn)
	assert.Equal(t, queueName, outputs.QueueName)

	tags, err := sqsClient.ListQueueTags(t.Context(), &sqssdk.ListQueueTagsInput{QueueUrl: created.QueueUrl})
	require.NoError(t, err)
	assert.NotContains(t, tags.Tags, "praxis:managed-key", "Observed import must remain provider-read-only")
}
