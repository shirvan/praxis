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

	"github.com/praxiscloud/praxis/internal/drivers/iamuser"
	"github.com/praxiscloud/praxis/internal/infra/awsclient"
	"github.com/praxiscloud/praxis/pkg/types"
)

func setupIAMUserDriver(t *testing.T) (*ingress.Client, *iamsdk.Client) {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := localstackAWSConfig(t)
	iamClient := awsclient.NewIAMClient(awsCfg)
	driver := iamuser.NewIAMUserDriver(nil)

	env := restatetest.Start(t, restate.Reflect(driver))
	return env.Ingress(), iamClient
}

func createIAMGroup(t *testing.T, iamClient *iamsdk.Client, groupName string) {
	t.Helper()
	_, err := iamClient.CreateGroup(context.Background(), &iamsdk.CreateGroupInput{GroupName: aws.String(groupName)})
	if err != nil && strings.Contains(err.Error(), "Service 'iam' is not enabled") {
		t.Skip("LocalStack IAM service is not enabled; restart the local stack after the compose update")
	}
	require.NoError(t, err)
}

func createManagedPolicy(t *testing.T, iamClient *iamsdk.Client, name string) string {
	t.Helper()
	doc := allowAllS3PolicyDoc()
	out, err := iamClient.CreatePolicy(context.Background(), &iamsdk.CreatePolicyInput{PolicyName: aws.String(name), PolicyDocument: aws.String(doc)})
	if err != nil && strings.Contains(err.Error(), "Service 'iam' is not enabled") {
		t.Skip("LocalStack IAM service is not enabled; restart the local stack after the compose update")
	}
	require.NoError(t, err)
	return aws.ToString(out.Policy.Arn)
}

func createIAMUserDirect(t *testing.T, iamClient *iamsdk.Client, userName string) {
	t.Helper()
	_, err := iamClient.CreateUser(context.Background(), &iamsdk.CreateUserInput{UserName: aws.String(userName)})
	if err != nil && strings.Contains(err.Error(), "Service 'iam' is not enabled") {
		t.Skip("LocalStack IAM service is not enabled; restart the local stack after the compose update")
	}
	require.NoError(t, err)
}

func TestIAMUserProvision_CreatesUser(t *testing.T) {
	client, iamClient := setupIAMUserDriver(t)
	userName := uniqueIAMName(t, "user")
	groupName := uniqueIAMName(t, "group")
	policyName := uniqueIAMName(t, "policy")
	createIAMGroup(t, iamClient, groupName)
	policyArn := createManagedPolicy(t, iamClient, policyName)

	outputs, err := ingress.Object[iamuser.IAMUserSpec, iamuser.IAMUserOutputs](client, iamuser.ServiceName, userName, "Provision").Request(t.Context(), iamuser.IAMUserSpec{
		Account:           integrationAccountName,
		Path:              "/app/",
		UserName:          userName,
		InlinePolicies:    map[string]string{"inline-access": allowAllS3PolicyDoc()},
		ManagedPolicyArns: []string{policyArn},
		Groups:            []string{groupName},
		Tags:              map[string]string{"env": "test"},
	})
	require.NoError(t, err)
	assert.Equal(t, userName, outputs.UserName)
	assert.NotEmpty(t, outputs.UserId)

	userOut, err := iamClient.GetUser(context.Background(), &iamsdk.GetUserInput{UserName: aws.String(userName)})
	require.NoError(t, err)
	assert.Equal(t, "/app/", aws.ToString(userOut.User.Path))

	groupOut, err := iamClient.ListGroupsForUser(context.Background(), &iamsdk.ListGroupsForUserInput{UserName: aws.String(userName)})
	require.NoError(t, err)
	require.Len(t, groupOut.Groups, 1)
	assert.Equal(t, groupName, aws.ToString(groupOut.Groups[0].GroupName))

	policyOut, err := iamClient.ListAttachedUserPolicies(context.Background(), &iamsdk.ListAttachedUserPoliciesInput{UserName: aws.String(userName)})
	require.NoError(t, err)
	require.Len(t, policyOut.AttachedPolicies, 1)
	assert.Equal(t, policyArn, aws.ToString(policyOut.AttachedPolicies[0].PolicyArn))

	inlineOut, err := iamClient.GetUserPolicy(context.Background(), &iamsdk.GetUserPolicyInput{UserName: aws.String(userName), PolicyName: aws.String("inline-access")})
	require.NoError(t, err)
	decodedPolicy, _ := url.QueryUnescape(aws.ToString(inlineOut.PolicyDocument))
	assert.Contains(t, decodedPolicy, "s3:GetObject")
}

func TestIAMUserProvision_UpdatesPathAndGroups(t *testing.T) {
	client, iamClient := setupIAMUserDriver(t)
	userName := uniqueIAMName(t, "user")
	groupA := uniqueIAMName(t, "group-a")
	groupB := uniqueIAMName(t, "group-b")
	createIAMGroup(t, iamClient, groupA)
	createIAMGroup(t, iamClient, groupB)

	spec := iamuser.IAMUserSpec{
		Account:  integrationAccountName,
		Path:     "/",
		UserName: userName,
		Groups:   []string{groupA},
	}
	_, err := ingress.Object[iamuser.IAMUserSpec, iamuser.IAMUserOutputs](client, iamuser.ServiceName, userName, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)

	spec.Path = "/app/"
	spec.Groups = []string{groupA, groupB}
	_, err = ingress.Object[iamuser.IAMUserSpec, iamuser.IAMUserOutputs](client, iamuser.ServiceName, userName, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)

	userOut, err := iamClient.GetUser(context.Background(), &iamsdk.GetUserInput{UserName: aws.String(userName)})
	require.NoError(t, err)
	assert.Equal(t, "/app/", aws.ToString(userOut.User.Path))

	groupOut, err := iamClient.ListGroupsForUser(context.Background(), &iamsdk.ListGroupsForUserInput{UserName: aws.String(userName)})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{groupA, groupB}, []string{aws.ToString(groupOut.Groups[0].GroupName), aws.ToString(groupOut.Groups[1].GroupName)})
}

func TestIAMUserImport_ExistingUserDefaultsObserved(t *testing.T) {
	client, iamClient := setupIAMUserDriver(t)
	userName := uniqueIAMName(t, "user")

	createIAMUserDirect(t, iamClient, userName)

	outputs, err := ingress.Object[types.ImportRef, iamuser.IAMUserOutputs](client, iamuser.ServiceName, userName, "Import").Request(t.Context(), types.ImportRef{ResourceID: userName, Account: integrationAccountName})
	require.NoError(t, err)
	assert.Equal(t, userName, outputs.UserName)

	status, err := ingress.Object[restate.Void, types.StatusResponse](client, iamuser.ServiceName, userName, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, status.Mode)
}

func TestIAMUserDelete_RemovesUser(t *testing.T) {
	client, iamClient := setupIAMUserDriver(t)
	userName := uniqueIAMName(t, "user")
	groupName := uniqueIAMName(t, "group")
	policyName := uniqueIAMName(t, "policy")
	createIAMGroup(t, iamClient, groupName)
	policyArn := createManagedPolicy(t, iamClient, policyName)

	_, err := ingress.Object[iamuser.IAMUserSpec, iamuser.IAMUserOutputs](client, iamuser.ServiceName, userName, "Provision").Request(t.Context(), iamuser.IAMUserSpec{
		Account:           integrationAccountName,
		UserName:          userName,
		InlinePolicies:    map[string]string{"inline-access": allowAllS3PolicyDoc()},
		ManagedPolicyArns: []string{policyArn},
		Groups:            []string{groupName},
	})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](client, iamuser.ServiceName, userName, "Delete").Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	_, err = iamClient.GetUser(context.Background(), &iamsdk.GetUserInput{UserName: aws.String(userName)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "NoSuchEntity")
}

func TestIAMUserReconcile_DetectsGroupDrift(t *testing.T) {
	client, iamClient := setupIAMUserDriver(t)
	userName := uniqueIAMName(t, "user")
	groupA := uniqueIAMName(t, "group-a")
	groupB := uniqueIAMName(t, "group-b")
	createIAMGroup(t, iamClient, groupA)
	createIAMGroup(t, iamClient, groupB)

	_, err := ingress.Object[iamuser.IAMUserSpec, iamuser.IAMUserOutputs](client, iamuser.ServiceName, userName, "Provision").Request(t.Context(), iamuser.IAMUserSpec{
		Account:  integrationAccountName,
		UserName: userName,
		Groups:   []string{groupA},
	})
	require.NoError(t, err)

	_, err = iamClient.AddUserToGroup(context.Background(), &iamsdk.AddUserToGroupInput{UserName: aws.String(userName), GroupName: aws.String(groupB)})
	require.NoError(t, err)

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, iamuser.ServiceName, userName, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)

	groupOut, err := iamClient.ListGroupsForUser(context.Background(), &iamsdk.ListGroupsForUserInput{UserName: aws.String(userName)})
	require.NoError(t, err)
	require.Len(t, groupOut.Groups, 1)
	assert.Equal(t, groupA, aws.ToString(groupOut.Groups[0].GroupName))
}
