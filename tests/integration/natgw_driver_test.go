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

	"github.com/shirvan/praxis/internal/drivers/natgw"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

func uniqueNATGatewayName(t *testing.T) string {
	t.Helper()
	name := strings.ReplaceAll(t.Name(), "/", "-")
	name = strings.ReplaceAll(name, "_", "-")
	if len(name) > 50 {
		name = name[:50]
	}
	return fmt.Sprintf("%s-%d", name, time.Now().UnixNano()%1000000000)
}

func setupNATGatewayIntegrationDriver(t *testing.T) (*ingress.Client, *ec2sdk.Client) {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := localstackAWSConfig(t)
	ec2Client := awsclient.NewEC2Client(awsCfg)
	driver := natgw.NewNATGatewayDriver(nil)

	env := restatetest.Start(t, restate.Reflect(driver))
	return env.Ingress(), ec2Client
}

func createNATGatewayFixture(t *testing.T, ec2Client *ec2sdk.Client, public bool) (string, string, string) {
	t.Helper()
	block := 10 + int(time.Now().UnixNano()%200)
	vpcCIDR := fmt.Sprintf("10.%d.0.0/16", block)
	subnetCIDR := fmt.Sprintf("10.%d.1.0/24", block)

	vpcOut, err := ec2Client.CreateVpc(context.Background(), &ec2sdk.CreateVpcInput{CidrBlock: aws.String(vpcCIDR)})
	require.NoError(t, err)
	vpcID := aws.ToString(vpcOut.Vpc.VpcId)

	subnetOut, err := ec2Client.CreateSubnet(context.Background(), &ec2sdk.CreateSubnetInput{
		VpcId:            aws.String(vpcID),
		CidrBlock:        aws.String(subnetCIDR),
		AvailabilityZone: aws.String("us-east-1a"),
	})
	require.NoError(t, err)
	subnetID := aws.ToString(subnetOut.Subnet.SubnetId)

	allocationID := ""
	if public {
		igwOut, err := ec2Client.CreateInternetGateway(context.Background(), &ec2sdk.CreateInternetGatewayInput{})
		require.NoError(t, err)
		igwID := aws.ToString(igwOut.InternetGateway.InternetGatewayId)

		_, err = ec2Client.AttachInternetGateway(context.Background(), &ec2sdk.AttachInternetGatewayInput{
			InternetGatewayId: aws.String(igwID),
			VpcId:             aws.String(vpcID),
		})
		require.NoError(t, err)

		routeTableOut, err := ec2Client.CreateRouteTable(context.Background(), &ec2sdk.CreateRouteTableInput{VpcId: aws.String(vpcID)})
		require.NoError(t, err)
		routeTableID := aws.ToString(routeTableOut.RouteTable.RouteTableId)

		_, err = ec2Client.CreateRoute(context.Background(), &ec2sdk.CreateRouteInput{
			RouteTableId:         aws.String(routeTableID),
			DestinationCidrBlock: aws.String("0.0.0.0/0"),
			GatewayId:            aws.String(igwID),
		})
		require.NoError(t, err)

		_, err = ec2Client.AssociateRouteTable(context.Background(), &ec2sdk.AssociateRouteTableInput{
			RouteTableId: aws.String(routeTableID),
			SubnetId:     aws.String(subnetID),
		})
		require.NoError(t, err)

		eipOut, err := ec2Client.AllocateAddress(context.Background(), &ec2sdk.AllocateAddressInput{Domain: ec2types.DomainTypeVpc})
		require.NoError(t, err)
		allocationID = aws.ToString(eipOut.AllocationId)
	}

	return vpcID, subnetID, allocationID
}

func createRawNATGateway(t *testing.T, ec2Client *ec2sdk.Client, subnetID, allocationID, connectivityType string, tags map[string]string) string {
	t.Helper()
	input := &ec2sdk.CreateNatGatewayInput{
		SubnetId:         aws.String(subnetID),
		ConnectivityType: ec2types.ConnectivityType(connectivityType),
	}
	if allocationID != "" {
		input.AllocationId = aws.String(allocationID)
	}
	for key, value := range tags {
		input.TagSpecifications = []ec2types.TagSpecification{{
			ResourceType: ec2types.ResourceTypeNatgateway,
			Tags: []ec2types.Tag{{
				Key:   aws.String(key),
				Value: aws.String(value),
			}},
		}}
	}
	out, err := ec2Client.CreateNatGateway(context.Background(), input)
	require.NoError(t, err)
	id := aws.ToString(out.NatGateway.NatGatewayId)
	waiter := ec2sdk.NewNatGatewayAvailableWaiter(ec2Client)
	require.NoError(t, waiter.Wait(context.Background(), &ec2sdk.DescribeNatGatewaysInput{NatGatewayIds: []string{id}}, 2*time.Minute))
	return id
}

func TestNATGWProvision_CreatesPublicNATGW(t *testing.T) {
	client, ec2Client := setupNATGatewayIntegrationDriver(t)
	name := uniqueNATGatewayName(t)
	key := fmt.Sprintf("us-east-1~%s", name)
	_, subnetID, allocationID := createNATGatewayFixture(t, ec2Client, true)

	outputs, err := ingress.Object[natgw.NATGatewaySpec, natgw.NATGatewayOutputs](client, "NATGateway", key, "Provision").Request(t.Context(), natgw.NATGatewaySpec{
		Account:          integrationAccountName,
		Region:           "us-east-1",
		SubnetId:         subnetID,
		ConnectivityType: "public",
		AllocationId:     allocationID,
		ManagedKey:       key,
		Tags:             map[string]string{"Name": name, "env": "test"},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, outputs.NatGatewayId)
	assert.Equal(t, allocationID, outputs.AllocationId)

	desc, err := ec2Client.DescribeNatGateways(context.Background(), &ec2sdk.DescribeNatGatewaysInput{NatGatewayIds: []string{outputs.NatGatewayId}})
	require.NoError(t, err)
	require.Len(t, desc.NatGateways, 1)
	assert.Equal(t, outputs.NatGatewayId, aws.ToString(desc.NatGateways[0].NatGatewayId))
}

func TestNATGWProvision_CreatesPrivateNATGW(t *testing.T) {
	client, ec2Client := setupNATGatewayIntegrationDriver(t)
	name := uniqueNATGatewayName(t)
	key := fmt.Sprintf("us-east-1~%s", name)
	_, subnetID, _ := createNATGatewayFixture(t, ec2Client, false)

	outputs, err := ingress.Object[natgw.NATGatewaySpec, natgw.NATGatewayOutputs](client, "NATGateway", key, "Provision").Request(t.Context(), natgw.NATGatewaySpec{
		Account:          integrationAccountName,
		Region:           "us-east-1",
		SubnetId:         subnetID,
		ConnectivityType: "private",
		ManagedKey:       key,
		Tags:             map[string]string{"Name": name},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, outputs.NatGatewayId)
	assert.Equal(t, "private", outputs.ConnectivityType)
	assert.Empty(t, outputs.AllocationId)
}

func TestNATGWProvision_Idempotent(t *testing.T) {
	client, ec2Client := setupNATGatewayIntegrationDriver(t)
	name := uniqueNATGatewayName(t)
	key := fmt.Sprintf("us-east-1~%s", name)
	_, subnetID, allocationID := createNATGatewayFixture(t, ec2Client, true)

	spec := natgw.NATGatewaySpec{
		Account:          integrationAccountName,
		Region:           "us-east-1",
		SubnetId:         subnetID,
		ConnectivityType: "public",
		AllocationId:     allocationID,
		ManagedKey:       key,
		Tags:             map[string]string{"Name": name},
	}

	out1, err := ingress.Object[natgw.NATGatewaySpec, natgw.NATGatewayOutputs](client, "NATGateway", key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	out2, err := ingress.Object[natgw.NATGatewaySpec, natgw.NATGatewayOutputs](client, "NATGateway", key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	assert.Equal(t, out1.NatGatewayId, out2.NatGatewayId)
}

func TestNATGWImport_Existing(t *testing.T) {
	client, ec2Client := setupNATGatewayIntegrationDriver(t)
	name := uniqueNATGatewayName(t)
	_, subnetID, allocationID := createNATGatewayFixture(t, ec2Client, true)
	natGatewayID := createRawNATGateway(t, ec2Client, subnetID, allocationID, "public", map[string]string{"Name": name})
	key := fmt.Sprintf("us-east-1~%s", natGatewayID)

	outputs, err := ingress.Object[types.ImportRef, natgw.NATGatewayOutputs](client, "NATGateway", key, "Import").Request(t.Context(), types.ImportRef{ResourceID: natGatewayID, Account: integrationAccountName})
	require.NoError(t, err)
	assert.Equal(t, natGatewayID, outputs.NatGatewayId)

	status, err := ingress.Object[restate.Void, types.StatusResponse](client, "NATGateway", key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, status.Mode)
}

func TestNATGWDelete_DeletesAndWaits(t *testing.T) {
	client, ec2Client := setupNATGatewayIntegrationDriver(t)
	name := uniqueNATGatewayName(t)
	key := fmt.Sprintf("us-east-1~%s", name)
	_, subnetID, allocationID := createNATGatewayFixture(t, ec2Client, true)

	outputs, err := ingress.Object[natgw.NATGatewaySpec, natgw.NATGatewayOutputs](client, "NATGateway", key, "Provision").Request(t.Context(), natgw.NATGatewaySpec{
		Account:          integrationAccountName,
		Region:           "us-east-1",
		SubnetId:         subnetID,
		ConnectivityType: "public",
		AllocationId:     allocationID,
		ManagedKey:       key,
		Tags:             map[string]string{"Name": name},
	})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](client, "NATGateway", key, "Delete").Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	desc, err := ec2Client.DescribeNatGateways(context.Background(), &ec2sdk.DescribeNatGatewaysInput{NatGatewayIds: []string{outputs.NatGatewayId}})
	if err == nil && len(desc.NatGateways) > 0 {
		assert.Equal(t, "deleted", string(desc.NatGateways[0].State))
	}
}

func TestNATGWReconcile_TagDrift(t *testing.T) {
	client, ec2Client := setupNATGatewayIntegrationDriver(t)
	name := uniqueNATGatewayName(t)
	key := fmt.Sprintf("us-east-1~%s", name)
	_, subnetID, allocationID := createNATGatewayFixture(t, ec2Client, true)

	outputs, err := ingress.Object[natgw.NATGatewaySpec, natgw.NATGatewayOutputs](client, "NATGateway", key, "Provision").Request(t.Context(), natgw.NATGatewaySpec{
		Account:          integrationAccountName,
		Region:           "us-east-1",
		SubnetId:         subnetID,
		ConnectivityType: "public",
		AllocationId:     allocationID,
		ManagedKey:       key,
		Tags:             map[string]string{"Name": name, "env": "managed"},
	})
	require.NoError(t, err)

	_, err = ec2Client.DeleteTags(context.Background(), &ec2sdk.DeleteTagsInput{Resources: []string{outputs.NatGatewayId}, Tags: []ec2types.Tag{{Key: aws.String("env")}}})
	require.NoError(t, err)

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, "NATGateway", key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)

	desc, err := ec2Client.DescribeNatGateways(context.Background(), &ec2sdk.DescribeNatGatewaysInput{NatGatewayIds: []string{outputs.NatGatewayId}})
	require.NoError(t, err)
	require.Len(t, desc.NatGateways, 1)
	assert.Contains(t, desc.NatGateways[0].Tags, ec2types.Tag{Key: aws.String("env"), Value: aws.String("managed")})
}

func TestNATGWGetStatus_ReturnsReady(t *testing.T) {
	client, ec2Client := setupNATGatewayIntegrationDriver(t)
	name := uniqueNATGatewayName(t)
	key := fmt.Sprintf("us-east-1~%s", name)
	_, subnetID, allocationID := createNATGatewayFixture(t, ec2Client, true)

	_, err := ingress.Object[natgw.NATGatewaySpec, natgw.NATGatewayOutputs](client, "NATGateway", key, "Provision").Request(t.Context(), natgw.NATGatewaySpec{
		Account:          integrationAccountName,
		Region:           "us-east-1",
		SubnetId:         subnetID,
		ConnectivityType: "public",
		AllocationId:     allocationID,
		ManagedKey:       key,
		Tags:             map[string]string{"Name": name},
	})
	require.NoError(t, err)

	status, err := ingress.Object[restate.Void, types.StatusResponse](client, "NATGateway", key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeManaged, status.Mode)
	assert.Greater(t, status.Generation, int64(0))
}
