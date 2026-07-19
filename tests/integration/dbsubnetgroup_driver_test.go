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
	rdssdk "github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	"github.com/shirvan/praxis/internal/core/authservice"

	"github.com/shirvan/praxis/internal/drivers/dbsubnetgroup"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

func uniqueSubnetGroupName(t *testing.T) string {
	t.Helper()
	name := strings.ReplaceAll(t.Name(), "/", "-")
	name = strings.ReplaceAll(name, "_", "-")
	name = strings.ToLower(name)
	if len(name) > 50 {
		name = name[:50]
	}
	return fmt.Sprintf("%s-%d", name, time.Now().UnixNano()%100000)
}

func setupDBSubnetGroupDriver(t *testing.T) (*ingress.Client, *rdssdk.Client, *ec2sdk.Client) {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := motoAWSConfig(t)
	rdsClient := awsclient.NewRDSClient(awsCfg)
	ec2Client := awsclient.NewEC2Client(awsCfg)
	driver := dbsubnetgroup.NewGenericDBSubnetGroupDriver(authservice.NewAuthClient())

	ingressClient := setupDriverEventingEnv(t, driver)
	return ingressClient, rdsClient, ec2Client
}

func getDefaultSubnetIds(t *testing.T, ec2Client *ec2sdk.Client) []string {
	t.Helper()
	out, err := ec2Client.DescribeSubnets(context.Background(), &ec2sdk.DescribeSubnetsInput{
		Filters: []ec2types.Filter{{Name: aws.String("default-for-az"), Values: []string{"true"}}},
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(out.Subnets), 2, "Moto should have at least 2 default subnets")

	ids := make([]string, 0, len(out.Subnets))
	for i := range out.Subnets {
		ids = append(ids, aws.ToString(out.Subnets[i].SubnetId))
	}
	return ids
}

func TestDBSubnetGroupProvision_Creates(t *testing.T) {
	client, rdsClient, ec2Client := setupDBSubnetGroupDriver(t)
	name := uniqueSubnetGroupName(t)
	subnetIds := getDefaultSubnetIds(t, ec2Client)
	key := fmt.Sprintf("us-east-1~%s", name)

	outputs, err := ingress.Object[types.ProvisionRequest, dbsubnetgroup.DBSubnetGroupOutputs](
		client, "DBSubnetGroup", key, "Provision",
	).Request(t.Context(), provisionRequest(t, dbsubnetgroup.DBSubnetGroupSpec{
		Account:     integrationAccountName,
		Region:      "us-east-1",
		GroupName:   name,
		Description: "Integration test subnet group",
		SubnetIds:   subnetIds[:2],
		Tags:        map[string]string{"env": "test"},
	}))
	require.NoError(t, err)
	assert.Equal(t, name, outputs.GroupName)
	assert.NotEmpty(t, outputs.ARN)

	desc, err := rdsClient.DescribeDBSubnetGroups(context.Background(), &rdssdk.DescribeDBSubnetGroupsInput{
		DBSubnetGroupName: aws.String(name),
	})
	require.NoError(t, err)
	require.Len(t, desc.DBSubnetGroups, 1)
	assert.Equal(t, name, aws.ToString(desc.DBSubnetGroups[0].DBSubnetGroupName))
}

func TestDBSubnetGroupProvision_Idempotent(t *testing.T) {
	client, _, ec2Client := setupDBSubnetGroupDriver(t)
	name := uniqueSubnetGroupName(t)
	subnetIds := getDefaultSubnetIds(t, ec2Client)
	key := fmt.Sprintf("us-east-1~%s", name)

	spec := dbsubnetgroup.DBSubnetGroupSpec{
		Account:     integrationAccountName,
		Region:      "us-east-1",
		GroupName:   name,
		Description: "Integration test subnet group",
		SubnetIds:   subnetIds[:2],
		Tags:        map[string]string{"env": "test"},
	}

	out1, err := ingress.Object[types.ProvisionRequest, dbsubnetgroup.DBSubnetGroupOutputs](
		client, "DBSubnetGroup", key, "Provision",
	).Request(t.Context(), provisionRequest(t, spec))
	require.NoError(t, err)

	out2, err := ingress.Object[types.ProvisionRequest, dbsubnetgroup.DBSubnetGroupOutputs](
		client, "DBSubnetGroup", key, "Provision",
	).Request(t.Context(), provisionRequest(t, spec))
	require.NoError(t, err)
	assert.Equal(t, out1.ARN, out2.ARN, "re-provision should return same ARN")
}

func TestDBSubnetGroupImport_ExistingGroup(t *testing.T) {
	client, rdsClient, ec2Client := setupDBSubnetGroupDriver(t)
	name := uniqueSubnetGroupName(t)
	subnetIds := getDefaultSubnetIds(t, ec2Client)

	_, err := rdsClient.CreateDBSubnetGroup(context.Background(), &rdssdk.CreateDBSubnetGroupInput{
		DBSubnetGroupName:        aws.String(name),
		DBSubnetGroupDescription: aws.String("Pre-existing group"),
		SubnetIds:                subnetIds[:2],
	})
	require.NoError(t, err)

	key := fmt.Sprintf("us-east-1~%s", name)
	outputs, err := ingress.Object[types.ImportRef, dbsubnetgroup.DBSubnetGroupOutputs](
		client, "DBSubnetGroup", key, "Import",
	).Request(t.Context(), types.ImportRef{
		ResourceID: name,
		Account:    integrationAccountName,
	})
	require.NoError(t, err)
	assert.Equal(t, name, outputs.GroupName)

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, "DBSubnetGroup", key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, status.Mode)
}

func TestDBSubnetGroupDelete_Removes(t *testing.T) {
	client, rdsClient, ec2Client := setupDBSubnetGroupDriver(t)
	name := uniqueSubnetGroupName(t)
	subnetIds := getDefaultSubnetIds(t, ec2Client)
	key := fmt.Sprintf("us-east-1~%s", name)

	_, err := ingress.Object[types.ProvisionRequest, dbsubnetgroup.DBSubnetGroupOutputs](
		client, "DBSubnetGroup", key, "Provision",
	).Request(t.Context(), provisionRequest(t, dbsubnetgroup.DBSubnetGroupSpec{
		Account:     integrationAccountName,
		Region:      "us-east-1",
		GroupName:   name,
		Description: "To be deleted",
		SubnetIds:   subnetIds[:2],
		Tags:        map[string]string{"env": "test"},
	}))
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](
		client, "DBSubnetGroup", key, "Delete",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	_, err = rdsClient.DescribeDBSubnetGroups(context.Background(), &rdssdk.DescribeDBSubnetGroupsInput{
		DBSubnetGroupName: aws.String(name),
	})
	require.Error(t, err, "subnet group should be gone")
}

// Note: description drift cannot be exercised against Moto — its
// ModifyDBSubnetGroup handler accepts the change but the group is no longer
// returned by DescribeDBSubnetGroups afterwards (the resource effectively
// vanishes). Tags are the other mutable field HasDrift compares, and Moto
// persists AddTagsToResource, so tag drift is used here.
func TestDBSubnetGroupReconcile_DetectsTagDrift(t *testing.T) {
	client, rdsClient, ec2Client := setupDBSubnetGroupDriver(t)
	name := uniqueSubnetGroupName(t)
	subnetIds := getDefaultSubnetIds(t, ec2Client)
	key := fmt.Sprintf("us-east-1~%s", name)

	outputs, err := ingress.Object[types.ProvisionRequest, dbsubnetgroup.DBSubnetGroupOutputs](
		client, "DBSubnetGroup", key, "Provision",
	).Request(t.Context(), provisionRequest(t, dbsubnetgroup.DBSubnetGroupSpec{
		Account:     integrationAccountName,
		Region:      "us-east-1",
		GroupName:   name,
		Description: "Integration test subnet group",
		SubnetIds:   subnetIds[:2],
		Tags:        map[string]string{"env": "test"},
	}))
	require.NoError(t, err)
	require.NotEmpty(t, outputs.ARN)

	// Externally overwrite a tag value to introduce drift.
	_, err = rdsClient.AddTagsToResource(context.Background(), &rdssdk.AddTagsToResourceInput{
		ResourceName: aws.String(outputs.ARN),
		Tags:         []rdstypes.Tag{{Key: aws.String("env"), Value: aws.String("hacked")}},
	})
	require.NoError(t, err)

	// Verify the external mutation landed before reconciling; otherwise there
	// is no observable drift and the scenario can only run against real AWS.
	tagsOut, err := rdsClient.ListTagsForResource(context.Background(), &rdssdk.ListTagsForResourceInput{
		ResourceName: aws.String(outputs.ARN),
	})
	require.NoError(t, err)
	if tagValue(tagsOut.TagList, "env") != "hacked" {
		t.Skip("Moto does not apply AddTagsToResource to DB subnet groups")
	}

	result, err := ingress.Object[restate.Void, types.ReconcileResult](
		client, "DBSubnetGroup", key, "Reconcile",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift, "drift should be detected")
	assert.True(t, result.Correcting, "managed mode should correct drift")

	tagsOut, err = rdsClient.ListTagsForResource(context.Background(), &rdssdk.ListTagsForResourceInput{
		ResourceName: aws.String(outputs.ARN),
	})
	require.NoError(t, err)
	assert.Equal(t, "test", tagValue(tagsOut.TagList, "env"), "tag should be restored to desired value")
}

func TestDBSubnetGroupGetStatus_ReturnsReady(t *testing.T) {
	client, _, ec2Client := setupDBSubnetGroupDriver(t)
	name := uniqueSubnetGroupName(t)
	subnetIds := getDefaultSubnetIds(t, ec2Client)
	key := fmt.Sprintf("us-east-1~%s", name)

	_, err := ingress.Object[types.ProvisionRequest, dbsubnetgroup.DBSubnetGroupOutputs](
		client, "DBSubnetGroup", key, "Provision",
	).Request(t.Context(), provisionRequest(t, dbsubnetgroup.DBSubnetGroupSpec{
		Account:     integrationAccountName,
		Region:      "us-east-1",
		GroupName:   name,
		Description: "Status check",
		SubnetIds:   subnetIds[:2],
		Tags:        map[string]string{"env": "test"},
	}))
	require.NoError(t, err)

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, "DBSubnetGroup", key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeManaged, status.Mode)
	assert.Greater(t, status.Generation, int64(0))
}
