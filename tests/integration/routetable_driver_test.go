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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	restatetest "github.com/restatedev/sdk-go/testing"

	"github.com/shirvan/praxis/internal/drivers/routetable"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

func uniqueRouteTableName(t *testing.T) string {
	t.Helper()
	name := strings.ReplaceAll(t.Name(), "/", "-")
	name = strings.ReplaceAll(name, "_", "-")
	if len(name) > 50 {
		name = name[:50]
	}
	return fmt.Sprintf("%s-%d", name, time.Now().UnixNano()%100000)
}

func setupRouteTableDriverIntegration(t *testing.T) (*ingress.Client, *ec2sdk.Client) {
	t.Helper()
	configureLocalAccount(t)
	awsCfg := localstackAWSConfig(t)
	ec2Client := awsclient.NewEC2Client(awsCfg)
	driver := routetable.NewRouteTableDriver(nil)
	env := restatetest.Start(t, restate.Reflect(driver))
	return env.Ingress(), ec2Client
}

func createRouteTableTestVPC(t *testing.T, ec2Client *ec2sdk.Client, cidr string) string {
	t.Helper()
	out, err := ec2Client.CreateVpc(context.Background(), &ec2sdk.CreateVpcInput{CidrBlock: aws.String(cidr)})
	require.NoError(t, err)
	return aws.ToString(out.Vpc.VpcId)
}

func createRouteTableTestSubnet(t *testing.T, ec2Client *ec2sdk.Client, vpcID string, cidr string) string {
	t.Helper()
	out, err := ec2Client.CreateSubnet(context.Background(), &ec2sdk.CreateSubnetInput{VpcId: aws.String(vpcID), CidrBlock: aws.String(cidr)})
	require.NoError(t, err)
	return aws.ToString(out.Subnet.SubnetId)
}

func createInternetGateway(t *testing.T, ec2Client *ec2sdk.Client, vpcID string) string {
	t.Helper()
	out, err := ec2Client.CreateInternetGateway(context.Background(), &ec2sdk.CreateInternetGatewayInput{})
	require.NoError(t, err)
	igwID := aws.ToString(out.InternetGateway.InternetGatewayId)
	_, err = ec2Client.AttachInternetGateway(context.Background(), &ec2sdk.AttachInternetGatewayInput{InternetGatewayId: aws.String(igwID), VpcId: aws.String(vpcID)})
	require.NoError(t, err)
	return igwID
}

func TestRouteTableProvision_CreatesWithRoutes(t *testing.T) {
	client, ec2Client := setupRouteTableDriverIntegration(t)
	vpcID := createRouteTableTestVPC(t, ec2Client, "10.30.0.0/16")
	igwID := createInternetGateway(t, ec2Client, vpcID)
	name := uniqueRouteTableName(t)
	key := fmt.Sprintf("%s~%s", vpcID, name)

	outputs, err := ingress.Object[routetable.RouteTableSpec, routetable.RouteTableOutputs](client, routetable.ServiceName, key, "Provision").Request(t.Context(), routetable.RouteTableSpec{
		Account:    integrationAccountName,
		Region:     "us-east-1",
		VpcId:      vpcID,
		ManagedKey: key,
		Routes:     []routetable.Route{{DestinationCidrBlock: "0.0.0.0/0", GatewayId: igwID}},
		Tags:       map[string]string{"Name": name},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, outputs.RouteTableId)

	desc, err := ec2Client.DescribeRouteTables(context.Background(), &ec2sdk.DescribeRouteTablesInput{RouteTableIds: []string{outputs.RouteTableId}})
	require.NoError(t, err)
	require.Len(t, desc.RouteTables, 1)
	foundDefaultRoute := false
	for _, route := range desc.RouteTables[0].Routes {
		if aws.ToString(route.DestinationCidrBlock) == "0.0.0.0/0" && aws.ToString(route.GatewayId) == igwID {
			foundDefaultRoute = true
		}
	}
	assert.True(t, foundDefaultRoute)
}

func TestRouteTableProvision_WithSubnetAssociation(t *testing.T) {
	client, ec2Client := setupRouteTableDriverIntegration(t)
	vpcID := createRouteTableTestVPC(t, ec2Client, "10.31.0.0/16")
	subnetID := createRouteTableTestSubnet(t, ec2Client, vpcID, "10.31.1.0/24")
	name := uniqueRouteTableName(t)
	key := fmt.Sprintf("%s~%s", vpcID, name)

	outputs, err := ingress.Object[routetable.RouteTableSpec, routetable.RouteTableOutputs](client, routetable.ServiceName, key, "Provision").Request(t.Context(), routetable.RouteTableSpec{
		Account:      integrationAccountName,
		Region:       "us-east-1",
		VpcId:        vpcID,
		ManagedKey:   key,
		Associations: []routetable.Association{{SubnetId: subnetID}},
		Tags:         map[string]string{"Name": name},
	})
	require.NoError(t, err)
	require.Len(t, outputs.Associations, 1)
	assert.Equal(t, subnetID, outputs.Associations[0].SubnetId)
}

func TestRouteTableImport_Existing(t *testing.T) {
	client, ec2Client := setupRouteTableDriverIntegration(t)
	vpcID := createRouteTableTestVPC(t, ec2Client, "10.32.0.0/16")
	out, err := ec2Client.CreateRouteTable(context.Background(), &ec2sdk.CreateRouteTableInput{VpcId: aws.String(vpcID)})
	require.NoError(t, err)
	routeTableID := aws.ToString(out.RouteTable.RouteTableId)
	key := fmt.Sprintf("us-east-1~%s", routeTableID)

	outputs, err := ingress.Object[types.ImportRef, routetable.RouteTableOutputs](client, routetable.ServiceName, key, "Import").Request(t.Context(), types.ImportRef{ResourceID: routeTableID, Account: integrationAccountName})
	require.NoError(t, err)
	assert.Equal(t, routeTableID, outputs.RouteTableId)
	assert.Equal(t, vpcID, outputs.VpcId)
}
