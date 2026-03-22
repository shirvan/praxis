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

	"github.com/praxiscloud/praxis/internal/drivers/vpcpeering"
	"github.com/praxiscloud/praxis/internal/infra/awsclient"
	"github.com/praxiscloud/praxis/pkg/types"
)

func uniqueVPCPeeringName(t *testing.T) string {
	t.Helper()
	name := strings.ReplaceAll(t.Name(), "/", "-")
	name = strings.ReplaceAll(name, "_", "-")
	if len(name) > 50 {
		name = name[:50]
	}
	return fmt.Sprintf("%s-%d", name, time.Now().UnixNano())
}

func setupVPCPeeringDriver(t *testing.T) (*ingress.Client, *ec2sdk.Client) {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := localstackAWSConfig(t)
	ec2Client := awsclient.NewEC2Client(awsCfg)
	driver := vpcpeering.NewVPCPeeringDriver(nil)

	env := restatetest.Start(t, restate.Reflect(driver))
	return env.Ingress(), ec2Client
}

func createIntegrationPeeringVPC(t *testing.T, ec2Client *ec2sdk.Client, cidr string) string {
	t.Helper()
	out, err := ec2Client.CreateVpc(context.Background(), &ec2sdk.CreateVpcInput{CidrBlock: aws.String(cidr)})
	require.NoError(t, err)
	return aws.ToString(out.Vpc.VpcId)
}

func TestVPCPeeringProvision_CreatesPeering(t *testing.T) {
	client, ec2Client := setupVPCPeeringDriver(t)
	requesterVpcID := createIntegrationPeeringVPC(t, ec2Client, uniqueCidr())
	accepterVpcID := createIntegrationPeeringVPC(t, ec2Client, uniqueCidr())
	name := uniqueVPCPeeringName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	outputs, err := ingress.Object[vpcpeering.VPCPeeringSpec, vpcpeering.VPCPeeringOutputs](client, vpcpeering.ServiceName, key, "Provision").Request(t.Context(), vpcpeering.VPCPeeringSpec{
		Account:        integrationAccountName,
		Region:         "us-east-1",
		RequesterVpcId: requesterVpcID,
		AccepterVpcId:  accepterVpcID,
		AutoAccept:     true,
		ManagedKey:     key,
		Tags:           map[string]string{"Name": name, "env": "test"},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, outputs.VpcPeeringConnectionId)
	assert.Equal(t, "active", outputs.Status)
	assert.Equal(t, requesterVpcID, outputs.RequesterVpcId)
	assert.Equal(t, accepterVpcID, outputs.AccepterVpcId)

	desc, err := ec2Client.DescribeVpcPeeringConnections(context.Background(), &ec2sdk.DescribeVpcPeeringConnectionsInput{
		VpcPeeringConnectionIds: []string{outputs.VpcPeeringConnectionId},
	})
	require.NoError(t, err)
	require.Len(t, desc.VpcPeeringConnections, 1)
	assert.Equal(t, ec2types.VpcPeeringConnectionStateReasonCodeActive, desc.VpcPeeringConnections[0].Status.Code)
}

func TestVPCPeeringProvision_Idempotent(t *testing.T) {
	client, ec2Client := setupVPCPeeringDriver(t)
	requesterVpcID := createIntegrationPeeringVPC(t, ec2Client, uniqueCidr())
	accepterVpcID := createIntegrationPeeringVPC(t, ec2Client, uniqueCidr())
	name := uniqueVPCPeeringName(t)
	key := fmt.Sprintf("us-east-1~%s", name)
	spec := vpcpeering.VPCPeeringSpec{
		Account:        integrationAccountName,
		Region:         "us-east-1",
		RequesterVpcId: requesterVpcID,
		AccepterVpcId:  accepterVpcID,
		AutoAccept:     true,
		ManagedKey:     key,
		Tags:           map[string]string{"Name": name},
	}

	out1, err := ingress.Object[vpcpeering.VPCPeeringSpec, vpcpeering.VPCPeeringOutputs](client, vpcpeering.ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	out2, err := ingress.Object[vpcpeering.VPCPeeringSpec, vpcpeering.VPCPeeringOutputs](client, vpcpeering.ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	assert.Equal(t, out1.VpcPeeringConnectionId, out2.VpcPeeringConnectionId)
}

func TestVPCPeeringImport_Existing(t *testing.T) {
	client, ec2Client := setupVPCPeeringDriver(t)
	requesterVpcID := createIntegrationPeeringVPC(t, ec2Client, uniqueCidr())
	accepterVpcID := createIntegrationPeeringVPC(t, ec2Client, uniqueCidr())

	createOut, err := ec2Client.CreateVpcPeeringConnection(context.Background(), &ec2sdk.CreateVpcPeeringConnectionInput{
		VpcId:     aws.String(requesterVpcID),
		PeerVpcId: aws.String(accepterVpcID),
	})
	require.NoError(t, err)
	peeringID := aws.ToString(createOut.VpcPeeringConnection.VpcPeeringConnectionId)

	_, err = ec2Client.AcceptVpcPeeringConnection(context.Background(), &ec2sdk.AcceptVpcPeeringConnectionInput{
		VpcPeeringConnectionId: aws.String(peeringID),
	})
	require.NoError(t, err)

	key := fmt.Sprintf("us-east-1~%s", peeringID)
	outputs, err := ingress.Object[types.ImportRef, vpcpeering.VPCPeeringOutputs](client, vpcpeering.ServiceName, key, "Import").Request(t.Context(), types.ImportRef{
		ResourceID: peeringID,
		Account:    integrationAccountName,
	})
	require.NoError(t, err)
	assert.Equal(t, peeringID, outputs.VpcPeeringConnectionId)
	assert.Equal(t, "active", outputs.Status)

	status, err := ingress.Object[restate.Void, types.StatusResponse](client, vpcpeering.ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, status.Mode)
}

func TestVPCPeeringDelete_Deletes(t *testing.T) {
	client, ec2Client := setupVPCPeeringDriver(t)
	requesterVpcID := createIntegrationPeeringVPC(t, ec2Client, uniqueCidr())
	accepterVpcID := createIntegrationPeeringVPC(t, ec2Client, uniqueCidr())
	name := uniqueVPCPeeringName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	out, err := ingress.Object[vpcpeering.VPCPeeringSpec, vpcpeering.VPCPeeringOutputs](client, vpcpeering.ServiceName, key, "Provision").Request(t.Context(), vpcpeering.VPCPeeringSpec{
		Account:        integrationAccountName,
		Region:         "us-east-1",
		RequesterVpcId: requesterVpcID,
		AccepterVpcId:  accepterVpcID,
		AutoAccept:     true,
		ManagedKey:     key,
		Tags:           map[string]string{"Name": name},
	})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](client, vpcpeering.ServiceName, key, "Delete").Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	desc, err := ec2Client.DescribeVpcPeeringConnections(context.Background(), &ec2sdk.DescribeVpcPeeringConnectionsInput{
		VpcPeeringConnectionIds: []string{out.VpcPeeringConnectionId},
	})
	if err != nil {
		return
	}
	require.Len(t, desc.VpcPeeringConnections, 1)
	assert.Equal(t, ec2types.VpcPeeringConnectionStateReasonCodeDeleted, desc.VpcPeeringConnections[0].Status.Code)
}

func TestVPCPeeringReconcile_TagDrift(t *testing.T) {
	client, ec2Client := setupVPCPeeringDriver(t)
	requesterVpcID := createIntegrationPeeringVPC(t, ec2Client, uniqueCidr())
	accepterVpcID := createIntegrationPeeringVPC(t, ec2Client, uniqueCidr())
	name := uniqueVPCPeeringName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	out, err := ingress.Object[vpcpeering.VPCPeeringSpec, vpcpeering.VPCPeeringOutputs](client, vpcpeering.ServiceName, key, "Provision").Request(t.Context(), vpcpeering.VPCPeeringSpec{
		Account:        integrationAccountName,
		Region:         "us-east-1",
		RequesterVpcId: requesterVpcID,
		AccepterVpcId:  accepterVpcID,
		AutoAccept:     true,
		ManagedKey:     key,
		Tags:           map[string]string{"Name": name, "env": "managed"},
	})
	require.NoError(t, err)

	_, err = ec2Client.DeleteTags(context.Background(), &ec2sdk.DeleteTagsInput{
		Resources: []string{out.VpcPeeringConnectionId},
		Tags:      []ec2types.Tag{{Key: aws.String("env")}},
	})
	require.NoError(t, err)

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, vpcpeering.ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)

	desc, err := ec2Client.DescribeVpcPeeringConnections(context.Background(), &ec2sdk.DescribeVpcPeeringConnectionsInput{
		VpcPeeringConnectionIds: []string{out.VpcPeeringConnectionId},
	})
	require.NoError(t, err)
	require.Len(t, desc.VpcPeeringConnections, 1)
	assert.Contains(t, desc.VpcPeeringConnections[0].Tags, ec2types.Tag{Key: aws.String("env"), Value: aws.String("managed")})
}

func TestVPCPeeringGetStatus_ReturnsReady(t *testing.T) {
	client, ec2Client := setupVPCPeeringDriver(t)
	requesterVpcID := createIntegrationPeeringVPC(t, ec2Client, uniqueCidr())
	accepterVpcID := createIntegrationPeeringVPC(t, ec2Client, uniqueCidr())
	name := uniqueVPCPeeringName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	_, err := ingress.Object[vpcpeering.VPCPeeringSpec, vpcpeering.VPCPeeringOutputs](client, vpcpeering.ServiceName, key, "Provision").Request(t.Context(), vpcpeering.VPCPeeringSpec{
		Account:        integrationAccountName,
		Region:         "us-east-1",
		RequesterVpcId: requesterVpcID,
		AccepterVpcId:  accepterVpcID,
		AutoAccept:     true,
		ManagedKey:     key,
		Tags:           map[string]string{"Name": name},
	})
	require.NoError(t, err)

	status, err := ingress.Object[restate.Void, types.StatusResponse](client, vpcpeering.ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeManaged, status.Mode)
	assert.Greater(t, status.Generation, int64(0))
}
