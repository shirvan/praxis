//go:build integration

package integration

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	iamsdk "github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/core/orchestrator"
	"github.com/shirvan/praxis/internal/drivers/ec2"
	"github.com/shirvan/praxis/internal/eventing"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

func uniqueInstanceName(t *testing.T) string {
	t.Helper()
	name := strings.ReplaceAll(t.Name(), "/", "-")
	name = strings.ReplaceAll(name, "_", "-")
	if len(name) > 50 {
		name = name[:50]
	}
	return fmt.Sprintf("%s-%d", name, time.Now().UnixNano()%100000)
}

func setupEC2Driver(t *testing.T) (*ingress.Client, *ec2sdk.Client) {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := motoAWSConfig(t)
	ec2Client := awsclient.NewEC2Client(awsCfg)
	driver := ec2.NewGenericEC2InstanceDriver(authservice.NewAuthClient())

	return setupDriverEventingEnvWithCoreRecovery(t, driver), ec2Client
}

func getDefaultSubnetId(t *testing.T, ec2Client *ec2sdk.Client) string {
	t.Helper()
	out, err := ec2Client.DescribeSubnets(context.Background(), &ec2sdk.DescribeSubnetsInput{
		Filters: []ec2types.Filter{{Name: aws.String("default-for-az"), Values: []string{"true"}}},
	})
	require.NoError(t, err)
	require.NotEmpty(t, out.Subnets, "Moto should have a default subnet")
	return aws.ToString(out.Subnets[0].SubnetId)
}

func TestEC2Provision_CreatesInstance(t *testing.T) {
	client, ec2Client := setupEC2Driver(t)
	name := uniqueInstanceName(t)
	subnetId := getDefaultSubnetId(t, ec2Client)
	key := fmt.Sprintf("us-east-1~%s", name)

	outputs, err := ingress.Object[types.ProvisionRequest, ec2.EC2InstanceOutputs](
		client, "EC2Instance", key, "Provision",
	).Request(t.Context(), provisionRequest(t, ec2.EC2InstanceSpec{
		Account:      integrationAccountName,
		Region:       "us-east-1",
		ImageId:      "ami-0123456789abcdef0",
		InstanceType: "t3.micro",
		SubnetId:     subnetId,
		Monitoring:   false,
		ManagedKey:   key,
		Tags:         map[string]string{"Name": name, "env": "test"},
	}))
	require.NoError(t, err)
	assert.NotEmpty(t, outputs.InstanceId)
	assert.Equal(t, subnetId, outputs.SubnetId)

	desc, err := ec2Client.DescribeInstances(context.Background(), &ec2sdk.DescribeInstancesInput{
		InstanceIds: []string{outputs.InstanceId},
	})
	require.NoError(t, err)
	require.NotEmpty(t, desc.Reservations)
	require.NotEmpty(t, desc.Reservations[0].Instances)
	assert.Equal(t, outputs.InstanceId, aws.ToString(desc.Reservations[0].Instances[0].InstanceId))
}

func TestEC2Import_DefaultsToObserved(t *testing.T) {
	client, ec2Client := setupEC2Driver(t)
	subnetId := getDefaultSubnetId(t, ec2Client)

	runOut, err := ec2Client.RunInstances(context.Background(), &ec2sdk.RunInstancesInput{
		ImageId:      aws.String("ami-0123456789abcdef0"),
		InstanceType: ec2types.InstanceTypeT3Micro,
		MinCount:     aws.Int32(1),
		MaxCount:     aws.Int32(1),
		SubnetId:     aws.String(subnetId),
		TagSpecifications: []ec2types.TagSpecification{{
			ResourceType: ec2types.ResourceTypeInstance,
			Tags:         []ec2types.Tag{{Key: aws.String("Name"), Value: aws.String(uniqueInstanceName(t))}},
		}},
	})
	require.NoError(t, err)
	require.NotEmpty(t, runOut.Instances)
	instanceId := aws.ToString(runOut.Instances[0].InstanceId)

	key := fmt.Sprintf("us-east-1~%s", instanceId)
	outputs, err := ingress.Object[types.ImportRef, ec2.EC2InstanceOutputs](
		client, "EC2Instance", key, "Import",
	).Request(t.Context(), types.ImportRef{
		ResourceID: instanceId,
		Account:    integrationAccountName,
	})
	require.NoError(t, err)
	assert.Equal(t, instanceId, outputs.InstanceId)

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, "EC2Instance", key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, status.Mode)
}

func TestEC2Delete_ObservedModeBlocked(t *testing.T) {
	client, ec2Client := setupEC2Driver(t)
	subnetId := getDefaultSubnetId(t, ec2Client)

	runOut, err := ec2Client.RunInstances(context.Background(), &ec2sdk.RunInstancesInput{
		ImageId:      aws.String("ami-0123456789abcdef0"),
		InstanceType: ec2types.InstanceTypeT3Micro,
		MinCount:     aws.Int32(1),
		MaxCount:     aws.Int32(1),
		SubnetId:     aws.String(subnetId),
	})
	require.NoError(t, err)
	instanceId := aws.ToString(runOut.Instances[0].InstanceId)
	key := fmt.Sprintf("us-east-1~%s", instanceId)

	_, err = ingress.Object[types.ImportRef, ec2.EC2InstanceOutputs](
		client, "EC2Instance", key, "Import",
	).Request(t.Context(), types.ImportRef{ResourceID: instanceId, Account: integrationAccountName})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](
		client, "EC2Instance", key, "Delete",
	).Request(t.Context(), restate.Void{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Observed mode")
}

func TestEC2Provision_Idempotent(t *testing.T) {
	client, ec2Client := setupEC2Driver(t)
	name := uniqueInstanceName(t)
	subnetId := getDefaultSubnetId(t, ec2Client)
	key := fmt.Sprintf("us-east-1~%s", name)

	spec := ec2.EC2InstanceSpec{
		Account:      integrationAccountName,
		Region:       "us-east-1",
		ImageId:      "ami-0123456789abcdef0",
		InstanceType: "t3.micro",
		SubnetId:     subnetId,
		Monitoring:   false,
		ManagedKey:   key,
		Tags:         map[string]string{"Name": name},
	}

	out1, err := ingress.Object[types.ProvisionRequest, ec2.EC2InstanceOutputs](
		client, "EC2Instance", key, "Provision",
	).Request(t.Context(), provisionRequest(t, spec))
	require.NoError(t, err)
	assert.NotEmpty(t, out1.InstanceId)

	out2, err := ingress.Object[types.ProvisionRequest, ec2.EC2InstanceOutputs](
		client, "EC2Instance", key, "Provision",
	).Request(t.Context(), provisionRequest(t, spec))
	require.NoError(t, err)
	assert.Equal(t, out1.InstanceId, out2.InstanceId, "re-provision should reuse same instance")
}

func TestEC2Provision_ConvergesIAMInstanceProfileAttachment(t *testing.T) {
	client, ec2Client := setupEC2Driver(t)
	iamClient := awsclient.NewIAMClient(motoAWSConfig(t))
	subnetId := getDefaultSubnetId(t, ec2Client)
	name := uniqueInstanceName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	profileA := createEC2TestInstanceProfile(t, iamClient, "profile-a", "role-a")
	profileB := createEC2TestInstanceProfile(t, iamClient, "profile-b", "role-b")
	spec := ec2.EC2InstanceSpec{
		Account: integrationAccountName, Region: "us-east-1", ImageId: "ami-0123456789abcdef0",
		InstanceType: "t3.micro", SubnetId: subnetId, IamInstanceProfile: profileA,
		ManagedKey: key, Tags: map[string]string{"Name": name},
	}

	outputs, err := ingress.Object[types.ProvisionRequest, ec2.EC2InstanceOutputs](client, ec2.ServiceName, key, "Provision").Request(t.Context(), provisionRequest(t, spec))
	require.NoError(t, err)
	assertEC2InstanceProfile(t, ec2Client, outputs.InstanceId, profileA)

	spec.IamInstanceProfile = profileB
	_, err = ingress.Object[types.ProvisionRequest, ec2.EC2InstanceOutputs](client, ec2.ServiceName, key, "Provision").Request(t.Context(), provisionRequest(t, spec))
	require.NoError(t, err)
	assertEC2InstanceProfile(t, ec2Client, outputs.InstanceId, profileB)

	spec.IamInstanceProfile = ""
	_, err = ingress.Object[types.ProvisionRequest, ec2.EC2InstanceOutputs](client, ec2.ServiceName, key, "Provision").Request(t.Context(), provisionRequest(t, spec))
	require.NoError(t, err)
	assertEC2InstanceProfile(t, ec2Client, outputs.InstanceId, "")
}

func createEC2TestInstanceProfile(t *testing.T, iamClient *iamsdk.Client, profilePrefix, rolePrefix string) string {
	t.Helper()
	profileName := uniqueIAMName(t, profilePrefix)
	roleName := uniqueIAMName(t, rolePrefix)
	createIAMRole(t, iamClient, roleName)
	registerIAMInstanceProfileCleanup(t, iamClient, profileName)
	_, err := iamClient.CreateInstanceProfile(t.Context(), &iamsdk.CreateInstanceProfileInput{InstanceProfileName: aws.String(profileName)})
	require.NoError(t, err)
	_, err = iamClient.AddRoleToInstanceProfile(t.Context(), &iamsdk.AddRoleToInstanceProfileInput{
		InstanceProfileName: aws.String(profileName), RoleName: aws.String(roleName),
	})
	require.NoError(t, err)
	return profileName
}

func assertEC2InstanceProfile(t *testing.T, ec2Client *ec2sdk.Client, instanceID, expectedName string) {
	t.Helper()
	out, err := ec2Client.DescribeIamInstanceProfileAssociations(t.Context(), &ec2sdk.DescribeIamInstanceProfileAssociationsInput{
		Filters: []ec2types.Filter{{Name: aws.String("instance-id"), Values: []string{instanceID}}},
	})
	require.NoError(t, err)
	if expectedName == "" {
		assert.Empty(t, out.IamInstanceProfileAssociations)
		return
	}
	require.Len(t, out.IamInstanceProfileAssociations, 1)
	profile := out.IamInstanceProfileAssociations[0].IamInstanceProfile
	require.NotNil(t, profile)
	assert.True(t, strings.HasSuffix(aws.ToString(profile.Arn), "/"+expectedName))
}

func TestEC2Delete_TerminatesInstance(t *testing.T) {
	client, ec2Client := setupEC2Driver(t)
	name := uniqueInstanceName(t)
	subnetId := getDefaultSubnetId(t, ec2Client)
	key := fmt.Sprintf("us-east-1~%s", name)

	out, err := ingress.Object[types.ProvisionRequest, ec2.EC2InstanceOutputs](
		client, "EC2Instance", key, "Provision",
	).Request(t.Context(), provisionRequest(t, ec2.EC2InstanceSpec{
		Account:      integrationAccountName,
		Region:       "us-east-1",
		ImageId:      "ami-0123456789abcdef0",
		InstanceType: "t3.micro",
		SubnetId:     subnetId,
		ManagedKey:   key,
		Tags:         map[string]string{"Name": name},
	}))
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](
		client, "EC2Instance", key, "Delete",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	desc, err := ec2Client.DescribeInstances(context.Background(), &ec2sdk.DescribeInstancesInput{
		InstanceIds: []string{out.InstanceId},
	})
	require.NoError(t, err)
	require.NotEmpty(t, desc.Reservations)
	require.NotEmpty(t, desc.Reservations[0].Instances)
	state := string(desc.Reservations[0].Instances[0].State.Name)
	assert.Contains(t, []string{"terminated", "shutting-down"}, state)
}

func TestEC2GetStatus_ReturnsReady(t *testing.T) {
	client, ec2Client := setupEC2Driver(t)
	name := uniqueInstanceName(t)
	subnetId := getDefaultSubnetId(t, ec2Client)
	key := fmt.Sprintf("us-east-1~%s", name)

	_, err := ingress.Object[types.ProvisionRequest, ec2.EC2InstanceOutputs](
		client, "EC2Instance", key, "Provision",
	).Request(t.Context(), provisionRequest(t, ec2.EC2InstanceSpec{
		Account:      integrationAccountName,
		Region:       "us-east-1",
		ImageId:      "ami-0123456789abcdef0",
		InstanceType: "t3.micro",
		SubnetId:     subnetId,
		ManagedKey:   key,
		Tags:         map[string]string{"Name": name},
	}))
	require.NoError(t, err)

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, "EC2Instance", key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeManaged, status.Mode)
	assert.Greater(t, status.Generation, int64(0))
}

func TestEC2Reconcile_EmitsDriftEvents(t *testing.T) {
	client, ec2Client := setupEC2Driver(t)
	name := uniqueInstanceName(t)
	subnetId := getDefaultSubnetId(t, ec2Client)
	key := fmt.Sprintf("us-east-1~%s", name)
	streamKey := "dep-ec2-drift-" + name
	registerDriftEventOwner(t, client, key, streamKey, name, ec2.ServiceName)

	outputs, err := ingress.Object[types.ProvisionRequest, ec2.EC2InstanceOutputs](
		client, "EC2Instance", key, "Provision",
	).Request(t.Context(), provisionRequest(t, ec2.EC2InstanceSpec{
		Account:      integrationAccountName,
		Region:       "us-east-1",
		ImageId:      "ami-0123456789abcdef0",
		InstanceType: "t3.micro",
		SubnetId:     subnetId,
		Monitoring:   false,
		ManagedKey:   key,
		Tags:         map[string]string{"Name": name, "env": "managed"},
	}))
	require.NoError(t, err)

	_, err = ec2Client.DeleteTags(context.Background(), &ec2sdk.DeleteTagsInput{
		Resources: []string{outputs.InstanceId},
		Tags:      []ec2types.Tag{{Key: aws.String("env")}},
	})
	require.NoError(t, err)

	result, err := ingress.Object[restate.Void, types.ReconcileResult](
		client, "EC2Instance", key, "Reconcile",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)
	assert.Contains(t, pollDriftEventTypes(t, client, streamKey, orchestrator.EventTypeDriftDetected, orchestrator.EventTypeDriftCorrected), orchestrator.EventTypeDriftDetected)
	assert.Contains(t, pollDriftEventTypes(t, client, streamKey, orchestrator.EventTypeDriftDetected, orchestrator.EventTypeDriftCorrected), orchestrator.EventTypeDriftCorrected)

	desc, err := ec2Client.DescribeInstances(context.Background(), &ec2sdk.DescribeInstancesInput{InstanceIds: []string{outputs.InstanceId}})
	require.NoError(t, err)
	var tags []ec2types.Tag
	for _, reservation := range desc.Reservations {
		for _, instance := range reservation.Instances {
			if aws.ToString(instance.InstanceId) == outputs.InstanceId {
				tags = instance.Tags
			}
		}
	}
	assert.Contains(t, tags, ec2types.Tag{Key: aws.String("env"), Value: aws.String("managed")})
}

func TestEC2Reconcile_EmitsExternalDeleteEvent(t *testing.T) {
	client, ec2Client := setupEC2Driver(t)
	name := uniqueInstanceName(t)
	subnetId := getDefaultSubnetId(t, ec2Client)
	key := fmt.Sprintf("us-east-1~%s", name)
	streamKey := "dep-ec2-external-delete-" + name
	_, err := ingress.Object[orchestrator.DeploymentPlan, int64](
		client, orchestrator.DeploymentStateServiceName, streamKey, "InitDeployment",
	).Request(t.Context(), orchestrator.DeploymentPlan{
		Key: streamKey, Workspace: "integration", CreatedAt: time.Now().UTC(),
		Resources: []orchestrator.PlanResource{{
			Name: name, Kind: ec2.ServiceName, DriverService: ec2.ServiceName, Key: key,
			Lifecycle: &types.LifecyclePolicy{Recovery: &types.RecoveryPolicy{Mode: types.RecoveryModeManual}},
		}},
	})
	require.NoError(t, err)
	_, err = ingress.Object[orchestrator.StatusUpdate, restate.Void](
		client, orchestrator.DeploymentStateServiceName, streamKey, "SetStatus",
	).Request(t.Context(), orchestrator.StatusUpdate{Status: types.DeploymentComplete, UpdatedAt: time.Now().UTC()})
	require.NoError(t, err)

	outputs, err := ingress.Object[types.ProvisionRequest, ec2.EC2InstanceOutputs](
		client, "EC2Instance", key, "Provision",
	).Request(t.Context(), provisionRequest(t, ec2.EC2InstanceSpec{
		Account:      integrationAccountName,
		Region:       "us-east-1",
		ImageId:      "ami-0123456789abcdef0",
		InstanceType: "t3.micro",
		SubnetId:     subnetId,
		ManagedKey:   key,
		Tags:         map[string]string{"Name": name},
	}))
	require.NoError(t, err)

	_, err = ec2Client.TerminateInstances(context.Background(), &ec2sdk.TerminateInstancesInput{InstanceIds: []string{outputs.InstanceId}})
	require.NoError(t, err)

	result, err := ingress.Object[restate.Void, types.ReconcileResult](
		client, "EC2Instance", key, "Reconcile",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, "EC2Instance resource was deleted externally", result.Error)
	assert.Contains(t, pollDriftEventTypes(t, client, streamKey, orchestrator.EventTypeDriftExternalDelete), orchestrator.EventTypeDriftExternalDelete)
	recoveryState := pollEC2ManualRecoveryState(t, client, streamKey, name)
	assert.Equal(t, types.DeploymentComplete, recoveryState.Status)
	resourceState := recoveryState.Resources[name]
	require.NotNil(t, resourceState)
	assert.Equal(t, types.DeploymentResourceError, resourceState.Status)
	assert.Equal(t, result.Error, resourceState.Error)
	require.Len(t, resourceState.Conditions, 1)
	assert.Equal(t, types.ReasonReplacementRequired, resourceState.Conditions[0].Reason)
	assert.Equal(t, eventing.DriftEventExternalDelete, strings.TrimPrefix(orchestrator.EventTypeDriftExternalDelete, "dev.praxis.drift."))
}

func pollEC2ManualRecoveryState(t *testing.T, client *ingress.Client, streamKey, resourceName string) *orchestrator.DeploymentState {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		state, err := ingress.Object[restate.Void, *orchestrator.DeploymentState](
			client, orchestrator.DeploymentStateServiceName, streamKey, "GetState",
		).Request(t.Context(), restate.Void{})
		require.NoError(t, err)
		if state != nil {
			if resource := state.Resources[resourceName]; resource != nil && resource.Status == types.DeploymentResourceError {
				return state
			}
		}
		if time.Now().After(deadline) {
			require.FailNow(t, "timed out waiting for EC2 manual recovery state")
		}
		time.Sleep(100 * time.Millisecond)
	}
}
