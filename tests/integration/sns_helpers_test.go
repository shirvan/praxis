//go:build integration

package integration

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	snssdk "github.com/aws/aws-sdk-go-v2/service/sns"
	sqssdk "github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/require"

	"github.com/restatedev/sdk-go/ingress"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/snssub"
	"github.com/shirvan/praxis/internal/drivers/snstopic"
	"github.com/shirvan/praxis/internal/infra/awsclient"
)

func uniqueTopicName(t *testing.T) string {
	t.Helper()
	name := strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	name = strings.ReplaceAll(name, "_", "-")
	if len(name) > 50 {
		name = name[:50]
	}
	return fmt.Sprintf("praxis-test-%s-%d", name, time.Now().UnixNano()%100000)
}

func requireSNSAvailable(t *testing.T, client *snssdk.Client) {
	t.Helper()
	_, err := client.ListTopics(context.Background(), &snssdk.ListTopicsInput{})
	require.NoError(t, err, "SNS API must be available in the integration environment")
}

func setupSNSTopicDriver(t *testing.T) (*ingress.Client, *snssdk.Client) {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := motoAWSConfig(t)
	snsClient := awsclient.NewSNSClient(awsCfg)
	requireSNSAvailable(t, snsClient)

	return setupDriverEventingEnv(t, snstopic.NewGenericSNSTopicDriver(authservice.NewAuthClient())), snsClient
}

func setupSNSSubDriver(t *testing.T) (*ingress.Client, *snssdk.Client) {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := motoAWSConfig(t)
	snsClient := awsclient.NewSNSClient(awsCfg)
	requireSNSAvailable(t, snsClient)

	return setupDriverEventingEnv(t, snssub.NewGenericSNSSubscriptionDriver(authservice.NewAuthClient())), snsClient
}

// createTopicDirect creates an SNS topic directly via the SDK (for subscription tests).
func createTopicDirect(t *testing.T, snsClient *snssdk.Client, name string) string {
	t.Helper()
	out, err := snsClient.CreateTopic(context.Background(), &snssdk.CreateTopicInput{
		Name: &name,
	})
	if err != nil {
		t.Fatalf("failed to create prerequisite SNS topic %s: %v", name, err)
	}
	return *out.TopicArn
}

// createSQSQueueARNDirect creates an SQS queue directly via the SDK and returns its ARN.
func createSQSQueueARNDirect(t *testing.T, queueName string) string {
	t.Helper()
	awsCfg := motoAWSConfig(t)
	sqsClient := awsclient.NewSQSClient(awsCfg)

	out, err := sqsClient.CreateQueue(context.Background(), &sqssdk.CreateQueueInput{
		QueueName: &queueName,
	})
	if err != nil {
		t.Fatalf("failed to create prerequisite SQS queue %s: %v", queueName, err)
	}

	attrs, err := sqsClient.GetQueueAttributes(context.Background(), &sqssdk.GetQueueAttributesInput{
		QueueUrl:       out.QueueUrl,
		AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn},
	})
	if err != nil {
		t.Fatalf("failed to get SQS queue ARN for %s: %v", queueName, err)
	}
	return attrs.Attributes[string(sqstypes.QueueAttributeNameQueueArn)]
}
