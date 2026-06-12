//go:build integration

package integration

import (
	"context"
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
	"github.com/shirvan/praxis/internal/core/authservice"

	"github.com/shirvan/praxis/internal/drivers/lambdaperm"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

func uniqueStatementId(t *testing.T) string {
	t.Helper()
	name := strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	name = strings.ReplaceAll(name, "_", "-")
	if len(name) > 40 {
		name = name[:40]
	}
	return fmt.Sprintf("%s-%d", name, time.Now().UnixNano()%100000)
}

// createTestFunction creates a Lambda function directly in Moto for permission tests.
func createTestFunction(t *testing.T, lambdaClient *lambdasdk.Client, name string) {
	t.Helper()
	_, err := lambdaClient.CreateFunction(context.Background(), &lambdasdk.CreateFunctionInput{
		FunctionName: aws.String(name),
		Role:         aws.String(testLambdaRole),
		Runtime:      lambdatypes.RuntimePython312,
		Handler:      aws.String("index.handler"),
		Code:         &lambdatypes.FunctionCode{ZipFile: minimalLambdaZipBytes()},
	})
	require.NoError(t, err)
}

func setupPermissionDriver(t *testing.T) (*ingress.Client, *lambdasdk.Client) {
	t.Helper()
	configureLocalAccount(t)
	ensureLambdaRole(t)

	awsCfg := motoAWSConfig(t)
	lambdaClient := awsclient.NewLambdaClient(awsCfg)
	driver := lambdaperm.NewLambdaPermissionDriver(authservice.NewAuthClient())

	ingressClient := setupDriverEventingEnv(t, driver)
	return ingressClient, lambdaClient
}

func TestLambdaPermissionProvision_AddsPermission(t *testing.T) {
	client, lambdaClient := setupPermissionDriver(t)
	funcName := uniqueFunctionName(t)
	stmtId := uniqueStatementId(t)
	createTestFunction(t, lambdaClient, funcName)

	key := fmt.Sprintf("us-east-1~%s~%s", funcName, stmtId)
	spec := lambdaperm.LambdaPermissionSpec{
		Account:      integrationAccountName,
		Region:       "us-east-1",
		FunctionName: funcName,
		StatementId:  stmtId,
		Action:       "lambda:InvokeFunction",
		Principal:    "s3.amazonaws.com",
		SourceArn:    "arn:aws:s3:::my-bucket",
	}

	outputs, err := ingress.Object[lambdaperm.LambdaPermissionSpec, lambdaperm.LambdaPermissionOutputs](
		client, lambdaperm.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)
	assert.Equal(t, stmtId, outputs.StatementId)
	assert.Equal(t, funcName, outputs.FunctionName)
	assert.NotEmpty(t, outputs.Statement)

	// Verify permission exists in Moto
	policy, err := lambdaClient.GetPolicy(context.Background(), &lambdasdk.GetPolicyInput{
		FunctionName: aws.String(funcName),
	})
	require.NoError(t, err)
	assert.Contains(t, aws.ToString(policy.Policy), stmtId)
}

func TestLambdaPermissionProvision_Idempotent(t *testing.T) {
	client, lambdaClient := setupPermissionDriver(t)
	funcName := uniqueFunctionName(t)
	stmtId := uniqueStatementId(t)
	createTestFunction(t, lambdaClient, funcName)

	key := fmt.Sprintf("us-east-1~%s~%s", funcName, stmtId)
	spec := lambdaperm.LambdaPermissionSpec{
		Account:      integrationAccountName,
		Region:       "us-east-1",
		FunctionName: funcName,
		StatementId:  stmtId,
		Action:       "lambda:InvokeFunction",
		Principal:    "s3.amazonaws.com",
	}

	out1, err := ingress.Object[lambdaperm.LambdaPermissionSpec, lambdaperm.LambdaPermissionOutputs](
		client, lambdaperm.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)

	out2, err := ingress.Object[lambdaperm.LambdaPermissionSpec, lambdaperm.LambdaPermissionOutputs](
		client, lambdaperm.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)
	assert.Equal(t, out1.StatementId, out2.StatementId)
}

func TestLambdaPermissionDelete_RemovesPermission(t *testing.T) {
	client, lambdaClient := setupPermissionDriver(t)
	funcName := uniqueFunctionName(t)
	stmtId := uniqueStatementId(t)
	createTestFunction(t, lambdaClient, funcName)

	key := fmt.Sprintf("us-east-1~%s~%s", funcName, stmtId)
	spec := lambdaperm.LambdaPermissionSpec{
		Account:      integrationAccountName,
		Region:       "us-east-1",
		FunctionName: funcName,
		StatementId:  stmtId,
		Action:       "lambda:InvokeFunction",
		Principal:    "events.amazonaws.com",
	}

	_, err := ingress.Object[lambdaperm.LambdaPermissionSpec, lambdaperm.LambdaPermissionOutputs](
		client, lambdaperm.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](
		client, lambdaperm.ServiceName, key, "Delete",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	// Verify permission is removed — GetPolicy should fail or not contain the statement
	policy, getErr := lambdaClient.GetPolicy(context.Background(), &lambdasdk.GetPolicyInput{
		FunctionName: aws.String(funcName),
	})
	if getErr == nil {
		assert.NotContains(t, aws.ToString(policy.Policy), stmtId)
	}
}

func TestLambdaPermissionReconcile_DetectsExternalDelete(t *testing.T) {
	client, lambdaClient := setupPermissionDriver(t)
	funcName := uniqueFunctionName(t)
	stmtId := uniqueStatementId(t)
	createTestFunction(t, lambdaClient, funcName)

	key := fmt.Sprintf("us-east-1~%s~%s", funcName, stmtId)
	_, err := ingress.Object[lambdaperm.LambdaPermissionSpec, lambdaperm.LambdaPermissionOutputs](
		client, lambdaperm.ServiceName, key, "Provision",
	).Request(t.Context(), lambdaperm.LambdaPermissionSpec{
		Account:      integrationAccountName,
		Region:       "us-east-1",
		FunctionName: funcName,
		StatementId:  stmtId,
		Action:       "lambda:InvokeFunction",
		Principal:    "s3.amazonaws.com",
		SourceArn:    "arn:aws:s3:::my-bucket",
	})
	require.NoError(t, err)

	// Externally remove the permission to introduce drift.
	_, err = lambdaClient.RemovePermission(context.Background(), &lambdasdk.RemovePermissionInput{
		FunctionName: aws.String(funcName),
		StatementId:  aws.String(stmtId),
	})
	require.NoError(t, err)

	// Permissions are replace-only: the driver flags the external delete as an
	// error rather than re-adding the statement (detect-only behavior).
	result, err := ingress.Object[restate.Void, types.ReconcileResult](
		client, lambdaperm.ServiceName, key, "Reconcile",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Contains(t, result.Error, "deleted externally", "external delete should be reported")
	assert.False(t, result.Correcting, "permission driver is detect-only; no correction is performed")

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, lambdaperm.ServiceName, key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusError, status.Status, "status should flag the external delete")
}

func TestLambdaPermissionGetStatus_ReturnsReady(t *testing.T) {
	client, lambdaClient := setupPermissionDriver(t)
	funcName := uniqueFunctionName(t)
	stmtId := uniqueStatementId(t)
	createTestFunction(t, lambdaClient, funcName)

	key := fmt.Sprintf("us-east-1~%s~%s", funcName, stmtId)
	spec := lambdaperm.LambdaPermissionSpec{
		Account:      integrationAccountName,
		Region:       "us-east-1",
		FunctionName: funcName,
		StatementId:  stmtId,
		Action:       "lambda:InvokeFunction",
		Principal:    "s3.amazonaws.com",
	}

	_, err := ingress.Object[lambdaperm.LambdaPermissionSpec, lambdaperm.LambdaPermissionOutputs](
		client, lambdaperm.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, lambdaperm.ServiceName, key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeManaged, status.Mode)
}
