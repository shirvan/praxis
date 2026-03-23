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

	"github.com/shirvan/praxis/internal/drivers/iamgroup"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

func setupIAMGroupDriver(t *testing.T) (*ingress.Client, *iamsdk.Client) {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := localstackAWSConfig(t)
	iamClient := awsclient.NewIAMClient(awsCfg)
	ensureIAMEnabledForGroupTests(t, iamClient)
	driver := iamgroup.NewIAMGroupDriver(nil)

	env := restatetest.Start(t, restate.Reflect(driver))
	return env.Ingress(), iamClient
}

func ensureIAMEnabledForGroupTests(t *testing.T, iamClient *iamsdk.Client) {
	t.Helper()
	_, err := iamClient.ListGroups(context.Background(), &iamsdk.ListGroupsInput{MaxItems: aws.Int32(1)})
	if err != nil && strings.Contains(err.Error(), "Service 'iam' is not enabled") {
		t.Skip("LocalStack IAM service is not enabled; restart the local stack after the compose update")
	}
	require.NoError(t, err)
}

func createIAMUserForGroup(t *testing.T, iamClient *iamsdk.Client, userName string) {
	t.Helper()
	_, err := iamClient.CreateUser(context.Background(), &iamsdk.CreateUserInput{UserName: aws.String(userName)})
	if err != nil && strings.Contains(err.Error(), "Service 'iam' is not enabled") {
		t.Skip("LocalStack IAM service is not enabled; restart the local stack after the compose update")
	}
	require.NoError(t, err)
}

func TestIAMGroupProvision_CreatesGroup(t *testing.T) {
	client, iamClient := setupIAMGroupDriver(t)
	groupName := uniqueIAMName(t, "group")
	policyArn := createManagedPolicy(t, iamClient, uniqueIAMName(t, "policy"))

	outputs, err := ingress.Object[iamgroup.IAMGroupSpec, iamgroup.IAMGroupOutputs](client, iamgroup.ServiceName, groupName, "Provision").Request(t.Context(), iamgroup.IAMGroupSpec{
		Account:           integrationAccountName,
		Path:              "/app/",
		GroupName:         groupName,
		InlinePolicies:    map[string]string{"inline-access": allowAllS3PolicyDoc()},
		ManagedPolicyArns: []string{policyArn},
	})
	require.NoError(t, err)
	assert.Equal(t, groupName, outputs.GroupName)
	assert.NotEmpty(t, outputs.GroupId)

	groupOut, err := iamClient.GetGroup(context.Background(), &iamsdk.GetGroupInput{GroupName: aws.String(groupName)})
	require.NoError(t, err)
	assert.Equal(t, "/app/", aws.ToString(groupOut.Group.Path))

	attachedOut, err := iamClient.ListAttachedGroupPolicies(context.Background(), &iamsdk.ListAttachedGroupPoliciesInput{GroupName: aws.String(groupName)})
	require.NoError(t, err)
	require.Len(t, attachedOut.AttachedPolicies, 1)
	assert.Equal(t, policyArn, aws.ToString(attachedOut.AttachedPolicies[0].PolicyArn))

	inlineOut, err := iamClient.GetGroupPolicy(context.Background(), &iamsdk.GetGroupPolicyInput{GroupName: aws.String(groupName), PolicyName: aws.String("inline-access")})
	require.NoError(t, err)
	decodedPolicy, _ := url.QueryUnescape(aws.ToString(inlineOut.PolicyDocument))
	assert.Contains(t, decodedPolicy, "s3:GetObject")
}

func TestIAMGroupProvision_UpdatesPath(t *testing.T) {
	client, iamClient := setupIAMGroupDriver(t)
	groupName := uniqueIAMName(t, "group")

	spec := iamgroup.IAMGroupSpec{Account: integrationAccountName, Path: "/", GroupName: groupName}
	_, err := ingress.Object[iamgroup.IAMGroupSpec, iamgroup.IAMGroupOutputs](client, iamgroup.ServiceName, groupName, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)

	spec.Path = "/app/"
	_, err = ingress.Object[iamgroup.IAMGroupSpec, iamgroup.IAMGroupOutputs](client, iamgroup.ServiceName, groupName, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)

	groupOut, err := iamClient.GetGroup(context.Background(), &iamsdk.GetGroupInput{GroupName: aws.String(groupName)})
	require.NoError(t, err)
	assert.Equal(t, "/app/", aws.ToString(groupOut.Group.Path))
}

func TestIAMGroupImport_ExistingGroupDefaultsObserved(t *testing.T) {
	client, iamClient := setupIAMGroupDriver(t)
	groupName := uniqueIAMName(t, "group")

	_, err := iamClient.CreateGroup(context.Background(), &iamsdk.CreateGroupInput{GroupName: aws.String(groupName)})
	if err != nil && strings.Contains(err.Error(), "Service 'iam' is not enabled") {
		t.Skip("LocalStack IAM service is not enabled; restart the local stack after the compose update")
	}
	require.NoError(t, err)

	outputs, err := ingress.Object[types.ImportRef, iamgroup.IAMGroupOutputs](client, iamgroup.ServiceName, groupName, "Import").Request(t.Context(), types.ImportRef{ResourceID: groupName, Account: integrationAccountName})
	require.NoError(t, err)
	assert.Equal(t, groupName, outputs.GroupName)

	status, err := ingress.Object[restate.Void, types.StatusResponse](client, iamgroup.ServiceName, groupName, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, status.Mode)
}

func TestIAMGroupDelete_RemovesMembersAndGroup(t *testing.T) {
	client, iamClient := setupIAMGroupDriver(t)
	groupName := uniqueIAMName(t, "group")
	userName := uniqueIAMName(t, "user")
	createIAMUserForGroup(t, iamClient, userName)

	_, err := ingress.Object[iamgroup.IAMGroupSpec, iamgroup.IAMGroupOutputs](client, iamgroup.ServiceName, groupName, "Provision").Request(t.Context(), iamgroup.IAMGroupSpec{Account: integrationAccountName, GroupName: groupName})
	require.NoError(t, err)

	_, err = iamClient.AddUserToGroup(context.Background(), &iamsdk.AddUserToGroupInput{GroupName: aws.String(groupName), UserName: aws.String(userName)})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](client, iamgroup.ServiceName, groupName, "Delete").Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	_, err = iamClient.GetGroup(context.Background(), &iamsdk.GetGroupInput{GroupName: aws.String(groupName)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "NoSuchEntity")
}

func TestIAMGroupReconcile_DetectsInlinePolicyDrift(t *testing.T) {
	client, iamClient := setupIAMGroupDriver(t)
	groupName := uniqueIAMName(t, "group")

	_, err := ingress.Object[iamgroup.IAMGroupSpec, iamgroup.IAMGroupOutputs](client, iamgroup.ServiceName, groupName, "Provision").Request(t.Context(), iamgroup.IAMGroupSpec{
		Account:        integrationAccountName,
		GroupName:      groupName,
		InlinePolicies: map[string]string{"inline-access": allowAllS3PolicyDoc()},
	})
	require.NoError(t, err)

	deny := denyAllS3PolicyDoc()
	_, err = iamClient.PutGroupPolicy(context.Background(), &iamsdk.PutGroupPolicyInput{GroupName: aws.String(groupName), PolicyName: aws.String("inline-access"), PolicyDocument: aws.String(deny)})
	require.NoError(t, err)

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, iamgroup.ServiceName, groupName, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)

	inlineOut, err := iamClient.GetGroupPolicy(context.Background(), &iamsdk.GetGroupPolicyInput{GroupName: aws.String(groupName), PolicyName: aws.String("inline-access")})
	require.NoError(t, err)
	decodedPolicy2, _ := url.QueryUnescape(aws.ToString(inlineOut.PolicyDocument))
	assert.Contains(t, decodedPolicy2, "s3:GetObject")
}

func TestIAMGroupGetStatus_ReturnsReady(t *testing.T) {
	client, _ := setupIAMGroupDriver(t)
	groupName := uniqueIAMName(t, "group")

	_, err := ingress.Object[iamgroup.IAMGroupSpec, iamgroup.IAMGroupOutputs](client, iamgroup.ServiceName, groupName, "Provision").Request(t.Context(), iamgroup.IAMGroupSpec{Account: integrationAccountName, GroupName: groupName})
	require.NoError(t, err)

	status, err := ingress.Object[restate.Void, types.StatusResponse](client, iamgroup.ServiceName, groupName, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeManaged, status.Mode)
	assert.Greater(t, status.Generation, int64(0))
}
