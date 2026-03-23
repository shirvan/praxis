//go:build integration

package integration

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	iamsdk "github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	restatetest "github.com/restatedev/sdk-go/testing"

	"github.com/praxiscloud/praxis/internal/drivers/iamrole"
	"github.com/praxiscloud/praxis/internal/infra/awsclient"
	"github.com/praxiscloud/praxis/pkg/types"
)

const lambdaAssumeRolePolicyDoc = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}`

func setupIAMRoleDriver(t *testing.T) (*ingress.Client, *iamsdk.Client) {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := localstackAWSConfig(t)
	iamClient := awsclient.NewIAMClient(awsCfg)
	driver := iamrole.NewIAMRoleDriver(nil)

	env := restatetest.Start(t, restate.Reflect(driver))
	return env.Ingress(), iamClient
}

func TestIAMRoleProvision_CreatesRole(t *testing.T) {
	client, iamClient := setupIAMRoleDriver(t)
	roleName := uniqueIAMName(t, "role")
	policyArn := createManagedPolicy(t, iamClient, uniqueIAMName(t, "policy"))

	outputs, err := ingress.Object[iamrole.IAMRoleSpec, iamrole.IAMRoleOutputs](client, iamrole.ServiceName, roleName, "Provision").Request(t.Context(), iamrole.IAMRoleSpec{
		Account:                  integrationAccountName,
		Path:                     "/app/",
		RoleName:                 roleName,
		AssumeRolePolicyDocument: assumeRolePolicyDoc,
		Description:              "integration test role",
		MaxSessionDuration:       3600,
		InlinePolicies:           map[string]string{"inline-access": allowAllS3PolicyDoc()},
		ManagedPolicyArns:        []string{policyArn},
		Tags:                     map[string]string{"env": "test"},
	})
	require.NoError(t, err)
	assert.Equal(t, roleName, outputs.RoleName)
	assert.NotEmpty(t, outputs.RoleId)

	roleOut, err := iamClient.GetRole(context.Background(), &iamsdk.GetRoleInput{RoleName: aws.String(roleName)})
	require.NoError(t, err)
	assert.Equal(t, "/app/", aws.ToString(roleOut.Role.Path))

	attachedOut, err := iamClient.ListAttachedRolePolicies(context.Background(), &iamsdk.ListAttachedRolePoliciesInput{RoleName: aws.String(roleName)})
	require.NoError(t, err)
	require.Len(t, attachedOut.AttachedPolicies, 1)
	assert.Equal(t, policyArn, aws.ToString(attachedOut.AttachedPolicies[0].PolicyArn))

	inlineOut, err := iamClient.GetRolePolicy(context.Background(), &iamsdk.GetRolePolicyInput{RoleName: aws.String(roleName), PolicyName: aws.String("inline-access")})
	require.NoError(t, err)
	decodedPolicy, _ := url.QueryUnescape(aws.ToString(inlineOut.PolicyDocument))
	assert.Contains(t, decodedPolicy, "s3:GetObject")
}

func TestIAMRoleProvision_UpdatesTrustPolicy(t *testing.T) {
	client, iamClient := setupIAMRoleDriver(t)
	roleName := uniqueIAMName(t, "role")
	spec := iamrole.IAMRoleSpec{
		Account:                  integrationAccountName,
		RoleName:                 roleName,
		AssumeRolePolicyDocument: assumeRolePolicyDoc,
		MaxSessionDuration:       3600,
	}

	_, err := ingress.Object[iamrole.IAMRoleSpec, iamrole.IAMRoleOutputs](client, iamrole.ServiceName, roleName, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)

	spec.AssumeRolePolicyDocument = lambdaAssumeRolePolicyDoc
	_, err = ingress.Object[iamrole.IAMRoleSpec, iamrole.IAMRoleOutputs](client, iamrole.ServiceName, roleName, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)

	roleOut, err := iamClient.GetRole(context.Background(), &iamsdk.GetRoleInput{RoleName: aws.String(roleName)})
	require.NoError(t, err)
	assert.Contains(t, aws.ToString(roleOut.Role.AssumeRolePolicyDocument), "lambda.amazonaws.com")
}

func TestIAMRoleImport_ExistingRoleDefaultsObserved(t *testing.T) {
	client, iamClient := setupIAMRoleDriver(t)
	roleName := uniqueIAMName(t, "role")
	_, err := iamClient.CreateRole(context.Background(), &iamsdk.CreateRoleInput{
		RoleName:                 aws.String(roleName),
		AssumeRolePolicyDocument: aws.String(assumeRolePolicyDoc),
	})
	if err != nil && strings.Contains(err.Error(), "Service 'iam' is not enabled") {
		t.Skip("LocalStack IAM service is not enabled; restart the local stack after the compose update")
	}
	require.NoError(t, err)

	outputs, err := ingress.Object[types.ImportRef, iamrole.IAMRoleOutputs](client, iamrole.ServiceName, roleName, "Import").Request(t.Context(), types.ImportRef{ResourceID: roleName, Account: integrationAccountName})
	require.NoError(t, err)
	assert.Equal(t, roleName, outputs.RoleName)

	status, err := ingress.Object[restate.Void, types.StatusResponse](client, iamrole.ServiceName, roleName, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, status.Mode)
}

func TestIAMRoleDelete_RemovesRole(t *testing.T) {
	client, iamClient := setupIAMRoleDriver(t)
	roleName := uniqueIAMName(t, "role")
	policyArn := createManagedPolicy(t, iamClient, uniqueIAMName(t, "policy"))

	_, err := ingress.Object[iamrole.IAMRoleSpec, iamrole.IAMRoleOutputs](client, iamrole.ServiceName, roleName, "Provision").Request(t.Context(), iamrole.IAMRoleSpec{
		Account:                  integrationAccountName,
		RoleName:                 roleName,
		AssumeRolePolicyDocument: assumeRolePolicyDoc,
		MaxSessionDuration:       3600,
		InlinePolicies:           map[string]string{"inline-access": allowAllS3PolicyDoc()},
		ManagedPolicyArns:        []string{policyArn},
	})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](client, iamrole.ServiceName, roleName, "Delete").Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	_, err = iamClient.GetRole(context.Background(), &iamsdk.GetRoleInput{RoleName: aws.String(roleName)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "NoSuchEntity")
}

func TestIAMRoleReconcile_DetectsDrift(t *testing.T) {
	client, iamClient := setupIAMRoleDriver(t)
	roleName := uniqueIAMName(t, "role")

	_, err := ingress.Object[iamrole.IAMRoleSpec, iamrole.IAMRoleOutputs](client, iamrole.ServiceName, roleName, "Provision").Request(t.Context(), iamrole.IAMRoleSpec{
		Account:                  integrationAccountName,
		RoleName:                 roleName,
		AssumeRolePolicyDocument: assumeRolePolicyDoc,
		MaxSessionDuration:       3600,
		InlinePolicies:           map[string]string{"inline-access": allowAllS3PolicyDoc()},
	})
	require.NoError(t, err)

	_, err = iamClient.PutRolePolicy(context.Background(), &iamsdk.PutRolePolicyInput{
		RoleName:       aws.String(roleName),
		PolicyName:     aws.String("extra-inline"),
		PolicyDocument: aws.String(denyAllS3PolicyDoc()),
	})
	require.NoError(t, err)

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, iamrole.ServiceName, roleName, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)

	inlineOut, err := iamClient.ListRolePolicies(context.Background(), &iamsdk.ListRolePoliciesInput{RoleName: aws.String(roleName)})
	require.NoError(t, err)
	assert.Equal(t, []string{"inline-access"}, inlineOut.PolicyNames)
}
