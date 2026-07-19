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
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/core/orchestrator"
	"github.com/shirvan/praxis/internal/drivers/sg"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// uniqueSGName generates a unique security group name for each test.
func uniqueSGName(t *testing.T) string {
	t.Helper()
	random := make([]byte, 6)
	_, err := rand.Read(random)
	require.NoError(t, err)
	suffix := hex.EncodeToString(random)
	name := strings.ReplaceAll(t.Name(), "/", "-")
	name = strings.ReplaceAll(name, "_", "-")
	if len(name) > 50 {
		name = name[:50]
	}
	return fmt.Sprintf("%s-%s", strings.Trim(name, "-"), suffix)
}

// setupSGDriver starts a Restate test environment with the SG driver registered.
func setupSGDriver(t *testing.T) (*ingress.Client, *ec2sdk.Client) {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := motoAWSConfig(t)
	ec2Client := awsclient.NewEC2Client(awsCfg)
	driver := sg.NewGenericSecurityGroupDriver(authservice.NewAuthClient())

	return setupDriverEventingEnv(t, driver), ec2Client
}

func registerSGCleanup(t *testing.T, ec2Client *ec2sdk.Client, groupName, vpcID string) {
	t.Helper()
	t.Cleanup(func() {
		out, err := ec2Client.DescribeSecurityGroups(context.Background(), &ec2sdk.DescribeSecurityGroupsInput{
			Filters: []ec2types.Filter{
				{Name: aws.String("group-name"), Values: []string{groupName}},
				{Name: aws.String("vpc-id"), Values: []string{vpcID}},
			},
		})
		if err != nil {
			t.Errorf("describe security group %s for cleanup: %v", groupName, err)
			return
		}
		for i := range out.SecurityGroups {
			group := &out.SecurityGroups[i]
			_, err = ec2Client.DeleteSecurityGroup(context.Background(), &ec2sdk.DeleteSecurityGroupInput{GroupId: group.GroupId})
			if err != nil && !strings.Contains(err.Error(), "InvalidGroup.NotFound") {
				t.Errorf("delete security group %s during cleanup: %v", aws.ToString(group.GroupId), err)
			}
		}
	})
}

// getDefaultVpcId returns the default VPC ID from Moto.
func getDefaultVpcId(t *testing.T, ec2Client *ec2sdk.Client) string {
	t.Helper()
	out, err := ec2Client.DescribeVpcs(context.Background(), &ec2sdk.DescribeVpcsInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("isDefault"), Values: []string{"true"}},
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, out.Vpcs, "Moto should have a default VPC")
	return aws.ToString(out.Vpcs[0].VpcId)
}

func TestSGProvision_CreatesSecurityGroup(t *testing.T) {
	client, ec2Client := setupSGDriver(t)
	sgName := uniqueSGName(t)
	vpcId := getDefaultVpcId(t, ec2Client)
	registerSGCleanup(t, ec2Client, sgName, vpcId)
	key := fmt.Sprintf("%s~%s", vpcId, sgName)

	outputs, err := ingress.Object[sg.SecurityGroupSpec, sg.SecurityGroupOutputs](
		client, "SecurityGroup", key, "Provision",
	).Request(t.Context(), sg.SecurityGroupSpec{
		Account:     integrationAccountName,
		GroupName:   sgName,
		Description: "Test security group",
		VpcId:       vpcId,
		IngressRules: []sg.IngressRule{
			{Protocol: "tcp", FromPort: 80, ToPort: 80, CidrBlock: "0.0.0.0/0"},
		},
		EgressRules: []sg.EgressRule{
			{Protocol: "-1", FromPort: 0, ToPort: 0, CidrBlock: "0.0.0.0/0"},
		},
		Tags: map[string]string{"env": "test"},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, outputs.GroupId)
	assert.Equal(t, vpcId, outputs.VpcId)

	// Verify SG exists in Moto
	desc, err := ec2Client.DescribeSecurityGroups(context.Background(), &ec2sdk.DescribeSecurityGroupsInput{
		GroupIds: []string{outputs.GroupId},
	})
	require.NoError(t, err)
	require.Len(t, desc.SecurityGroups, 1)
	assert.Equal(t, sgName, aws.ToString(desc.SecurityGroups[0].GroupName))
	tags := make(map[string]string, len(desc.SecurityGroups[0].Tags))
	for _, tag := range desc.SecurityGroups[0].Tags {
		tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	assert.Equal(t, key, tags["praxis:managed-key"])
	assert.Equal(t, "test", tags["env"])
}

func TestSGProvision_RejectsUnownedSameNameCollision(t *testing.T) {
	client, ec2Client := setupSGDriver(t)
	sgName := uniqueSGName(t)
	vpcID := getDefaultVpcId(t, ec2Client)
	registerSGCleanup(t, ec2Client, sgName, vpcID)
	key := fmt.Sprintf("%s~%s", vpcID, sgName)

	created, err := ec2Client.CreateSecurityGroup(t.Context(), &ec2sdk.CreateSecurityGroupInput{
		GroupName: aws.String(sgName), Description: aws.String("external"), VpcId: aws.String(vpcID),
	})
	require.NoError(t, err)
	externalID := aws.ToString(created.GroupId)

	_, err = ingress.Object[sg.SecurityGroupSpec, sg.SecurityGroupOutputs](
		client, sg.ServiceName, key, "Provision",
	).Request(t.Context(), sg.SecurityGroupSpec{
		Account: integrationAccountName, GroupName: sgName, Description: "external", VpcId: vpcID,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "different ownership")

	desc, err := ec2Client.DescribeSecurityGroups(t.Context(), &ec2sdk.DescribeSecurityGroupsInput{GroupIds: []string{externalID}})
	require.NoError(t, err)
	require.Len(t, desc.SecurityGroups, 1)
	for _, tag := range desc.SecurityGroups[0].Tags {
		assert.NotEqual(t, "praxis:managed-key", aws.ToString(tag.Key), "external resource must not be adopted")
	}
}

func TestSGProvision_RejectsImmutableDescriptionChange(t *testing.T) {
	client, ec2Client := setupSGDriver(t)
	sgName := uniqueSGName(t)
	vpcID := getDefaultVpcId(t, ec2Client)
	registerSGCleanup(t, ec2Client, sgName, vpcID)
	key := fmt.Sprintf("%s~%s", vpcID, sgName)
	spec := sg.SecurityGroupSpec{
		Account: integrationAccountName, GroupName: sgName, Description: "original", VpcId: vpcID,
	}

	outputs, err := ingress.Object[sg.SecurityGroupSpec, sg.SecurityGroupOutputs](
		client, sg.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)
	spec.Description = "changed"
	_, err = ingress.Object[sg.SecurityGroupSpec, sg.SecurityGroupOutputs](
		client, sg.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "description is immutable")

	desc, err := ec2Client.DescribeSecurityGroups(t.Context(), &ec2sdk.DescribeSecurityGroupsInput{GroupIds: []string{outputs.GroupId}})
	require.NoError(t, err)
	require.Len(t, desc.SecurityGroups, 1)
	assert.Equal(t, "original", aws.ToString(desc.SecurityGroups[0].Description))
}

func TestSGProvision_RecoversExactManagedKey(t *testing.T) {
	client, ec2Client := setupSGDriver(t)
	sgName := uniqueSGName(t)
	vpcID := getDefaultVpcId(t, ec2Client)
	registerSGCleanup(t, ec2Client, sgName, vpcID)
	key := fmt.Sprintf("%s~%s", vpcID, sgName)

	created, err := ec2Client.CreateSecurityGroup(t.Context(), &ec2sdk.CreateSecurityGroupInput{
		GroupName: aws.String(sgName), Description: aws.String("recover"), VpcId: aws.String(vpcID),
		TagSpecifications: []ec2types.TagSpecification{{
			ResourceType: ec2types.ResourceTypeSecurityGroup,
			Tags: []ec2types.Tag{
				{Key: aws.String("praxis:managed-key"), Value: aws.String(key)},
				{Key: aws.String("env"), Value: aws.String("test")},
			},
		}},
	})
	require.NoError(t, err)

	outputs, err := ingress.Object[sg.SecurityGroupSpec, sg.SecurityGroupOutputs](
		client, sg.ServiceName, key, "Provision",
	).Request(t.Context(), sg.SecurityGroupSpec{
		Account: integrationAccountName, GroupName: sgName, Description: "recover", VpcId: vpcID,
		Tags: map[string]string{"env": "test"},
	})
	require.NoError(t, err)
	assert.Equal(t, aws.ToString(created.GroupId), outputs.GroupId)

	desc, err := ec2Client.DescribeSecurityGroups(t.Context(), &ec2sdk.DescribeSecurityGroupsInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("group-name"), Values: []string{sgName}},
			{Name: aws.String("vpc-id"), Values: []string{vpcID}},
		},
	})
	require.NoError(t, err)
	assert.Len(t, desc.SecurityGroups, 1, "recovery must not create a duplicate")
}

func TestSGProvision_TagConvergencePreservesManagedKey(t *testing.T) {
	client, ec2Client := setupSGDriver(t)
	sgName := uniqueSGName(t)
	vpcID := getDefaultVpcId(t, ec2Client)
	registerSGCleanup(t, ec2Client, sgName, vpcID)
	key := fmt.Sprintf("%s~%s", vpcID, sgName)
	spec := sg.SecurityGroupSpec{
		Account: integrationAccountName, GroupName: sgName, Description: "tags", VpcId: vpcID,
		Tags: map[string]string{"env": "before", "remove": "me"},
	}

	outputs, err := ingress.Object[sg.SecurityGroupSpec, sg.SecurityGroupOutputs](
		client, sg.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)
	spec.Tags = map[string]string{"env": "after"}
	_, err = ingress.Object[sg.SecurityGroupSpec, sg.SecurityGroupOutputs](
		client, sg.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)

	desc, err := ec2Client.DescribeSecurityGroups(t.Context(), &ec2sdk.DescribeSecurityGroupsInput{GroupIds: []string{outputs.GroupId}})
	require.NoError(t, err)
	require.Len(t, desc.SecurityGroups, 1)
	tags := make(map[string]string, len(desc.SecurityGroups[0].Tags))
	for _, tag := range desc.SecurityGroups[0].Tags {
		tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	assert.Equal(t, map[string]string{"env": "after", "praxis:managed-key": key}, tags)
}

func TestSGProvision_Idempotent(t *testing.T) {
	client, ec2Client := setupSGDriver(t)
	sgName := uniqueSGName(t)
	vpcId := getDefaultVpcId(t, ec2Client)
	registerSGCleanup(t, ec2Client, sgName, vpcId)
	key := fmt.Sprintf("%s~%s", vpcId, sgName)

	spec := sg.SecurityGroupSpec{
		Account:     integrationAccountName,
		GroupName:   sgName,
		Description: "Idempotent test",
		VpcId:       vpcId,
		IngressRules: []sg.IngressRule{
			{Protocol: "tcp", FromPort: 443, ToPort: 443, CidrBlock: "0.0.0.0/0"},
		},
	}

	// First provision
	out1, err := ingress.Object[sg.SecurityGroupSpec, sg.SecurityGroupOutputs](
		client, "SecurityGroup", key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)

	// Second provision with same spec — should succeed
	out2, err := ingress.Object[sg.SecurityGroupSpec, sg.SecurityGroupOutputs](
		client, "SecurityGroup", key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)
	assert.Equal(t, out1.GroupId, out2.GroupId)
}

func TestSGImport_ExistingGroup(t *testing.T) {
	client, ec2Client := setupSGDriver(t)
	sgName := uniqueSGName(t)
	vpcId := getDefaultVpcId(t, ec2Client)
	registerSGCleanup(t, ec2Client, sgName, vpcId)

	// Create SG directly in Moto
	createOut, err := ec2Client.CreateSecurityGroup(context.Background(), &ec2sdk.CreateSecurityGroupInput{
		GroupName:   aws.String(sgName),
		Description: aws.String("Manually created"),
		VpcId:       aws.String(vpcId),
	})
	require.NoError(t, err)
	groupId := aws.ToString(createOut.GroupId)

	// Import via driver
	key := fmt.Sprintf("%s~%s", vpcId, sgName)
	outputs, err := ingress.Object[types.ImportRef, sg.SecurityGroupOutputs](
		client, "SecurityGroup", key, "Import",
	).Request(t.Context(), types.ImportRef{
		ResourceID: groupId,
		Mode:       types.ModeManaged,
		Account:    integrationAccountName,
	})
	require.NoError(t, err)
	assert.Equal(t, groupId, outputs.GroupId)
}

func TestSGDelete_RemovesGroup(t *testing.T) {
	client, ec2Client := setupSGDriver(t)
	sgName := uniqueSGName(t)
	vpcId := getDefaultVpcId(t, ec2Client)
	registerSGCleanup(t, ec2Client, sgName, vpcId)
	key := fmt.Sprintf("%s~%s", vpcId, sgName)

	// Provision
	outputs, err := ingress.Object[sg.SecurityGroupSpec, sg.SecurityGroupOutputs](
		client, "SecurityGroup", key, "Provision",
	).Request(t.Context(), sg.SecurityGroupSpec{
		Account:     integrationAccountName,
		GroupName:   sgName,
		Description: "To be deleted",
		VpcId:       vpcId,
	})
	require.NoError(t, err)

	// Delete
	_, err = ingress.Object[restate.Void, restate.Void](
		client, "SecurityGroup", key, "Delete",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	// Verify SG is gone
	_, err = ec2Client.DescribeSecurityGroups(context.Background(), &ec2sdk.DescribeSecurityGroupsInput{
		GroupIds: []string{outputs.GroupId},
	})
	require.Error(t, err, "security group should be deleted from Moto")
}

func TestSGReconcile_DetectsDrift(t *testing.T) {
	client, ec2Client := setupSGDriver(t)
	sgName := uniqueSGName(t)
	vpcId := getDefaultVpcId(t, ec2Client)
	registerSGCleanup(t, ec2Client, sgName, vpcId)
	key := fmt.Sprintf("%s~%s", vpcId, sgName)
	streamKey := "dep-sg-drift-" + sgName
	registerDriftEventOwner(t, client, key, streamKey, sgName, sg.ServiceName)

	// Provision with one ingress rule
	outputs, err := ingress.Object[sg.SecurityGroupSpec, sg.SecurityGroupOutputs](
		client, "SecurityGroup", key, "Provision",
	).Request(t.Context(), sg.SecurityGroupSpec{
		Account:     integrationAccountName,
		GroupName:   sgName,
		Description: "Drift test",
		VpcId:       vpcId,
		IngressRules: []sg.IngressRule{
			{Protocol: "tcp", FromPort: 80, ToPort: 80, CidrBlock: "0.0.0.0/0"},
		},
	})
	require.NoError(t, err)

	// Introduce drift: add an extra ingress rule directly via EC2 API
	_, err = ec2Client.AuthorizeSecurityGroupIngress(context.Background(), &ec2sdk.AuthorizeSecurityGroupIngressInput{
		GroupId: aws.String(outputs.GroupId),
		IpPermissions: []ec2types.IpPermission{
			{
				IpProtocol: aws.String("tcp"),
				FromPort:   aws.Int32(22),
				ToPort:     aws.Int32(22),
				IpRanges:   []ec2types.IpRange{{CidrIp: aws.String("0.0.0.0/0")}},
			},
		},
	})
	require.NoError(t, err)

	// Trigger reconcile
	result, err := ingress.Object[restate.Void, types.ReconcileResult](
		client, "SecurityGroup", key, "Reconcile",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift, "drift should be detected")
	assert.True(t, result.Correcting, "managed mode should correct drift")
	assert.Contains(t, pollDriftEventTypes(t, client, streamKey, orchestrator.EventTypeDriftDetected, orchestrator.EventTypeDriftCorrected), orchestrator.EventTypeDriftDetected)
	assert.Contains(t, pollDriftEventTypes(t, client, streamKey, orchestrator.EventTypeDriftDetected, orchestrator.EventTypeDriftCorrected), orchestrator.EventTypeDriftCorrected)

	// Verify the extra rule was removed
	desc, err := ec2Client.DescribeSecurityGroups(context.Background(), &ec2sdk.DescribeSecurityGroupsInput{
		GroupIds: []string{outputs.GroupId},
	})
	require.NoError(t, err)
	require.Len(t, desc.SecurityGroups, 1)

	for _, perm := range desc.SecurityGroups[0].IpPermissions {
		if aws.ToInt32(perm.FromPort) == 22 {
			t.Error("port 22 rule should have been removed during drift correction")
		}
	}
}

func TestSGReconcile_EmitsExternalDeleteEvent(t *testing.T) {
	client, ec2Client := setupSGDriver(t)
	sgName := uniqueSGName(t)
	vpcId := getDefaultVpcId(t, ec2Client)
	registerSGCleanup(t, ec2Client, sgName, vpcId)
	key := fmt.Sprintf("%s~%s", vpcId, sgName)
	streamKey := "dep-sg-external-delete-" + sgName
	registerDriftEventOwner(t, client, key, streamKey, sgName, sg.ServiceName)

	outputs, err := ingress.Object[sg.SecurityGroupSpec, sg.SecurityGroupOutputs](
		client, "SecurityGroup", key, "Provision",
	).Request(t.Context(), sg.SecurityGroupSpec{
		Account:     integrationAccountName,
		GroupName:   sgName,
		Description: "Delete test",
		VpcId:       vpcId,
	})
	require.NoError(t, err)

	_, err = ec2Client.DeleteSecurityGroup(context.Background(), &ec2sdk.DeleteSecurityGroupInput{GroupId: aws.String(outputs.GroupId)})
	require.NoError(t, err)

	result, err := ingress.Object[restate.Void, types.ReconcileResult](
		client, "SecurityGroup", key, "Reconcile",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Contains(t, result.Error, "deleted externally")
	assert.True(t, result.ReplacementRequired)
	_, err = ec2Client.DescribeSecurityGroups(context.Background(), &ec2sdk.DescribeSecurityGroupsInput{GroupIds: []string{outputs.GroupId}})
	require.Error(t, err, "Reconcile must report replacement without recreating the security group")
	assert.Contains(t, pollDriftEventTypes(t, client, streamKey, orchestrator.EventTypeDriftExternalDelete), orchestrator.EventTypeDriftExternalDelete)
}

func TestSGGetStatus_ReturnsReady(t *testing.T) {
	client, ec2Client := setupSGDriver(t)
	sgName := uniqueSGName(t)
	vpcId := getDefaultVpcId(t, ec2Client)
	registerSGCleanup(t, ec2Client, sgName, vpcId)
	key := fmt.Sprintf("%s~%s", vpcId, sgName)

	// Provision
	_, err := ingress.Object[sg.SecurityGroupSpec, sg.SecurityGroupOutputs](
		client, "SecurityGroup", key, "Provision",
	).Request(t.Context(), sg.SecurityGroupSpec{
		Account:     integrationAccountName,
		GroupName:   sgName,
		Description: "Status test",
		VpcId:       vpcId,
	})
	require.NoError(t, err)

	// GetStatus
	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, "SecurityGroup", key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeManaged, status.Mode)
	assert.Greater(t, status.Generation, int64(0))
}
