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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	restatetest "github.com/restatedev/sdk-go/testing"

	"github.com/shirvan/praxis/internal/drivers/ebs"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

func uniqueVolumeName(t *testing.T) string {
	t.Helper()
	name := strings.ReplaceAll(t.Name(), "/", "-")
	name = strings.ReplaceAll(name, "_", "-")
	if len(name) > 50 {
		name = name[:50]
	}
	return fmt.Sprintf("%s-%d", name, time.Now().UnixNano()%1000000000)
}

func setupEBSDriver(t *testing.T) (*ingress.Client, *ec2sdk.Client) {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := localstackAWSConfig(t)
	ec2Client := awsclient.NewEC2Client(awsCfg)
	driver := ebs.NewEBSVolumeDriver(nil)

	env := restatetest.Start(t, restate.Reflect(driver))
	return env.Ingress(), ec2Client
}

func defaultSubnetAndAZ(t *testing.T, ec2Client *ec2sdk.Client) (string, string) {
	t.Helper()
	out, err := ec2Client.DescribeSubnets(context.Background(), &ec2sdk.DescribeSubnetsInput{
		Filters: []ec2types.Filter{{Name: aws.String("default-for-az"), Values: []string{"true"}}},
	})
	require.NoError(t, err)
	require.NotEmpty(t, out.Subnets, "LocalStack should have a default subnet")
	return aws.ToString(out.Subnets[0].SubnetId), aws.ToString(out.Subnets[0].AvailabilityZone)
}

func TestEBSProvision_CreatesRealVolume(t *testing.T) {
	client, ec2Client := setupEBSDriver(t)
	name := uniqueVolumeName(t)
	_, az := defaultSubnetAndAZ(t, ec2Client)
	key := fmt.Sprintf("us-east-1~%s", name)

	outputs, err := ingress.Object[ebs.EBSVolumeSpec, ebs.EBSVolumeOutputs](
		client, "EBSVolume", key, "Provision",
	).Request(t.Context(), ebs.EBSVolumeSpec{
		Account:          integrationAccountName,
		Region:           "us-east-1",
		AvailabilityZone: az,
		VolumeType:       "gp3",
		SizeGiB:          20,
		Encrypted:        true,
		ManagedKey:       key,
		Tags:             map[string]string{"Name": name, "env": "test"},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, outputs.VolumeId)
	assert.Equal(t, az, outputs.AvailabilityZone)

	desc, err := ec2Client.DescribeVolumes(context.Background(), &ec2sdk.DescribeVolumesInput{VolumeIds: []string{outputs.VolumeId}})
	require.NoError(t, err)
	require.Len(t, desc.Volumes, 1)
	assert.Equal(t, outputs.VolumeId, aws.ToString(desc.Volumes[0].VolumeId))
}

func TestEBSProvision_Idempotent(t *testing.T) {
	client, ec2Client := setupEBSDriver(t)
	name := uniqueVolumeName(t)
	_, az := defaultSubnetAndAZ(t, ec2Client)
	key := fmt.Sprintf("us-east-1~%s", name)

	spec := ebs.EBSVolumeSpec{
		Account:          integrationAccountName,
		Region:           "us-east-1",
		AvailabilityZone: az,
		VolumeType:       "gp3",
		SizeGiB:          20,
		Encrypted:        true,
		ManagedKey:       key,
		Tags:             map[string]string{"Name": name},
	}

	out1, err := ingress.Object[ebs.EBSVolumeSpec, ebs.EBSVolumeOutputs](client, "EBSVolume", key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	out2, err := ingress.Object[ebs.EBSVolumeSpec, ebs.EBSVolumeOutputs](client, "EBSVolume", key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	assert.Equal(t, out1.VolumeId, out2.VolumeId)
}

func TestEBSImport_ExistingVolume(t *testing.T) {
	client, ec2Client := setupEBSDriver(t)
	name := uniqueVolumeName(t)
	_, az := defaultSubnetAndAZ(t, ec2Client)

	createOut, err := ec2Client.CreateVolume(context.Background(), &ec2sdk.CreateVolumeInput{
		AvailabilityZone: aws.String(az),
		VolumeType:       ec2types.VolumeTypeGp3,
		Size:             aws.Int32(20),
		TagSpecifications: []ec2types.TagSpecification{{
			ResourceType: ec2types.ResourceTypeVolume,
			Tags:         []ec2types.Tag{{Key: aws.String("Name"), Value: aws.String(name)}},
		}},
	})
	require.NoError(t, err)
	volumeID := aws.ToString(createOut.VolumeId)

	key := fmt.Sprintf("us-east-1~%s", volumeID)
	outputs, err := ingress.Object[types.ImportRef, ebs.EBSVolumeOutputs](client, "EBSVolume", key, "Import").Request(t.Context(), types.ImportRef{
		ResourceID: volumeID,
		Account:    integrationAccountName,
	})
	require.NoError(t, err)
	assert.Equal(t, volumeID, outputs.VolumeId)

	status, err := ingress.Object[restate.Void, types.StatusResponse](client, "EBSVolume", key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, status.Mode)
}

func TestEBSDelete_RemovesVolume(t *testing.T) {
	client, ec2Client := setupEBSDriver(t)
	name := uniqueVolumeName(t)
	_, az := defaultSubnetAndAZ(t, ec2Client)
	key := fmt.Sprintf("us-east-1~%s", name)

	out, err := ingress.Object[ebs.EBSVolumeSpec, ebs.EBSVolumeOutputs](client, "EBSVolume", key, "Provision").Request(t.Context(), ebs.EBSVolumeSpec{
		Account:          integrationAccountName,
		Region:           "us-east-1",
		AvailabilityZone: az,
		VolumeType:       "gp3",
		SizeGiB:          20,
		Encrypted:        true,
		ManagedKey:       key,
		Tags:             map[string]string{"Name": name},
	})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](client, "EBSVolume", key, "Delete").Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	_, err = ec2Client.DescribeVolumes(context.Background(), &ec2sdk.DescribeVolumesInput{VolumeIds: []string{out.VolumeId}})
	require.Error(t, err, "volume should be deleted from LocalStack")
}

func TestEBSDelete_AttachedVolumeFails(t *testing.T) {
	client, ec2Client := setupEBSDriver(t)
	name := uniqueVolumeName(t)
	subnetID, az := defaultSubnetAndAZ(t, ec2Client)
	key := fmt.Sprintf("us-east-1~%s", name)

	vol, err := ingress.Object[ebs.EBSVolumeSpec, ebs.EBSVolumeOutputs](client, "EBSVolume", key, "Provision").Request(t.Context(), ebs.EBSVolumeSpec{
		Account:          integrationAccountName,
		Region:           "us-east-1",
		AvailabilityZone: az,
		VolumeType:       "gp3",
		SizeGiB:          20,
		Encrypted:        true,
		ManagedKey:       key,
		Tags:             map[string]string{"Name": name},
	})
	require.NoError(t, err)

	runOut, err := ec2Client.RunInstances(context.Background(), &ec2sdk.RunInstancesInput{
		ImageId:      aws.String("ami-0123456789abcdef0"),
		InstanceType: ec2types.InstanceTypeT3Micro,
		MinCount:     aws.Int32(1),
		MaxCount:     aws.Int32(1),
		SubnetId:     aws.String(subnetID),
	})
	require.NoError(t, err)
	require.NotEmpty(t, runOut.Instances)
	instanceID := aws.ToString(runOut.Instances[0].InstanceId)

	_, err = ec2Client.AttachVolume(context.Background(), &ec2sdk.AttachVolumeInput{
		Device:     aws.String("/dev/sdf"),
		InstanceId: aws.String(instanceID),
		VolumeId:   aws.String(vol.VolumeId),
	})
	if err != nil {
		t.Skipf("LocalStack volume attachment not supported here: %v", err)
	}

	_, err = ingress.Object[restate.Void, restate.Void](client, "EBSVolume", key, "Delete").Request(t.Context(), restate.Void{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "detach it before deleting")
}

func TestEBSReconcile_DetectsAndFixesDrift(t *testing.T) {
	client, ec2Client := setupEBSDriver(t)
	name := uniqueVolumeName(t)
	_, az := defaultSubnetAndAZ(t, ec2Client)
	key := fmt.Sprintf("us-east-1~%s", name)

	out, err := ingress.Object[ebs.EBSVolumeSpec, ebs.EBSVolumeOutputs](client, "EBSVolume", key, "Provision").Request(t.Context(), ebs.EBSVolumeSpec{
		Account:          integrationAccountName,
		Region:           "us-east-1",
		AvailabilityZone: az,
		VolumeType:       "gp3",
		SizeGiB:          20,
		Encrypted:        true,
		ManagedKey:       key,
		Tags:             map[string]string{"Name": name, "env": "managed"},
	})
	require.NoError(t, err)

	_, err = ec2Client.DeleteTags(context.Background(), &ec2sdk.DeleteTagsInput{
		Resources: []string{out.VolumeId},
		Tags:      []ec2types.Tag{{Key: aws.String("env")}},
	})
	require.NoError(t, err)

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, "EBSVolume", key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)

	desc, err := ec2Client.DescribeVolumes(context.Background(), &ec2sdk.DescribeVolumesInput{VolumeIds: []string{out.VolumeId}})
	require.NoError(t, err)
	require.Len(t, desc.Volumes, 1)
	assert.Contains(t, desc.Volumes[0].Tags, ec2types.Tag{Key: aws.String("env"), Value: aws.String("managed")})
}

func TestEBSGetStatus_ReturnsReady(t *testing.T) {
	client, ec2Client := setupEBSDriver(t)
	name := uniqueVolumeName(t)
	_, az := defaultSubnetAndAZ(t, ec2Client)
	key := fmt.Sprintf("us-east-1~%s", name)

	_, err := ingress.Object[ebs.EBSVolumeSpec, ebs.EBSVolumeOutputs](client, "EBSVolume", key, "Provision").Request(t.Context(), ebs.EBSVolumeSpec{
		Account:          integrationAccountName,
		Region:           "us-east-1",
		AvailabilityZone: az,
		VolumeType:       "gp3",
		SizeGiB:          20,
		Encrypted:        true,
		ManagedKey:       key,
		Tags:             map[string]string{"Name": name},
	})
	require.NoError(t, err)

	status, err := ingress.Object[restate.Void, types.StatusResponse](client, "EBSVolume", key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeManaged, status.Mode)
	assert.Greater(t, status.Generation, int64(0))
}
