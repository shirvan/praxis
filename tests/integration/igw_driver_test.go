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

	"github.com/praxiscloud/praxis/internal/drivers/igw"
	"github.com/praxiscloud/praxis/internal/infra/awsclient"
	"github.com/praxiscloud/praxis/pkg/types"
)

func uniqueIGWName(t *testing.T) string {
	t.Helper()
	name := strings.ReplaceAll(t.Name(), "/", "-")
	name = strings.ReplaceAll(name, "_", "-")
	if len(name) > 50 {
		name = name[:50]
	}
	return fmt.Sprintf("%s-%d", name, time.Now().UnixNano()%1000000000)
}

func setupIGWIntegrationDriver(t *testing.T) (*ingress.Client, *ec2sdk.Client) {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := localstackAWSConfig(t)
	ec2Client := awsclient.NewEC2Client(awsCfg)
	driver := igw.NewIGWDriver(nil)

	env := restatetest.Start(t, restate.Reflect(driver))
	return env.Ingress(), ec2Client
}

func createIntegrationVPC(t *testing.T, ec2Client *ec2sdk.Client) string {
	t.Helper()
	out, err := ec2Client.CreateVpc(context.Background(), &ec2sdk.CreateVpcInput{
		CidrBlock: aws.String(uniqueCidr()),
	})
	require.NoError(t, err)
	return aws.ToString(out.Vpc.VpcId)
}

func TestIGWProvision_CreatesAndAttaches(t *testing.T) {
	client, ec2Client := setupIGWIntegrationDriver(t)
	vpcID := createIntegrationVPC(t, ec2Client)
	name := uniqueIGWName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	outputs, err := ingress.Object[igw.IGWSpec, igw.IGWOutputs](client, igw.ServiceName, key, "Provision").Request(t.Context(), igw.IGWSpec{
		Account:    integrationAccountName,
		Region:     "us-east-1",
		VpcId:      vpcID,
		ManagedKey: key,
		Tags:       map[string]string{"Name": name, "env": "test"},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, outputs.InternetGatewayId)
	assert.Equal(t, vpcID, outputs.VpcId)

	desc, err := ec2Client.DescribeInternetGateways(context.Background(), &ec2sdk.DescribeInternetGatewaysInput{
		InternetGatewayIds: []string{outputs.InternetGatewayId},
	})
	require.NoError(t, err)
	require.Len(t, desc.InternetGateways, 1)
	require.NotEmpty(t, desc.InternetGateways[0].Attachments)
	assert.Equal(t, vpcID, aws.ToString(desc.InternetGateways[0].Attachments[0].VpcId))
}

func TestIGWProvision_Idempotent(t *testing.T) {
	client, ec2Client := setupIGWIntegrationDriver(t)
	vpcID := createIntegrationVPC(t, ec2Client)
	name := uniqueIGWName(t)
	key := fmt.Sprintf("us-east-1~%s", name)
	spec := igw.IGWSpec{
		Account:    integrationAccountName,
		Region:     "us-east-1",
		VpcId:      vpcID,
		ManagedKey: key,
		Tags:       map[string]string{"Name": name},
	}

	out1, err := ingress.Object[igw.IGWSpec, igw.IGWOutputs](client, igw.ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	out2, err := ingress.Object[igw.IGWSpec, igw.IGWOutputs](client, igw.ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	assert.Equal(t, out1.InternetGatewayId, out2.InternetGatewayId)
}

func TestIGWImport_ExistingIGW(t *testing.T) {
	client, ec2Client := setupIGWIntegrationDriver(t)
	vpcID := createIntegrationVPC(t, ec2Client)
	name := uniqueIGWName(t)

	createOut, err := ec2Client.CreateInternetGateway(context.Background(), &ec2sdk.CreateInternetGatewayInput{})
	require.NoError(t, err)
	igwID := aws.ToString(createOut.InternetGateway.InternetGatewayId)

	_, err = ec2Client.AttachInternetGateway(context.Background(), &ec2sdk.AttachInternetGatewayInput{
		InternetGatewayId: aws.String(igwID),
		VpcId:             aws.String(vpcID),
	})
	require.NoError(t, err)

	_, err = ec2Client.CreateTags(context.Background(), &ec2sdk.CreateTagsInput{
		Resources: []string{igwID},
		Tags:      []ec2types.Tag{{Key: aws.String("Name"), Value: aws.String(name)}},
	})
	require.NoError(t, err)

	key := fmt.Sprintf("us-east-1~%s", igwID)
	outputs, err := ingress.Object[types.ImportRef, igw.IGWOutputs](client, igw.ServiceName, key, "Import").Request(t.Context(), types.ImportRef{ResourceID: igwID, Account: integrationAccountName})
	require.NoError(t, err)
	assert.Equal(t, igwID, outputs.InternetGatewayId)
	assert.Equal(t, vpcID, outputs.VpcId)

	status, err := ingress.Object[restate.Void, types.StatusResponse](client, igw.ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, status.Mode)
}

func TestIGWDelete_DetachesAndDeletes(t *testing.T) {
	client, ec2Client := setupIGWIntegrationDriver(t)
	vpcID := createIntegrationVPC(t, ec2Client)
	name := uniqueIGWName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	out, err := ingress.Object[igw.IGWSpec, igw.IGWOutputs](client, igw.ServiceName, key, "Provision").Request(t.Context(), igw.IGWSpec{
		Account:    integrationAccountName,
		Region:     "us-east-1",
		VpcId:      vpcID,
		ManagedKey: key,
		Tags:       map[string]string{"Name": name},
	})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](client, igw.ServiceName, key, "Delete").Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	_, err = ec2Client.DescribeInternetGateways(context.Background(), &ec2sdk.DescribeInternetGatewaysInput{
		InternetGatewayIds: []string{out.InternetGatewayId},
	})
	require.Error(t, err)
}

func TestIGWReconcile_ReattachesDetachedIGW(t *testing.T) {
	client, ec2Client := setupIGWIntegrationDriver(t)
	vpcID := createIntegrationVPC(t, ec2Client)
	name := uniqueIGWName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	out, err := ingress.Object[igw.IGWSpec, igw.IGWOutputs](client, igw.ServiceName, key, "Provision").Request(t.Context(), igw.IGWSpec{
		Account:    integrationAccountName,
		Region:     "us-east-1",
		VpcId:      vpcID,
		ManagedKey: key,
		Tags:       map[string]string{"Name": name, "env": "managed"},
	})
	require.NoError(t, err)

	_, err = ec2Client.DetachInternetGateway(context.Background(), &ec2sdk.DetachInternetGatewayInput{
		InternetGatewayId: aws.String(out.InternetGatewayId),
		VpcId:             aws.String(vpcID),
	})
	require.NoError(t, err)

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, igw.ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)

	desc, err := ec2Client.DescribeInternetGateways(context.Background(), &ec2sdk.DescribeInternetGatewaysInput{InternetGatewayIds: []string{out.InternetGatewayId}})
	require.NoError(t, err)
	require.Len(t, desc.InternetGateways, 1)
	assert.Equal(t, vpcID, aws.ToString(desc.InternetGateways[0].Attachments[0].VpcId))
}

func TestIGWReconcile_TagDrift(t *testing.T) {
	client, ec2Client := setupIGWIntegrationDriver(t)
	vpcID := createIntegrationVPC(t, ec2Client)
	name := uniqueIGWName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	out, err := ingress.Object[igw.IGWSpec, igw.IGWOutputs](client, igw.ServiceName, key, "Provision").Request(t.Context(), igw.IGWSpec{
		Account:    integrationAccountName,
		Region:     "us-east-1",
		VpcId:      vpcID,
		ManagedKey: key,
		Tags:       map[string]string{"Name": name, "env": "managed"},
	})
	require.NoError(t, err)

	_, err = ec2Client.DeleteTags(context.Background(), &ec2sdk.DeleteTagsInput{
		Resources: []string{out.InternetGatewayId},
		Tags:      []ec2types.Tag{{Key: aws.String("env")}},
	})
	require.NoError(t, err)

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, igw.ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)

	desc, err := ec2Client.DescribeInternetGateways(context.Background(), &ec2sdk.DescribeInternetGatewaysInput{InternetGatewayIds: []string{out.InternetGatewayId}})
	require.NoError(t, err)
	require.Len(t, desc.InternetGateways, 1)
	assert.Contains(t, desc.InternetGateways[0].Tags, ec2types.Tag{Key: aws.String("env"), Value: aws.String("managed")})
}

func TestIGWGetStatus_ReturnsReady(t *testing.T) {
	client, ec2Client := setupIGWIntegrationDriver(t)
	vpcID := createIntegrationVPC(t, ec2Client)
	name := uniqueIGWName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	_, err := ingress.Object[igw.IGWSpec, igw.IGWOutputs](client, igw.ServiceName, key, "Provision").Request(t.Context(), igw.IGWSpec{
		Account:    integrationAccountName,
		Region:     "us-east-1",
		VpcId:      vpcID,
		ManagedKey: key,
		Tags:       map[string]string{"Name": name},
	})
	require.NoError(t, err)

	status, err := ingress.Object[restate.Void, types.StatusResponse](client, igw.ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeManaged, status.Mode)
	assert.Greater(t, status.Generation, int64(0))
}
