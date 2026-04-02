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

	"github.com/shirvan/praxis/internal/drivers/sqspolicy"
	"github.com/shirvan/praxis/pkg/types"
)

func testQueuePolicy(queueArn string) string {
	return fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Sid":"AllowAccount","Effect":"Allow","Principal":{"AWS":"*"},"Action":"sqs:SendMessage","Resource":"%s"}]}`,
		queueArn,
	)
}

func TestSQSQueuePolicyProvision_SetsPolicy(t *testing.T) {
	client, sqsClient := setupSQSQueuePolicyDriver(t)
	queueName := uniqueQueueName(t, false)

	created, err := sqsClient.CreateQueue(context.Background(), &sqssdk.CreateQueueInput{
		QueueName: aws.String(queueName),
	})
	require.NoError(t, err)

	attrs, err := sqsClient.GetQueueAttributes(context.Background(), &sqssdk.GetQueueAttributesInput{
		QueueUrl: created.QueueUrl,
		AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn},
	})
	require.NoError(t, err)
	queueArn := attrs.Attributes[string(sqstypes.QueueAttributeNameQueueArn)]

	key := fmt.Sprintf("us-east-1~%s", queueName)
	outputs, err := ingress.Object[sqspolicy.SQSQueuePolicySpec, sqspolicy.SQSQueuePolicyOutputs](
		client, sqspolicy.ServiceName, key, "Provision",
	).Request(t.Context(), sqspolicy.SQSQueuePolicySpec{
		Account:   integrationAccountName,
		Region:    "us-east-1",
		QueueName: queueName,
		Policy:    testQueuePolicy(queueArn),
	})
	require.NoError(t, err)
	assert.Equal(t, queueName, outputs.QueueName)
	assert.Equal(t, aws.ToString(created.QueueUrl), outputs.QueueUrl)

	policyAttrs, err := sqsClient.GetQueueAttributes(context.Background(), &sqssdk.GetQueueAttributesInput{
		QueueUrl: created.QueueUrl,
		AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNamePolicy},
	})
	require.NoError(t, err)
	assert.JSONEq(t, testQueuePolicy(queueArn), policyAttrs.Attributes[string(sqstypes.QueueAttributeNamePolicy)])
}

func TestSQSQueuePolicyImport_ExistingPolicy(t *testing.T) {
	client, sqsClient := setupSQSQueuePolicyDriver(t)
	queueName := uniqueQueueName(t, false)

	created, err := sqsClient.CreateQueue(context.Background(), &sqssdk.CreateQueueInput{
		QueueName: aws.String(queueName),
	})
	require.NoError(t, err)

	attrs, err := sqsClient.GetQueueAttributes(context.Background(), &sqssdk.GetQueueAttributesInput{
		QueueUrl: created.QueueUrl,
		AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn},
	})
	require.NoError(t, err)
	queueArn := attrs.Attributes[string(sqstypes.QueueAttributeNameQueueArn)]

	policy := testQueuePolicy(queueArn)
	_, err = sqsClient.SetQueueAttributes(context.Background(), &sqssdk.SetQueueAttributesInput{
		QueueUrl: created.QueueUrl,
		Attributes: map[string]string{
			string(sqstypes.QueueAttributeNamePolicy): policy,
		},
	})
	require.NoError(t, err)

	key := fmt.Sprintf("us-east-1~%s", queueName)
	outputs, err := ingress.Object[types.ImportRef, sqspolicy.SQSQueuePolicyOutputs](
		client, sqspolicy.ServiceName, key, "Import",
	).Request(t.Context(), types.ImportRef{
		ResourceID: queueName,
		Mode:       types.ModeObserved,
		Account:    integrationAccountName,
	})
	require.NoError(t, err)
	assert.Equal(t, queueName, outputs.QueueName)
	assert.Equal(t, aws.ToString(created.QueueUrl), outputs.QueueUrl)

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, sqspolicy.ServiceName, key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, status.Mode)
	assert.Equal(t, types.StatusReady, status.Status)
}

func TestSQSQueuePolicyDelete_RemovesPolicy(t *testing.T) {
	client, sqsClient := setupSQSQueuePolicyDriver(t)
	queueName := uniqueQueueName(t, false)

	created, err := sqsClient.CreateQueue(context.Background(), &sqssdk.CreateQueueInput{
		QueueName: aws.String(queueName),
	})
	require.NoError(t, err)

	attrs, err := sqsClient.GetQueueAttributes(context.Background(), &sqssdk.GetQueueAttributesInput{
		QueueUrl: created.QueueUrl,
		AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn},
	})
	require.NoError(t, err)
	queueArn := attrs.Attributes[string(sqstypes.QueueAttributeNameQueueArn)]

	key := fmt.Sprintf("us-east-1~%s", queueName)
	_, err = ingress.Object[sqspolicy.SQSQueuePolicySpec, sqspolicy.SQSQueuePolicyOutputs](
		client, sqspolicy.ServiceName, key, "Provision",
	).Request(t.Context(), sqspolicy.SQSQueuePolicySpec{
		Account:   integrationAccountName,
		Region:    "us-east-1",
		QueueName: queueName,
		Policy:    testQueuePolicy(queueArn),
	})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](
		client, sqspolicy.ServiceName, key, "Delete",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	policyAttrs, err := sqsClient.GetQueueAttributes(context.Background(), &sqssdk.GetQueueAttributesInput{
		QueueUrl: created.QueueUrl,
		AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNamePolicy},
	})
	require.NoError(t, err)
	assert.Empty(t, policyAttrs.Attributes[string(sqstypes.QueueAttributeNamePolicy)])
}