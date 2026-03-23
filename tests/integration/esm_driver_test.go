//go:build integration

package integration

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	lambdasdk "github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	sqssdk "github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	restatetest "github.com/restatedev/sdk-go/testing"

	"github.com/praxiscloud/praxis/internal/drivers/esm"
	"github.com/praxiscloud/praxis/internal/infra/awsclient"
	"github.com/praxiscloud/praxis/pkg/types"
)

func uniqueESMName(t *testing.T) string {
	t.Helper()
	name := strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	name = strings.ReplaceAll(name, "_", "-")
	if len(name) > 40 {
		name = name[:40]
	}
	return fmt.Sprintf("%s-%d", name, time.Now().UnixNano()%100000)
}

func setupESMDriver(t *testing.T) (*ingress.Client, *lambdasdk.Client) {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := localstackAWSConfig(t)
	lambdaClient := awsclient.NewLambdaClient(awsCfg)
	driver := esm.NewEventSourceMappingDriver(nil)

	env := restatetest.Start(t, restate.Reflect(driver))
	return env.Ingress(), lambdaClient
}

// createTestFunctionForESM creates a Lambda function for ESM tests.
func createTestFunctionForESM(t *testing.T, lambdaClient *lambdasdk.Client, name string) string {
	t.Helper()
	out, err := lambdaClient.CreateFunction(context.Background(), &lambdasdk.CreateFunctionInput{
		FunctionName: aws.String(name),
		Role:         aws.String(testLambdaRole),
		Runtime:      lambdatypes.RuntimePython312,
		Handler:      aws.String("index.handler"),
		Code:         &lambdatypes.FunctionCode{ZipFile: minimalLambdaZipBytes()},
	})
	require.NoError(t, err)
	return aws.ToString(out.FunctionArn)
}

// createTestSQSQueue creates an SQS queue and returns its ARN.
func createTestSQSQueue(t *testing.T, name string) string {
	t.Helper()
	awsCfg := localstackAWSConfig(t)
	sqsClient := sqssdk.NewFromConfig(awsCfg)

	out, err := sqsClient.CreateQueue(context.Background(), &sqssdk.CreateQueueInput{
		QueueName: aws.String(name),
	})
	if err != nil {
		t.Skipf("SQS not available in LocalStack: %v", err)
	}

	attrs, err := sqsClient.GetQueueAttributes(context.Background(), &sqssdk.GetQueueAttributesInput{
		QueueUrl:       out.QueueUrl,
		AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn},
	})
	require.NoError(t, err)
	return attrs.Attributes["QueueArn"]
}

func TestESMProvision_CreatesSQSMapping(t *testing.T) {
	client, lambdaClient := setupESMDriver(t)
	funcName := uniqueESMName(t)
	queueName := fmt.Sprintf("queue-%s", funcName)

	createTestFunctionForESM(t, lambdaClient, funcName)
	queueArn := createTestSQSQueue(t, queueName)

	encodedSource := base64.RawURLEncoding.EncodeToString([]byte(queueArn))
	key := fmt.Sprintf("us-east-1~%s~%s", funcName, encodedSource)

	spec := esm.EventSourceMappingSpec{
		Account:        integrationAccountName,
		Region:         "us-east-1",
		FunctionName:   funcName,
		EventSourceArn: queueArn,
		Enabled:        true,
		BatchSize:      aws.Int32(10),
	}

	outputs, err := ingress.Object[esm.EventSourceMappingSpec, esm.EventSourceMappingOutputs](
		client, esm.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)
	assert.NotEmpty(t, outputs.UUID)
	assert.Equal(t, queueArn, outputs.EventSourceArn)
	assert.NotEmpty(t, outputs.FunctionArn)
	assert.Equal(t, int32(10), outputs.BatchSize)

	// Verify mapping exists in LocalStack
	desc, err := lambdaClient.GetEventSourceMapping(context.Background(), &lambdasdk.GetEventSourceMappingInput{
		UUID: aws.String(outputs.UUID),
	})
	require.NoError(t, err)
	assert.Equal(t, queueArn, aws.ToString(desc.EventSourceArn))
}

func TestESMProvision_Idempotent(t *testing.T) {
	client, lambdaClient := setupESMDriver(t)
	funcName := uniqueESMName(t)
	queueName := fmt.Sprintf("queue-%s", funcName)

	createTestFunctionForESM(t, lambdaClient, funcName)
	queueArn := createTestSQSQueue(t, queueName)

	encodedSource := base64.RawURLEncoding.EncodeToString([]byte(queueArn))
	key := fmt.Sprintf("us-east-1~%s~%s", funcName, encodedSource)

	spec := esm.EventSourceMappingSpec{
		Account:        integrationAccountName,
		Region:         "us-east-1",
		FunctionName:   funcName,
		EventSourceArn: queueArn,
		Enabled:        true,
		BatchSize:      aws.Int32(5),
	}

	out1, err := ingress.Object[esm.EventSourceMappingSpec, esm.EventSourceMappingOutputs](
		client, esm.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)

	out2, err := ingress.Object[esm.EventSourceMappingSpec, esm.EventSourceMappingOutputs](
		client, esm.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)
	assert.Equal(t, out1.UUID, out2.UUID)
}

func TestESMDelete_RemovesMapping(t *testing.T) {
	client, lambdaClient := setupESMDriver(t)
	funcName := uniqueESMName(t)
	queueName := fmt.Sprintf("queue-%s", funcName)

	createTestFunctionForESM(t, lambdaClient, funcName)
	queueArn := createTestSQSQueue(t, queueName)

	encodedSource := base64.RawURLEncoding.EncodeToString([]byte(queueArn))
	key := fmt.Sprintf("us-east-1~%s~%s", funcName, encodedSource)

	spec := esm.EventSourceMappingSpec{
		Account:        integrationAccountName,
		Region:         "us-east-1",
		FunctionName:   funcName,
		EventSourceArn: queueArn,
		Enabled:        true,
		BatchSize:      aws.Int32(10),
	}

	outputs, err := ingress.Object[esm.EventSourceMappingSpec, esm.EventSourceMappingOutputs](
		client, esm.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](
		client, esm.ServiceName, key, "Delete",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	// Verify mapping is removed
	_, err = lambdaClient.GetEventSourceMapping(context.Background(), &lambdasdk.GetEventSourceMappingInput{
		UUID: aws.String(outputs.UUID),
	})
	require.Error(t, err, "event source mapping should be deleted")
}

func TestESMGetStatus_ReturnsReady(t *testing.T) {
	client, lambdaClient := setupESMDriver(t)
	funcName := uniqueESMName(t)
	queueName := fmt.Sprintf("queue-%s", funcName)

	createTestFunctionForESM(t, lambdaClient, funcName)
	queueArn := createTestSQSQueue(t, queueName)

	encodedSource := base64.RawURLEncoding.EncodeToString([]byte(queueArn))
	key := fmt.Sprintf("us-east-1~%s~%s", funcName, encodedSource)

	spec := esm.EventSourceMappingSpec{
		Account:        integrationAccountName,
		Region:         "us-east-1",
		FunctionName:   funcName,
		EventSourceArn: queueArn,
		Enabled:        true,
		BatchSize:      aws.Int32(10),
	}

	_, err := ingress.Object[esm.EventSourceMappingSpec, esm.EventSourceMappingOutputs](
		client, esm.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, esm.ServiceName, key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeManaged, status.Mode)
}
