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

	"github.com/praxiscloud/praxis/internal/drivers/sg"
	"github.com/praxiscloud/praxis/internal/infra/awsclient"
	"github.com/praxiscloud/praxis/pkg/types"
)

// uniqueSGName generates a unique security group name for each test.
func uniqueSGName(t *testing.T) string {
	t.Helper()
	name := strings.ReplaceAll(t.Name(), "/", "-")
	name = strings.ReplaceAll(name, "_", "-")
	if len(name) > 50 {
		name = name[:50]
	}
	return fmt.Sprintf("%s-%d", name, time.Now().UnixNano()%100000)
}

// setupSGDriver starts a Restate test environment with the SG driver registered.
func setupSGDriver(t *testing.T) (*ingress.Client, *ec2sdk.Client) {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := localstackAWSConfig(t)
	ec2Client := awsclient.NewEC2Client(awsCfg)
	driver := sg.NewSecurityGroupDriver(nil)

	env := restatetest.Start(t, restate.Reflect(driver))
	return env.Ingress(), ec2Client
}

// getDefaultVpcId returns the default VPC ID from LocalStack.
func getDefaultVpcId(t *testing.T, ec2Client *ec2sdk.Client) string {
	t.Helper()
	out, err := ec2Client.DescribeVpcs(context.Background(), &ec2sdk.DescribeVpcsInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("isDefault"), Values: []string{"true"}},
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, out.Vpcs, "LocalStack should have a default VPC")
	return aws.ToString(out.Vpcs[0].VpcId)
}

func TestSGProvision_CreatesSecurityGroup(t *testing.T) {
	client, ec2Client := setupSGDriver(t)
	sgName := uniqueSGName(t)
	vpcId := getDefaultVpcId(t, ec2Client)
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

	// Verify SG exists in LocalStack
	desc, err := ec2Client.DescribeSecurityGroups(context.Background(), &ec2sdk.DescribeSecurityGroupsInput{
		GroupIds: []string{outputs.GroupId},
	})
	require.NoError(t, err)
	require.Len(t, desc.SecurityGroups, 1)
	assert.Equal(t, sgName, aws.ToString(desc.SecurityGroups[0].GroupName))
}

func TestSGProvision_Idempotent(t *testing.T) {
	client, ec2Client := setupSGDriver(t)
	sgName := uniqueSGName(t)
	vpcId := getDefaultVpcId(t, ec2Client)
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

	// Create SG directly in LocalStack
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
	require.Error(t, err, "security group should be deleted from LocalStack")
}

func TestSGReconcile_DetectsDrift(t *testing.T) {
	client, ec2Client := setupSGDriver(t)
	sgName := uniqueSGName(t)
	vpcId := getDefaultVpcId(t, ec2Client)
	key := fmt.Sprintf("%s~%s", vpcId, sgName)

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

func TestSGGetStatus_ReturnsReady(t *testing.T) {
	client, ec2Client := setupSGDriver(t)
	sgName := uniqueSGName(t)
	vpcId := getDefaultVpcId(t, ec2Client)
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
