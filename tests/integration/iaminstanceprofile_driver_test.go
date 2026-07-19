//go:build integration

package integration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	iamsdk "github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	"github.com/shirvan/praxis/internal/core/authservice"

	"github.com/shirvan/praxis/internal/drivers/iaminstanceprofile"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

const assumeRolePolicyDoc = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`

func uniqueIAMName(t *testing.T, prefix string) string {
	t.Helper()
	random := make([]byte, 6)
	_, err := rand.Read(random)
	require.NoError(t, err)
	suffix := hex.EncodeToString(random)

	name := strings.ReplaceAll(t.Name(), "/", "-")
	name = strings.ReplaceAll(name, "_", "-")
	maxNameLength := 64 - len(prefix) - len(suffix) - 2
	if len(name) > maxNameLength {
		name = name[:maxNameLength]
	}
	return fmt.Sprintf("%s-%s-%s", prefix, strings.Trim(name, "-"), suffix)
}

func setupIAMInstanceProfileIntegration(t *testing.T) (*ingress.Client, *iamsdk.Client) {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := motoAWSConfig(t)
	iamClient := awsclient.NewIAMClient(awsCfg)
	driver := iaminstanceprofile.NewGenericIAMInstanceProfileDriver(authservice.NewAuthClient())

	ingressClient := setupDriverEventingEnv(t, driver)
	return ingressClient, iamClient
}

func createIAMRole(t *testing.T, iamClient *iamsdk.Client, roleName string) {
	t.Helper()
	_, err := iamClient.CreateRole(context.Background(), &iamsdk.CreateRoleInput{
		RoleName:                 aws.String(roleName),
		AssumeRolePolicyDocument: aws.String(assumeRolePolicyDoc),
	})
	if err != nil && strings.Contains(err.Error(), "Service 'iam' is not enabled") {
		t.Skip("Moto IAM service is not enabled; restart the local stack after the compose update")
	}
	require.NoError(t, err)
	t.Cleanup(func() {
		_, cleanupErr := iamClient.DeleteRole(context.Background(), &iamsdk.DeleteRoleInput{RoleName: aws.String(roleName)})
		if cleanupErr != nil && !isIAMNoSuchEntity(cleanupErr) {
			t.Errorf("clean up IAM role %s: %v", roleName, cleanupErr)
		}
	})
}

func registerIAMInstanceProfileCleanup(t *testing.T, iamClient *iamsdk.Client, profileName string) {
	t.Helper()
	t.Cleanup(func() {
		described, err := iamClient.GetInstanceProfile(context.Background(), &iamsdk.GetInstanceProfileInput{
			InstanceProfileName: aws.String(profileName),
		})
		if isIAMNoSuchEntity(err) {
			return
		}
		if err != nil {
			t.Errorf("describe IAM instance profile %s for cleanup: %v", profileName, err)
			return
		}
		for _, role := range described.InstanceProfile.Roles {
			_, err = iamClient.RemoveRoleFromInstanceProfile(context.Background(), &iamsdk.RemoveRoleFromInstanceProfileInput{
				InstanceProfileName: aws.String(profileName),
				RoleName:            role.RoleName,
			})
			if err != nil && !isIAMNoSuchEntity(err) {
				t.Errorf("remove role %s from IAM instance profile %s during cleanup: %v", aws.ToString(role.RoleName), profileName, err)
				return
			}
		}
		_, err = iamClient.DeleteInstanceProfile(context.Background(), &iamsdk.DeleteInstanceProfileInput{
			InstanceProfileName: aws.String(profileName),
		})
		if err != nil && !isIAMNoSuchEntity(err) {
			t.Errorf("clean up IAM instance profile %s: %v", profileName, err)
		}
	})
}

func isIAMNoSuchEntity(err error) bool {
	return err != nil && strings.Contains(err.Error(), "NoSuchEntity")
}

func TestIAMInstanceProfileProvision_Creates(t *testing.T) {
	client, iamClient := setupIAMInstanceProfileIntegration(t)
	profileName := uniqueIAMName(t, "profile")
	roleName := uniqueIAMName(t, "role")
	createIAMRole(t, iamClient, roleName)
	registerIAMInstanceProfileCleanup(t, iamClient, profileName)

	outputs, err := ingress.Object[types.ProvisionRequest, iaminstanceprofile.IAMInstanceProfileOutputs](client, iaminstanceprofile.ServiceName, profileName, "Provision").Request(t.Context(), provisionRequest(t, iaminstanceprofile.IAMInstanceProfileSpec{
		Account:             integrationAccountName,
		Path:                "/",
		InstanceProfileName: profileName,
		RoleName:            roleName,
		Tags:                map[string]string{"Name": profileName, "env": "test"},
	}))
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
	registerIAMInstanceProfileCleanup(t, iamClient, profileName)

	spec := iaminstanceprofile.IAMInstanceProfileSpec{
		Account:             integrationAccountName,
		Path:                "/",
		InstanceProfileName: profileName,
		RoleName:            roleA,
		Tags:                map[string]string{"Name": profileName},
	}
	_, err := ingress.Object[types.ProvisionRequest, iaminstanceprofile.IAMInstanceProfileOutputs](client, iaminstanceprofile.ServiceName, profileName, "Provision").Request(t.Context(), provisionRequest(t, spec))
	require.NoError(t, err)

	spec.RoleName = roleB
	_, err = ingress.Object[types.ProvisionRequest, iaminstanceprofile.IAMInstanceProfileOutputs](client, iaminstanceprofile.ServiceName, profileName, "Provision").Request(t.Context(), provisionRequest(t, spec))
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
	registerIAMInstanceProfileCleanup(t, iamClient, profileName)

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
	registerIAMInstanceProfileCleanup(t, iamClient, profileName)

	_, err := ingress.Object[types.ProvisionRequest, iaminstanceprofile.IAMInstanceProfileOutputs](client, iaminstanceprofile.ServiceName, profileName, "Provision").Request(t.Context(), provisionRequest(t, iaminstanceprofile.IAMInstanceProfileSpec{
		Account:             integrationAccountName,
		Path:                "/",
		InstanceProfileName: profileName,
		RoleName:            roleName,
		Tags:                map[string]string{"Name": profileName},
	}))
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
	registerIAMInstanceProfileCleanup(t, iamClient, profileName)

	_, err := ingress.Object[types.ProvisionRequest, iaminstanceprofile.IAMInstanceProfileOutputs](client, iaminstanceprofile.ServiceName, profileName, "Provision").Request(t.Context(), provisionRequest(t, iaminstanceprofile.IAMInstanceProfileSpec{
		Account:             integrationAccountName,
		Path:                "/",
		InstanceProfileName: profileName,
		RoleName:            roleName,
		Tags:                map[string]string{"Name": profileName},
	}))
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
