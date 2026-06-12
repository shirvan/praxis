//go:build integration

package integration

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	sqssdk "github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/restatedev/sdk-go/ingress"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/sqs"
	"github.com/shirvan/praxis/internal/drivers/sqspolicy"
	"github.com/shirvan/praxis/internal/infra/awsclient"
)

func uniqueQueueName(t *testing.T, fifo bool) string {
	t.Helper()
	name := strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	name = strings.ReplaceAll(name, "_", "-")
	if len(name) > 60 {
		name = name[:60]
	}
	name = fmt.Sprintf("%s-%d", name, time.Now().UnixNano()%100000)
	if fifo {
		name += ".fifo"
	}
	return name
}

func skipIfSQSUnavailable(t *testing.T, client *sqssdk.Client) {
	t.Helper()
	_, err := client.ListQueues(context.Background(), &sqssdk.ListQueuesInput{})
	if err != nil {
		t.Skipf("SQS API unavailable in test environment: %v", err)
	}
}

func setupSQSQueueDriver(t *testing.T) (*ingress.Client, *sqssdk.Client) {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := motoAWSConfig(t)
	sqsClient := awsclient.NewSQSClient(awsCfg)
	skipIfSQSUnavailable(t, sqsClient)

	return setupDriverEventingEnv(t, sqs.NewSQSQueueDriver(authservice.NewAuthClient())), sqsClient
}

func setupSQSQueuePolicyDriver(t *testing.T) (*ingress.Client, *sqssdk.Client) {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := motoAWSConfig(t)
	sqsClient := awsclient.NewSQSClient(awsCfg)
	skipIfSQSUnavailable(t, sqsClient)

	return setupDriverEventingEnv(t, sqspolicy.NewSQSQueuePolicyDriver(authservice.NewAuthClient())), sqsClient
}
