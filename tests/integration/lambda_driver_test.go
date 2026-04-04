//go:build integration

package integration

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	lambdasdk "github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	restatetest "github.com/restatedev/sdk-go/testing"

	"github.com/shirvan/praxis/internal/drivers/lambda"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

const testLambdaRole = "arn:aws:iam::000000000000:role/lambda-role"

func uniqueFunctionName(t *testing.T) string {
	t.Helper()
	name := strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	name = strings.ReplaceAll(name, "_", "-")
	if len(name) > 50 {
		name = name[:50]
	}
	return fmt.Sprintf("%s-%d", name, time.Now().UnixNano()%100000)
}

// minimalLambdaZip returns a base64-encoded zip containing a minimal Python handler.
func minimalLambdaZip() string {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, _ := w.Create("index.py")
	f.Write([]byte("def handler(event, context): return {\"statusCode\": 200}\n"))
	w.Close()
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// minimalLambdaZipBytes returns raw zip bytes for direct use with the Lambda SDK.
func minimalLambdaZipBytes() []byte {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, _ := w.Create("index.py")
	f.Write([]byte("def handler(event, context): return {\"statusCode\": 200}\n"))
	w.Close()
	return buf.Bytes()
}

func setupLambdaDriver(t *testing.T) (*ingress.Client, *lambdasdk.Client) {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := localstackAWSConfig(t)
	lambdaClient := awsclient.NewLambdaClient(awsCfg)
	driver := lambda.NewLambdaFunctionDriver(nil)

	env := restatetest.Start(t, restate.Reflect(driver))
	return env.Ingress(), lambdaClient
}

func defaultLambdaSpec(name string) lambda.LambdaFunctionSpec {
	return lambda.LambdaFunctionSpec{
		Account:      integrationAccountName,
		Region:       "us-east-1",
		FunctionName: name,
		Role:         testLambdaRole,
		Runtime:      "python3.12",
		Handler:      "index.handler",
		MemorySize:   128,
		Timeout:      30,
		Code:         lambda.CodeSpec{ZipFile: minimalLambdaZip()},
		Tags:         map[string]string{"env": "test"},
	}
}

func TestLambdaProvision_CreatesFunction(t *testing.T) {
	client, lambdaClient := setupLambdaDriver(t)
	name := uniqueFunctionName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	outputs, err := ingress.Object[lambda.LambdaFunctionSpec, lambda.LambdaFunctionOutputs](
		client, lambda.ServiceName, key, "Provision",
	).Request(t.Context(), defaultLambdaSpec(name))
	require.NoError(t, err)
	assert.Equal(t, name, outputs.FunctionName)
	assert.NotEmpty(t, outputs.FunctionArn)
	assert.Contains(t, outputs.FunctionArn, name)

	// Verify function exists in Moto
	desc, err := lambdaClient.GetFunction(context.Background(), &lambdasdk.GetFunctionInput{
		FunctionName: aws.String(name),
	})
	require.NoError(t, err)
	assert.Equal(t, name, aws.ToString(desc.Configuration.FunctionName))
	assert.Equal(t, int32(128), aws.ToInt32(desc.Configuration.MemorySize))
}

func TestLambdaProvision_Idempotent(t *testing.T) {
	client, _ := setupLambdaDriver(t)
	name := uniqueFunctionName(t)
	key := fmt.Sprintf("us-east-1~%s", name)
	spec := defaultLambdaSpec(name)

	out1, err := ingress.Object[lambda.LambdaFunctionSpec, lambda.LambdaFunctionOutputs](
		client, lambda.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)

	out2, err := ingress.Object[lambda.LambdaFunctionSpec, lambda.LambdaFunctionOutputs](
		client, lambda.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)
	assert.Equal(t, out1.FunctionArn, out2.FunctionArn)
}

func TestLambdaProvision_UpdatesConfiguration(t *testing.T) {
	client, lambdaClient := setupLambdaDriver(t)
	name := uniqueFunctionName(t)
	key := fmt.Sprintf("us-east-1~%s", name)
	spec := defaultLambdaSpec(name)

	_, err := ingress.Object[lambda.LambdaFunctionSpec, lambda.LambdaFunctionOutputs](
		client, lambda.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)

	// Update memory and timeout
	spec.MemorySize = 256
	spec.Timeout = 60
	spec.Description = "updated"

	out, err := ingress.Object[lambda.LambdaFunctionSpec, lambda.LambdaFunctionOutputs](
		client, lambda.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)
	assert.Equal(t, name, out.FunctionName)

	// Verify update in Moto
	desc, err := lambdaClient.GetFunction(context.Background(), &lambdasdk.GetFunctionInput{
		FunctionName: aws.String(name),
	})
	require.NoError(t, err)
	assert.Equal(t, int32(256), aws.ToInt32(desc.Configuration.MemorySize))
	assert.Equal(t, int32(60), aws.ToInt32(desc.Configuration.Timeout))
}

func TestLambdaImport_ExistingFunction(t *testing.T) {
	client, lambdaClient := setupLambdaDriver(t)
	name := uniqueFunctionName(t)

	// Create function directly in Moto
	_, err := lambdaClient.CreateFunction(context.Background(), &lambdasdk.CreateFunctionInput{
		FunctionName: aws.String(name),
		Role:         aws.String(testLambdaRole),
		Runtime:      lambdatypes.RuntimePython312,
		Handler:      aws.String("index.handler"),
		Code:         &lambdatypes.FunctionCode{ZipFile: minimalLambdaZipBytes()},
	})
	require.NoError(t, err)

	key := fmt.Sprintf("us-east-1~%s", name)
	outputs, err := ingress.Object[types.ImportRef, lambda.LambdaFunctionOutputs](
		client, lambda.ServiceName, key, "Import",
	).Request(t.Context(), types.ImportRef{
		ResourceID: name,
		Account:    integrationAccountName,
		Mode:       types.ModeManaged,
	})
	require.NoError(t, err)
	assert.Equal(t, name, outputs.FunctionName)
	assert.Contains(t, outputs.FunctionArn, name)

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, lambda.ServiceName, key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
}

func TestLambdaDelete_RemovesFunction(t *testing.T) {
	client, lambdaClient := setupLambdaDriver(t)
	name := uniqueFunctionName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	_, err := ingress.Object[lambda.LambdaFunctionSpec, lambda.LambdaFunctionOutputs](
		client, lambda.ServiceName, key, "Provision",
	).Request(t.Context(), defaultLambdaSpec(name))
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](
		client, lambda.ServiceName, key, "Delete",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	// Verify function is gone
	_, err = lambdaClient.GetFunction(context.Background(), &lambdasdk.GetFunctionInput{
		FunctionName: aws.String(name),
	})
	require.Error(t, err, "function should be deleted from Moto")
}

func TestLambdaGetStatus_ReturnsReady(t *testing.T) {
	client, _ := setupLambdaDriver(t)
	name := uniqueFunctionName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	_, err := ingress.Object[lambda.LambdaFunctionSpec, lambda.LambdaFunctionOutputs](
		client, lambda.ServiceName, key, "Provision",
	).Request(t.Context(), defaultLambdaSpec(name))
	require.NoError(t, err)

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, lambda.ServiceName, key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeManaged, status.Mode)
	assert.Greater(t, status.Generation, int64(0))
}

func TestLambdaReconcile_DetectsDrift(t *testing.T) {
	client, lambdaClient := setupLambdaDriver(t)
	name := uniqueFunctionName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	_, err := ingress.Object[lambda.LambdaFunctionSpec, lambda.LambdaFunctionOutputs](
		client, lambda.ServiceName, key, "Provision",
	).Request(t.Context(), defaultLambdaSpec(name))
	require.NoError(t, err)

	// Introduce drift: change description directly via Lambda API
	_, err = lambdaClient.UpdateFunctionConfiguration(context.Background(), &lambdasdk.UpdateFunctionConfigurationInput{
		FunctionName: aws.String(name),
		Description:  aws.String("drifted-description"),
	})
	require.NoError(t, err)

	result, err := ingress.Object[restate.Void, types.ReconcileResult](
		client, lambda.ServiceName, key, "Reconcile",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift, "drift should be detected")
	assert.True(t, result.Correcting, "managed mode should correct drift")
}
