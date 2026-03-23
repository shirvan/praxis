//go:build integration

package integration

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	iamsdk "github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	restatetest "github.com/restatedev/sdk-go/testing"

	"github.com/shirvan/praxis/internal/drivers/iaminstanceprofile"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

const assumeRolePolicyDoc = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`

func uniqueIAMName(t *testing.T, prefix string) string {
	t.Helper()
	name := strings.ReplaceAll(t.Name(), "/", "-")
	name = strings.ReplaceAll(name, "_", "-")
	if len(name) > 60 {
		name = name[:60]
	}
	return fmt.Sprintf("%s-%s-%d", prefix, name, time.Now().UnixNano()%100000)
}

func setupIAMInstanceProfileIntegration(t *testing.T) (*ingress.Client, *iamsdk.Client) {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := localstackAWSConfig(t)
	iamClient := awsclient.NewIAMClient(awsCfg)
	driver := iaminstanceprofile.NewIAMInstanceProfileDriver(nil)

	env := restatetest.Start(t, restate.Reflect(driver))
	return env.Ingress(), iamClient
}

func createIAMRole(t *testing.T, iamClient *iamsdk.Client, roleName string) {
	t.Helper()
	_, err := iamClient.CreateRole(context.Background(), &iamsdk.CreateRoleInput{
		RoleName:                 aws.String(roleName),
		AssumeRolePolicyDocument: aws.String(assumeRolePolicyDoc),
	})
	if err != nil && strings.Contains(err.Error(), "Service 'iam' is not enabled") {
		t.Skip("LocalStack IAM service is not enabled; restart the local stack after the compose update")
	}
	require.NoError(t, err)
}

func TestIAMInstanceProfileProvision_Creates(t *testing.T) {
	client, iamClient := setupIAMInstanceProfileIntegration(t)
	profileName := uniqueIAMName(t, "profile")
	roleName := uniqueIAMName(t, "role")
	createIAMRole(t, iamClient, roleName)

	outputs, err := ingress.Object[iaminstanceprofile.IAMInstanceProfileSpec, iaminstanceprofile.IAMInstanceProfileOutputs](client, iaminstanceprofile.ServiceName, profileName, "Provision").Request(t.Context(), iaminstanceprofile.IAMInstanceProfileSpec{
		Account:             integrationAccountName,
		Path:                "/",
		InstanceProfileName: profileName,
		RoleName:            roleName,
		Tags:                map[string]string{"Name": profileName, "env": "test"},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, outputs.InstanceProfileId)
	assert.Equal(t, profileName, outputs.InstanceProfileName)

	desc, err := iamClient.GetInstanceProfile(context.Background(), &iamsdk.GetInstanceProfileInput{InstanceProfileName: aws.String(profileName)})
	require.NoError(t, err)
	require.NotNil(t, desc.InstanceProfile)
	require.Len(t, desc.InstanceProfile.Roles, 1)
	assert.Equal(t, roleName, aws.ToString(desc.InstanceProfile.Roles[0].RoleName))
}

func TestIAMInstanceProfileProvision_ChangeRole(t *testing.T) {
	client, iamClient := setupIAMInstanceProfileIntegration(t)
	profileName := uniqueIAMName(t, "profile")
	roleA := uniqueIAMName(t, "role-a")
	roleB := uniqueIAMName(t, "role-b")
	createIAMRole(t, iamClient, roleA)
	createIAMRole(t, iamClient, roleB)

	spec := iaminstanceprofile.IAMInstanceProfileSpec{
		Account:             integrationAccountName,
		Path:                "/",
		InstanceProfileName: profileName,
		RoleName:            roleA,
		Tags:                map[string]string{"Name": profileName},
	}
	_, err := ingress.Object[iaminstanceprofile.IAMInstanceProfileSpec, iaminstanceprofile.IAMInstanceProfileOutputs](client, iaminstanceprofile.ServiceName, profileName, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)

	spec.RoleName = roleB
	_, err = ingress.Object[iaminstanceprofile.IAMInstanceProfileSpec, iaminstanceprofile.IAMInstanceProfileOutputs](client, iaminstanceprofile.ServiceName, profileName, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)

	desc, err := iamClient.GetInstanceProfile(context.Background(), &iamsdk.GetInstanceProfileInput{InstanceProfileName: aws.String(profileName)})
	require.NoError(t, err)
	require.Len(t, desc.InstanceProfile.Roles, 1)
	assert.Equal(t, roleB, aws.ToString(desc.InstanceProfile.Roles[0].RoleName))
}

func TestIAMInstanceProfileImport_DefaultsToObserved(t *testing.T) {
	client, iamClient := setupIAMInstanceProfileIntegration(t)
	profileName := uniqueIAMName(t, "profile")
	roleName := uniqueIAMName(t, "role")
	createIAMRole(t, iamClient, roleName)

	_, err := iamClient.CreateInstanceProfile(context.Background(), &iamsdk.CreateInstanceProfileInput{InstanceProfileName: aws.String(profileName)})
	require.NoError(t, err)
	_, err = iamClient.AddRoleToInstanceProfile(context.Background(), &iamsdk.AddRoleToInstanceProfileInput{InstanceProfileName: aws.String(profileName), RoleName: aws.String(roleName)})
	require.NoError(t, err)

	outputs, err := ingress.Object[types.ImportRef, iaminstanceprofile.IAMInstanceProfileOutputs](client, iaminstanceprofile.ServiceName, profileName, "Import").Request(t.Context(), types.ImportRef{ResourceID: profileName, Account: integrationAccountName})
	require.NoError(t, err)
	assert.Equal(t, profileName, outputs.InstanceProfileName)

	status, err := ingress.Object[restate.Void, types.StatusResponse](client, iaminstanceprofile.ServiceName, profileName, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, status.Mode)
}

func TestIAMInstanceProfileDelete_RemovesProfile(t *testing.T) {
	client, iamClient := setupIAMInstanceProfileIntegration(t)
	profileName := uniqueIAMName(t, "profile")
	roleName := uniqueIAMName(t, "role")
	createIAMRole(t, iamClient, roleName)

	_, err := ingress.Object[iaminstanceprofile.IAMInstanceProfileSpec, iaminstanceprofile.IAMInstanceProfileOutputs](client, iaminstanceprofile.ServiceName, profileName, "Provision").Request(t.Context(), iaminstanceprofile.IAMInstanceProfileSpec{
		Account:             integrationAccountName,
		Path:                "/",
		InstanceProfileName: profileName,
		RoleName:            roleName,
		Tags:                map[string]string{"Name": profileName},
	})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](client, iaminstanceprofile.ServiceName, profileName, "Delete").Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	_, err = iamClient.GetInstanceProfile(context.Background(), &iamsdk.GetInstanceProfileInput{InstanceProfileName: aws.String(profileName)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "NoSuchEntity")
}

func TestIAMInstanceProfileReconcile_DetectsRoleDrift(t *testing.T) {
	client, iamClient := setupIAMInstanceProfileIntegration(t)
	profileName := uniqueIAMName(t, "profile")
	roleName := uniqueIAMName(t, "role")
	createIAMRole(t, iamClient, roleName)

	_, err := ingress.Object[iaminstanceprofile.IAMInstanceProfileSpec, iaminstanceprofile.IAMInstanceProfileOutputs](client, iaminstanceprofile.ServiceName, profileName, "Provision").Request(t.Context(), iaminstanceprofile.IAMInstanceProfileSpec{
		Account:             integrationAccountName,
		Path:                "/",
		InstanceProfileName: profileName,
		RoleName:            roleName,
		Tags:                map[string]string{"Name": profileName},
	})
	require.NoError(t, err)

	_, err = iamClient.RemoveRoleFromInstanceProfile(context.Background(), &iamsdk.RemoveRoleFromInstanceProfileInput{InstanceProfileName: aws.String(profileName), RoleName: aws.String(roleName)})
	require.NoError(t, err)

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, iaminstanceprofile.ServiceName, profileName, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)

	desc, err := iamClient.GetInstanceProfile(context.Background(), &iamsdk.GetInstanceProfileInput{InstanceProfileName: aws.String(profileName)})
	require.NoError(t, err)
	require.Len(t, desc.InstanceProfile.Roles, 1)
	assert.Equal(t, roleName, aws.ToString(desc.InstanceProfile.Roles[0].RoleName))
}
