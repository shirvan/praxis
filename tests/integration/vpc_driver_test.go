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

	"github.com/praxiscloud/praxis/internal/drivers/vpc"
	"github.com/praxiscloud/praxis/internal/infra/awsclient"
	"github.com/praxiscloud/praxis/pkg/types"
)

func uniqueVpcName(t *testing.T) string {
	t.Helper()
	name := strings.ReplaceAll(t.Name(), "/", "-")
	name = strings.ReplaceAll(name, "_", "-")
	if len(name) > 50 {
		name = name[:50]
	}
	return fmt.Sprintf("%s-%d", name, time.Now().UnixNano()%100000)
}

// uniqueCidr returns a unique /24 CIDR block to avoid collisions across tests.
var cidrCounter int

func uniqueCidr() string {
	cidrCounter++
	// Use 10.X.Y.0/24 pattern with different second and third octets
	return fmt.Sprintf("10.%d.%d.0/24", cidrCounter/256, cidrCounter%256)
}

func setupVPCDriver(t *testing.T) (*ingress.Client, *ec2sdk.Client) {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := localstackAWSConfig(t)
	ec2Client := awsclient.NewEC2Client(awsCfg)
	driver := vpc.NewVPCDriver(nil)

	env := restatetest.Start(t, restate.Reflect(driver))
	return env.Ingress(), ec2Client
}

func TestVPCProvision_CreatesVPC(t *testing.T) {
	client, ec2Client := setupVPCDriver(t)
	name := uniqueVpcName(t)
	cidr := uniqueCidr()
	key := fmt.Sprintf("us-east-1~%s", name)

	outputs, err := ingress.Object[vpc.VPCSpec, vpc.VPCOutputs](
		client, "VPC", key, "Provision",
	).Request(t.Context(), vpc.VPCSpec{
		Account:            integrationAccountName,
		Region:             "us-east-1",
		CidrBlock:          cidr,
		EnableDnsHostnames: true,
		EnableDnsSupport:   true,
		InstanceTenancy:    "default",
		ManagedKey:         key,
		Tags:               map[string]string{"Name": name, "env": "test"},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, outputs.VpcId)
	assert.Equal(t, cidr, outputs.CidrBlock)
	assert.Equal(t, "available", outputs.State)
	assert.True(t, outputs.EnableDnsHostnames)
	assert.True(t, outputs.EnableDnsSupport)

	// Verify VPC exists in LocalStack
	desc, err := ec2Client.DescribeVpcs(context.Background(), &ec2sdk.DescribeVpcsInput{
		VpcIds: []string{outputs.VpcId},
	})
	require.NoError(t, err)
	require.Len(t, desc.Vpcs, 1)
	assert.Equal(t, cidr, aws.ToString(desc.Vpcs[0].CidrBlock))
}

func TestVPCProvision_Idempotent(t *testing.T) {
	client, _ := setupVPCDriver(t)
	name := uniqueVpcName(t)
	cidr := uniqueCidr()
	key := fmt.Sprintf("us-east-1~%s", name)

	spec := vpc.VPCSpec{
		Account:            integrationAccountName,
		Region:             "us-east-1",
		CidrBlock:          cidr,
		EnableDnsHostnames: false,
		EnableDnsSupport:   true,
		ManagedKey:         key,
		Tags:               map[string]string{"Name": name},
	}

	out1, err := ingress.Object[vpc.VPCSpec, vpc.VPCOutputs](
		client, "VPC", key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)
	assert.NotEmpty(t, out1.VpcId)

	out2, err := ingress.Object[vpc.VPCSpec, vpc.VPCOutputs](
		client, "VPC", key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)
	assert.Equal(t, out1.VpcId, out2.VpcId, "re-provision should reuse same VPC")
}

func TestVPCImport_ExistingVPC(t *testing.T) {
	client, ec2Client := setupVPCDriver(t)
	cidr := uniqueCidr()

	// Create VPC directly in LocalStack
	createOut, err := ec2Client.CreateVpc(context.Background(), &ec2sdk.CreateVpcInput{
		CidrBlock: aws.String(cidr),
	})
	require.NoError(t, err)
	vpcId := aws.ToString(createOut.Vpc.VpcId)

	key := fmt.Sprintf("us-east-1~%s", vpcId)
	outputs, err := ingress.Object[types.ImportRef, vpc.VPCOutputs](
		client, "VPC", key, "Import",
	).Request(t.Context(), types.ImportRef{
		ResourceID: vpcId,
		Account:    integrationAccountName,
	})
	require.NoError(t, err)
	assert.Equal(t, vpcId, outputs.VpcId)
	assert.Equal(t, cidr, outputs.CidrBlock)

	// Verify imported as Observed mode
	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, "VPC", key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, status.Mode)
}

func TestVPCDelete_ObservedModeBlocked(t *testing.T) {
	client, ec2Client := setupVPCDriver(t)
	cidr := uniqueCidr()

	createOut, err := ec2Client.CreateVpc(context.Background(), &ec2sdk.CreateVpcInput{
		CidrBlock: aws.String(cidr),
	})
	require.NoError(t, err)
	vpcId := aws.ToString(createOut.Vpc.VpcId)
	key := fmt.Sprintf("us-east-1~%s", vpcId)

	_, err = ingress.Object[types.ImportRef, vpc.VPCOutputs](
		client, "VPC", key, "Import",
	).Request(t.Context(), types.ImportRef{ResourceID: vpcId, Account: integrationAccountName})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](
		client, "VPC", key, "Delete",
	).Request(t.Context(), restate.Void{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Observed mode")
}

func TestVPCDelete_RemovesVPC(t *testing.T) {
	client, ec2Client := setupVPCDriver(t)
	name := uniqueVpcName(t)
	cidr := uniqueCidr()
	key := fmt.Sprintf("us-east-1~%s", name)

	out, err := ingress.Object[vpc.VPCSpec, vpc.VPCOutputs](
		client, "VPC", key, "Provision",
	).Request(t.Context(), vpc.VPCSpec{
		Account:          integrationAccountName,
		Region:           "us-east-1",
		CidrBlock:        cidr,
		EnableDnsSupport: true,
		ManagedKey:       key,
		Tags:             map[string]string{"Name": name},
	})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](
		client, "VPC", key, "Delete",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	// Verify VPC is gone
	_, err = ec2Client.DescribeVpcs(context.Background(), &ec2sdk.DescribeVpcsInput{
		VpcIds: []string{out.VpcId},
	})
	require.Error(t, err, "VPC should be deleted from LocalStack")
}

func TestVPCGetStatus_ReturnsReady(t *testing.T) {
	client, _ := setupVPCDriver(t)
	name := uniqueVpcName(t)
	cidr := uniqueCidr()
	key := fmt.Sprintf("us-east-1~%s", name)

	_, err := ingress.Object[vpc.VPCSpec, vpc.VPCOutputs](
		client, "VPC", key, "Provision",
	).Request(t.Context(), vpc.VPCSpec{
		Account:          integrationAccountName,
		Region:           "us-east-1",
		CidrBlock:        cidr,
		EnableDnsSupport: true,
		ManagedKey:       key,
		Tags:             map[string]string{"Name": name},
	})
	require.NoError(t, err)

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, "VPC", key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeManaged, status.Mode)
	assert.Greater(t, status.Generation, int64(0))
}

func TestVPCReconcile_DetectsDrift(t *testing.T) {
	client, ec2Client := setupVPCDriver(t)
	name := uniqueVpcName(t)
	cidr := uniqueCidr()
	key := fmt.Sprintf("us-east-1~%s", name)

	// Provision with DNS hostnames enabled
	outputs, err := ingress.Object[vpc.VPCSpec, vpc.VPCOutputs](
		client, "VPC", key, "Provision",
	).Request(t.Context(), vpc.VPCSpec{
		Account:            integrationAccountName,
		Region:             "us-east-1",
		CidrBlock:          cidr,
		EnableDnsHostnames: true,
		EnableDnsSupport:   true,
		ManagedKey:         key,
		Tags:               map[string]string{"Name": name},
	})
	require.NoError(t, err)

	// Introduce drift: disable DNS hostnames directly via EC2 API
	_, err = ec2Client.ModifyVpcAttribute(context.Background(), &ec2sdk.ModifyVpcAttributeInput{
		VpcId: aws.String(outputs.VpcId),
		EnableDnsHostnames: &ec2types.AttributeBooleanValue{
			Value: aws.Bool(false),
		},
	})
	require.NoError(t, err)

	// Trigger reconcile
	result, err := ingress.Object[restate.Void, types.ReconcileResult](
		client, "VPC", key, "Reconcile",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift, "drift should be detected")
	assert.True(t, result.Correcting, "managed mode should correct drift")

	// Verify DNS hostnames was restored
	dnsOut, err := ec2Client.DescribeVpcAttribute(context.Background(), &ec2sdk.DescribeVpcAttributeInput{
		VpcId:     aws.String(outputs.VpcId),
		Attribute: ec2types.VpcAttributeNameEnableDnsHostnames,
	})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(dnsOut.EnableDnsHostnames.Value), "DNS hostnames should be restored")
}

func TestVPCProvision_MissingCidrBlock(t *testing.T) {
	client, _ := setupVPCDriver(t)
	name := uniqueVpcName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	_, err := ingress.Object[vpc.VPCSpec, vpc.VPCOutputs](
		client, "VPC", key, "Provision",
	).Request(t.Context(), vpc.VPCSpec{
		Account:          integrationAccountName,
		Region:           "us-east-1",
		CidrBlock:        "",
		EnableDnsSupport: true,
		ManagedKey:       key,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cidrBlock")
}

func TestVPCProvision_InvalidDnsCombination(t *testing.T) {
	client, _ := setupVPCDriver(t)
	name := uniqueVpcName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	_, err := ingress.Object[vpc.VPCSpec, vpc.VPCOutputs](
		client, "VPC", key, "Provision",
	).Request(t.Context(), vpc.VPCSpec{
		Account:            integrationAccountName,
		Region:             "us-east-1",
		CidrBlock:          uniqueCidr(),
		EnableDnsHostnames: true,
		EnableDnsSupport:   false,
		ManagedKey:         key,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "enableDnsHostnames requires enableDnsSupport")
}
