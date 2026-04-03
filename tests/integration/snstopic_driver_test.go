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

	"github.com/shirvan/praxis/internal/drivers/snstopic"
	"github.com/shirvan/praxis/pkg/types"
)

func TestSNSTopic_Provision(t *testing.T) {
	t.Parallel()
	client, snsClient := setupSNSTopicDriver(t)
	topicName := uniqueTopicName(t)
	key := fmt.Sprintf("us-east-1~%s", topicName)

	outputs, err := ingress.Object[snstopic.SNSTopicSpec, snstopic.SNSTopicOutputs](
		client, snstopic.ServiceName, key, "Provision",
	).Request(t.Context(), snstopic.SNSTopicSpec{
		Account:     integrationAccountName,
		Region:      "us-east-1",
		TopicName:   topicName,
		DisplayName: "Test Topic",
		Tags:        map[string]string{"env": "test"},
	})
	require.NoError(t, err)
	assert.Equal(t, topicName, outputs.TopicName)
	assert.NotEmpty(t, outputs.TopicArn)
	assert.Contains(t, outputs.TopicArn, topicName)

	// Verify topic exists in LocalStack
	attrs, err := snsClient.GetTopicAttributes(context.Background(), &snssdk.GetTopicAttributesInput{
		TopicArn: aws.String(outputs.TopicArn),
	})
	require.NoError(t, err)
	assert.Equal(t, "Test Topic", attrs.Attributes["DisplayName"])
}

func TestSNSTopic_Provision_Idempotent(t *testing.T) {
	t.Parallel()
	client, _ := setupSNSTopicDriver(t)
	topicName := uniqueTopicName(t)
	key := fmt.Sprintf("us-east-1~%s", topicName)
	spec := snstopic.SNSTopicSpec{
		Account:     integrationAccountName,
		Region:      "us-east-1",
		TopicName:   topicName,
		DisplayName: "Test Topic",
	}

	out1, err := ingress.Object[snstopic.SNSTopicSpec, snstopic.SNSTopicOutputs](
		client, snstopic.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)

	out2, err := ingress.Object[snstopic.SNSTopicSpec, snstopic.SNSTopicOutputs](
		client, snstopic.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)

	assert.Equal(t, out1.TopicArn, out2.TopicArn)
	assert.Equal(t, out1.TopicName, out2.TopicName)
}

func TestSNSTopic_Import(t *testing.T) {
	t.Parallel()
	client, snsClient := setupSNSTopicDriver(t)
	topicName := uniqueTopicName(t)

	// Create topic directly in LocalStack
	created, err := snsClient.CreateTopic(context.Background(), &snssdk.CreateTopicInput{
		Name: aws.String(topicName),
	})
	require.NoError(t, err)

	key := fmt.Sprintf("us-east-1~%s", topicName)
	outputs, err := ingress.Object[types.ImportRef, snstopic.SNSTopicOutputs](
		client, snstopic.ServiceName, key, "Import",
	).Request(t.Context(), types.ImportRef{
		ResourceID: aws.ToString(created.TopicArn),
		Mode:       types.ModeObserved,
		Account:    integrationAccountName,
	})
	require.NoError(t, err)
	assert.Equal(t, topicName, outputs.TopicName)
	assert.Equal(t, aws.ToString(created.TopicArn), outputs.TopicArn)

	// Verify status is observed after import
	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, snstopic.ServiceName, key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, status.Mode)
}

func TestSNSTopic_Delete(t *testing.T) {
	t.Parallel()
	client, snsClient := setupSNSTopicDriver(t)
	topicName := uniqueTopicName(t)
	key := fmt.Sprintf("us-east-1~%s", topicName)

	outputs, err := ingress.Object[snstopic.SNSTopicSpec, snstopic.SNSTopicOutputs](
		client, snstopic.ServiceName, key, "Provision",
	).Request(t.Context(), snstopic.SNSTopicSpec{
		Account:   integrationAccountName,
		Region:    "us-east-1",
		TopicName: topicName,
	})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](
		client, snstopic.ServiceName, key, "Delete",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	// Verify topic is gone
	_, err = snsClient.GetTopicAttributes(context.Background(), &snssdk.GetTopicAttributesInput{
		TopicArn: aws.String(outputs.TopicArn),
	})
	require.Error(t, err, "topic should be deleted from LocalStack")
}

func TestSNSTopic_Reconcile_DetectsDisplayNameDrift(t *testing.T) {
	t.Parallel()
	client, snsClient := setupSNSTopicDriver(t)
	topicName := uniqueTopicName(t)
	key := fmt.Sprintf("us-east-1~%s", topicName)

	// Provision with DisplayName
	outputs, err := ingress.Object[snstopic.SNSTopicSpec, snstopic.SNSTopicOutputs](
		client, snstopic.ServiceName, key, "Provision",
	).Request(t.Context(), snstopic.SNSTopicSpec{
		Account:     integrationAccountName,
		Region:      "us-east-1",
		TopicName:   topicName,
		DisplayName: "Original Name",
	})
	require.NoError(t, err)

	// Introduce drift: change display name directly via SNS API
	_, err = snsClient.SetTopicAttributes(context.Background(), &snssdk.SetTopicAttributesInput{
		TopicArn:       aws.String(outputs.TopicArn),
		AttributeName:  aws.String("DisplayName"),
		AttributeValue: aws.String("Drifted Name"),
	})
	require.NoError(t, err)

	// Trigger reconcile
	result, err := ingress.Object[restate.Void, types.ReconcileResult](
		client, snstopic.ServiceName, key, "Reconcile",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift, "drift should be detected")
	assert.True(t, result.Correcting, "managed mode should correct drift")
}

func TestSNSTopic_GetStatus(t *testing.T) {
	t.Parallel()
	client, _ := setupSNSTopicDriver(t)
	topicName := uniqueTopicName(t)
	key := fmt.Sprintf("us-east-1~%s", topicName)

	_, err := ingress.Object[snstopic.SNSTopicSpec, snstopic.SNSTopicOutputs](
		client, snstopic.ServiceName, key, "Provision",
	).Request(t.Context(), snstopic.SNSTopicSpec{
		Account:   integrationAccountName,
		Region:    "us-east-1",
		TopicName: topicName,
	})
	require.NoError(t, err)

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, snstopic.ServiceName, key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeManaged, status.Mode)
	assert.Greater(t, status.Generation, int64(0))
}
