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

	"github.com/shirvan/praxis/internal/drivers/subnet"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

func uniqueSubnetName(t *testing.T) string {
	t.Helper()
	name := strings.ReplaceAll(t.Name(), "/", "-")
	name = strings.ReplaceAll(name, "_", "-")
	if len(name) > 50 {
		name = name[:50]
	}
	return fmt.Sprintf("%s-%d", name, time.Now().UnixNano()%100000)
}

var subnetCIDRCounter int

func nextSubnetCIDRs() (string, string) {
	subnetCIDRCounter++
	segment := 100 + subnetCIDRCounter
	return fmt.Sprintf("10.%d.0.0/16", segment), fmt.Sprintf("10.%d.1.0/24", segment)
}

func setupSubnetIntegrationDriver(t *testing.T) (*ingress.Client, *ec2sdk.Client) {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := localstackAWSConfig(t)
	ec2Client := awsclient.NewEC2Client(awsCfg)
	driver := subnet.NewSubnetDriver(nil)

	env := restatetest.Start(t, restate.Reflect(driver))
	return env.Ingress(), ec2Client
}

func createSubnetTestVPC(t *testing.T, ec2Client *ec2sdk.Client, cidr string) string {
	t.Helper()
	out, err := ec2Client.CreateVpc(context.Background(), &ec2sdk.CreateVpcInput{CidrBlock: aws.String(cidr)})
	require.NoError(t, err)
	return aws.ToString(out.Vpc.VpcId)
}

func defaultAvailabilityZone(t *testing.T, ec2Client *ec2sdk.Client) string {
	t.Helper()
	out, err := ec2Client.DescribeSubnets(context.Background(), &ec2sdk.DescribeSubnetsInput{})
	require.NoError(t, err)
	require.NotEmpty(t, out.Subnets, "Moto should have at least one default subnet")
	return aws.ToString(out.Subnets[0].AvailabilityZone)
}

func waitForSubnetAvailable(t *testing.T, ec2Client *ec2sdk.Client, subnetID string) {
	t.Helper()
	waiter := ec2sdk.NewSubnetAvailableWaiter(ec2Client)
	err := waiter.Wait(context.Background(), &ec2sdk.DescribeSubnetsInput{SubnetIds: []string{subnetID}}, time.Minute)
	require.NoError(t, err)
}

func TestSubnetProvision_CreatesRealSubnet(t *testing.T) {
	client, ec2Client := setupSubnetIntegrationDriver(t)
	vpcCIDR, subnetCIDR := nextSubnetCIDRs()
	vpcID := createSubnetTestVPC(t, ec2Client, vpcCIDR)
	az := defaultAvailabilityZone(t, ec2Client)
	name := uniqueSubnetName(t)
	key := fmt.Sprintf("%s~%s", vpcID, name)

	outputs, err := ingress.Object[subnet.SubnetSpec, subnet.SubnetOutputs](client, subnet.ServiceName, key, "Provision").Request(t.Context(), subnet.SubnetSpec{
		Account:          integrationAccountName,
		Region:           "us-east-1",
		VpcId:            vpcID,
		CidrBlock:        subnetCIDR,
		AvailabilityZone: az,
		ManagedKey:       key,
		Tags:             map[string]string{"Name": name, "env": "test"},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, outputs.SubnetId)
	assert.Equal(t, vpcID, outputs.VpcId)
	assert.Equal(t, subnetCIDR, outputs.CidrBlock)

	desc, err := ec2Client.DescribeSubnets(context.Background(), &ec2sdk.DescribeSubnetsInput{SubnetIds: []string{outputs.SubnetId}})
	require.NoError(t, err)
	require.Len(t, desc.Subnets, 1)
	assert.Equal(t, subnetCIDR, aws.ToString(desc.Subnets[0].CidrBlock))
}

func TestSubnetProvision_Idempotent(t *testing.T) {
	client, ec2Client := setupSubnetIntegrationDriver(t)
	vpcCIDR, subnetCIDR := nextSubnetCIDRs()
	vpcID := createSubnetTestVPC(t, ec2Client, vpcCIDR)
	az := defaultAvailabilityZone(t, ec2Client)
	name := uniqueSubnetName(t)
	key := fmt.Sprintf("%s~%s", vpcID, name)

	spec := subnet.SubnetSpec{
		Account:          integrationAccountName,
		Region:           "us-east-1",
		VpcId:            vpcID,
		CidrBlock:        subnetCIDR,
		AvailabilityZone: az,
		ManagedKey:       key,
		Tags:             map[string]string{"Name": name},
	}

	out1, err := ingress.Object[subnet.SubnetSpec, subnet.SubnetOutputs](client, subnet.ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	out2, err := ingress.Object[subnet.SubnetSpec, subnet.SubnetOutputs](client, subnet.ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	assert.Equal(t, out1.SubnetId, out2.SubnetId)
}

func TestSubnetProvision_WithPublicIP(t *testing.T) {
	client, ec2Client := setupSubnetIntegrationDriver(t)
	vpcCIDR, subnetCIDR := nextSubnetCIDRs()
	vpcID := createSubnetTestVPC(t, ec2Client, vpcCIDR)
	az := defaultAvailabilityZone(t, ec2Client)
	name := uniqueSubnetName(t)
	key := fmt.Sprintf("%s~%s", vpcID, name)

	outputs, err := ingress.Object[subnet.SubnetSpec, subnet.SubnetOutputs](client, subnet.ServiceName, key, "Provision").Request(t.Context(), subnet.SubnetSpec{
		Account:             integrationAccountName,
		Region:              "us-east-1",
		VpcId:               vpcID,
		CidrBlock:           subnetCIDR,
		AvailabilityZone:    az,
		MapPublicIpOnLaunch: true,
		ManagedKey:          key,
		Tags:                map[string]string{"Name": name},
	})
	require.NoError(t, err)
	assert.True(t, outputs.MapPublicIpOnLaunch)

	desc, err := ec2Client.DescribeSubnets(context.Background(), &ec2sdk.DescribeSubnetsInput{SubnetIds: []string{outputs.SubnetId}})
	require.NoError(t, err)
	require.Len(t, desc.Subnets, 1)
	assert.True(t, aws.ToBool(desc.Subnets[0].MapPublicIpOnLaunch))
}

func TestSubnetImport_ExistingSubnet(t *testing.T) {
	client, ec2Client := setupSubnetIntegrationDriver(t)
	vpcCIDR, subnetCIDR := nextSubnetCIDRs()
	vpcID := createSubnetTestVPC(t, ec2Client, vpcCIDR)
	az := defaultAvailabilityZone(t, ec2Client)

	createOut, err := ec2Client.CreateSubnet(context.Background(), &ec2sdk.CreateSubnetInput{
		VpcId:            aws.String(vpcID),
		CidrBlock:        aws.String(subnetCIDR),
		AvailabilityZone: aws.String(az),
	})
	require.NoError(t, err)
	subnetID := aws.ToString(createOut.Subnet.SubnetId)
	waitForSubnetAvailable(t, ec2Client, subnetID)

	key := fmt.Sprintf("us-east-1~%s", subnetID)
	outputs, err := ingress.Object[types.ImportRef, subnet.SubnetOutputs](client, subnet.ServiceName, key, "Import").Request(t.Context(), types.ImportRef{
		ResourceID: subnetID,
		Account:    integrationAccountName,
	})
	require.NoError(t, err)
	assert.Equal(t, subnetID, outputs.SubnetId)
	assert.Equal(t, vpcID, outputs.VpcId)

	status, err := ingress.Object[restate.Void, types.StatusResponse](client, subnet.ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, status.Mode)
}

func TestSubnetDelete_DeletesSubnet(t *testing.T) {
	client, ec2Client := setupSubnetIntegrationDriver(t)
	vpcCIDR, subnetCIDR := nextSubnetCIDRs()
	vpcID := createSubnetTestVPC(t, ec2Client, vpcCIDR)
	az := defaultAvailabilityZone(t, ec2Client)
	name := uniqueSubnetName(t)
	key := fmt.Sprintf("%s~%s", vpcID, name)

	outputs, err := ingress.Object[subnet.SubnetSpec, subnet.SubnetOutputs](client, subnet.ServiceName, key, "Provision").Request(t.Context(), subnet.SubnetSpec{
		Account:          integrationAccountName,
		Region:           "us-east-1",
		VpcId:            vpcID,
		CidrBlock:        subnetCIDR,
		AvailabilityZone: az,
		ManagedKey:       key,
		Tags:             map[string]string{"Name": name},
	})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](client, subnet.ServiceName, key, "Delete").Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	_, err = ec2Client.DescribeSubnets(context.Background(), &ec2sdk.DescribeSubnetsInput{SubnetIds: []string{outputs.SubnetId}})
	require.Error(t, err)
}

func TestSubnetReconcile_DetectsTagDrift(t *testing.T) {
	client, ec2Client := setupSubnetIntegrationDriver(t)
	vpcCIDR, subnetCIDR := nextSubnetCIDRs()
	vpcID := createSubnetTestVPC(t, ec2Client, vpcCIDR)
	az := defaultAvailabilityZone(t, ec2Client)
	name := uniqueSubnetName(t)
	key := fmt.Sprintf("%s~%s", vpcID, name)

	outputs, err := ingress.Object[subnet.SubnetSpec, subnet.SubnetOutputs](client, subnet.ServiceName, key, "Provision").Request(t.Context(), subnet.SubnetSpec{
		Account:          integrationAccountName,
		Region:           "us-east-1",
		VpcId:            vpcID,
		CidrBlock:        subnetCIDR,
		AvailabilityZone: az,
		ManagedKey:       key,
		Tags:             map[string]string{"Name": name, "env": "desired"},
	})
	require.NoError(t, err)

	_, err = ec2Client.CreateTags(context.Background(), &ec2sdk.CreateTagsInput{
		Resources: []string{outputs.SubnetId},
		Tags:      []ec2types.Tag{{Key: aws.String("env"), Value: aws.String("rogue")}},
	})
	require.NoError(t, err)

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, subnet.ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)

	desc, err := ec2Client.DescribeSubnets(context.Background(), &ec2sdk.DescribeSubnetsInput{SubnetIds: []string{outputs.SubnetId}})
	require.NoError(t, err)
	require.Len(t, desc.Subnets, 1)
	tags := map[string]string{}
	for _, tag := range desc.Subnets[0].Tags {
		tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	assert.Equal(t, "desired", tags["env"])
}

func TestSubnetReconcile_DetectsMapPublicIpDrift(t *testing.T) {
	client, ec2Client := setupSubnetIntegrationDriver(t)
	vpcCIDR, subnetCIDR := nextSubnetCIDRs()
	vpcID := createSubnetTestVPC(t, ec2Client, vpcCIDR)
	az := defaultAvailabilityZone(t, ec2Client)
	name := uniqueSubnetName(t)
	key := fmt.Sprintf("%s~%s", vpcID, name)

	outputs, err := ingress.Object[subnet.SubnetSpec, subnet.SubnetOutputs](client, subnet.ServiceName, key, "Provision").Request(t.Context(), subnet.SubnetSpec{
		Account:             integrationAccountName,
		Region:              "us-east-1",
		VpcId:               vpcID,
		CidrBlock:           subnetCIDR,
		AvailabilityZone:    az,
		MapPublicIpOnLaunch: true,
		ManagedKey:          key,
		Tags:                map[string]string{"Name": name},
	})
	require.NoError(t, err)

	_, err = ec2Client.ModifySubnetAttribute(context.Background(), &ec2sdk.ModifySubnetAttributeInput{
		SubnetId: aws.String(outputs.SubnetId),
		MapPublicIpOnLaunch: &ec2types.AttributeBooleanValue{
			Value: aws.Bool(false),
		},
	})
	require.NoError(t, err)

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, subnet.ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)

	desc, err := ec2Client.DescribeSubnets(context.Background(), &ec2sdk.DescribeSubnetsInput{SubnetIds: []string{outputs.SubnetId}})
	require.NoError(t, err)
	require.Len(t, desc.Subnets, 1)
	assert.True(t, aws.ToBool(desc.Subnets[0].MapPublicIpOnLaunch))
}

func TestSubnetGetStatus_ReturnsReady(t *testing.T) {
	client, ec2Client := setupSubnetIntegrationDriver(t)
	vpcCIDR, subnetCIDR := nextSubnetCIDRs()
	vpcID := createSubnetTestVPC(t, ec2Client, vpcCIDR)
	az := defaultAvailabilityZone(t, ec2Client)
	name := uniqueSubnetName(t)
	key := fmt.Sprintf("%s~%s", vpcID, name)

	_, err := ingress.Object[subnet.SubnetSpec, subnet.SubnetOutputs](client, subnet.ServiceName, key, "Provision").Request(t.Context(), subnet.SubnetSpec{
		Account:          integrationAccountName,
		Region:           "us-east-1",
		VpcId:            vpcID,
		CidrBlock:        subnetCIDR,
		AvailabilityZone: az,
		ManagedKey:       key,
		Tags:             map[string]string{"Name": name},
	})
	require.NoError(t, err)

	status, err := ingress.Object[restate.Void, types.StatusResponse](client, subnet.ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeManaged, status.Mode)
	assert.Greater(t, status.Generation, int64(0))
}
