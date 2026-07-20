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
	"github.com/shirvan/praxis/internal/core/authservice"

	"github.com/shirvan/praxis/internal/drivers/esm"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
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
	ensureLambdaRole(t)

	awsCfg := motoAWSConfig(t)
	lambdaClient := awsclient.NewLambdaClient(awsCfg)
	driver := esm.NewGenericEventSourceMappingDriver(authservice.NewAuthClient())

	ingressClient := setupDriverEventingEnv(t, driver)
	return ingressClient, lambdaClient
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
	awsCfg := motoAWSConfig(t)
	sqsClient := sqssdk.NewFromConfig(awsCfg)

	out, err := sqsClient.CreateQueue(context.Background(), &sqssdk.CreateQueueInput{
		QueueName: aws.String(name),
	})
	require.NoError(t, err, "SQS API must be available in the integration environment")

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

	outputs, err := ingress.Object[types.ProvisionRequest, esm.EventSourceMappingOutputs](
		client, esm.ServiceName, key, "Provision",
	).Request(t.Context(), provisionRequest(t, spec))
	require.NoError(t, err)
	assert.NotEmpty(t, outputs.UUID)
	assert.Equal(t, queueArn, outputs.EventSourceArn)
	assert.NotEmpty(t, outputs.FunctionArn)
	assert.Equal(t, int32(10), outputs.BatchSize)

	// Verify mapping exists in Moto
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

	out1, err := ingress.Object[types.ProvisionRequest, esm.EventSourceMappingOutputs](
		client, esm.ServiceName, key, "Provision",
	).Request(t.Context(), provisionRequest(t, spec))
	require.NoError(t, err)

	out2, err := ingress.Object[types.ProvisionRequest, esm.EventSourceMappingOutputs](
		client, esm.ServiceName, key, "Provision",
	).Request(t.Context(), provisionRequest(t, spec))
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

	outputs, err := ingress.Object[types.ProvisionRequest, esm.EventSourceMappingOutputs](
		client, esm.ServiceName, key, "Provision",
	).Request(t.Context(), provisionRequest(t, spec))
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

func TestESMReconcile_DetectsBatchSizeDrift(t *testing.T) {
	client, lambdaClient := setupESMDriver(t)
	funcName := uniqueESMName(t)
	queueName := fmt.Sprintf("queue-%s", funcName)

	createTestFunctionForESM(t, lambdaClient, funcName)
	queueArn := createTestSQSQueue(t, queueName)

	encodedSource := base64.RawURLEncoding.EncodeToString([]byte(queueArn))
	key := fmt.Sprintf("us-east-1~%s~%s", funcName, encodedSource)

	outputs, err := ingress.Object[types.ProvisionRequest, esm.EventSourceMappingOutputs](
		client, esm.ServiceName, key, "Provision",
	).Request(t.Context(), provisionRequest(t, esm.EventSourceMappingSpec{
		Account:        integrationAccountName,
		Region:         "us-east-1",
		FunctionName:   funcName,
		EventSourceArn: queueArn,
		Enabled:        true,
		BatchSize:      aws.Int32(10),
	}))
	require.NoError(t, err)
	require.NotEmpty(t, outputs.UUID)

	// Externally change the batch size to introduce drift.
	_, err = lambdaClient.UpdateEventSourceMapping(context.Background(), &lambdasdk.UpdateEventSourceMappingInput{
		UUID:      aws.String(outputs.UUID),
		BatchSize: aws.Int32(25),
	})
	require.NoError(t, err)

	// Verify the external mutation landed before reconciling; otherwise there
	// is no observable drift and the scenario can only run against real AWS.
	desc, err := lambdaClient.GetEventSourceMapping(context.Background(), &lambdasdk.GetEventSourceMappingInput{
		UUID: aws.String(outputs.UUID),
	})
	require.NoError(t, err)
	if aws.ToInt32(desc.BatchSize) != 25 {
		t.Skip("Moto does not apply UpdateEventSourceMapping BatchSize")
	}

	// Automatic reconciliation restores the declared batch size.
	result, err := ingress.Object[restate.Void, types.ReconcileResult](
		client, esm.ServiceName, key, "Reconcile",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift, "drift should be detected")
	assert.True(t, result.Correcting)

	desc, err = lambdaClient.GetEventSourceMapping(context.Background(), &lambdasdk.GetEventSourceMappingInput{
		UUID: aws.String(outputs.UUID),
	})
	require.NoError(t, err)
	assert.Equal(t, int32(10), aws.ToInt32(desc.BatchSize), "automatic reconciliation should restore the declared value")
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

	_, err := ingress.Object[types.ProvisionRequest, esm.EventSourceMappingOutputs](
		client, esm.ServiceName, key, "Provision",
	).Request(t.Context(), provisionRequest(t, spec))
	require.NoError(t, err)

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, esm.ServiceName, key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeManaged, status.Mode)
}
