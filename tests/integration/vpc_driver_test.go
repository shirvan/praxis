//go:build integration

package integration

import (
	"context"
	"fmt"
	"path/filepath"
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

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/core/orchestrator"
	"github.com/shirvan/praxis/internal/drivers/vpc"
	"github.com/shirvan/praxis/internal/eventing"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
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
	authClient := authservice.NewAuthClient()
	driver := vpc.NewVPCDriver(authClient)
	absSchemaDir, err := filepath.Abs("../../schemas")
	require.NoError(t, err)

	env := restatetest.Start(t,
		restate.Reflect(authservice.NewAuthService(authservice.LoadBootstrapFromEnv())),
		restate.Reflect(driver),
		restate.Reflect(orchestrator.NewEventBus(absSchemaDir)),
		restate.Reflect(orchestrator.DeploymentEventStore{}),
		restate.Reflect(orchestrator.EventIndex{}),
		restate.Reflect(orchestrator.ResourceEventOwnerObj{}),
		restate.Reflect(orchestrator.ResourceEventBridge{}),
		restate.Reflect(orchestrator.SinkRouter{}),
		restate.Reflect(orchestrator.NewNotificationSinkConfig(absSchemaDir)),
	)
	return env.Ingress(), ec2Client
}

func registerVPCEventOwner(t *testing.T, client *ingress.Client, resourceKey, streamKey, resourceName string) {
	t.Helper()
	_, err := ingress.Object[eventing.ResourceEventOwner, restate.Void](
		client,
		eventing.ResourceEventOwnerServiceName,
		resourceKey,
		"Upsert",
	).Request(t.Context(), eventing.ResourceEventOwner{
		StreamKey:    streamKey,
		Workspace:    "integration",
		Generation:   1,
		ResourceName: resourceName,
		ResourceKind: vpc.ServiceName,
	})
	require.NoError(t, err)
}

func pollEventTypes(t *testing.T, client *ingress.Client, streamKey string, expected ...string) []string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		records, err := ingress.Object[int64, []orchestrator.SequencedCloudEvent](
			client,
			orchestrator.DeploymentEventStoreServiceName,
			streamKey,
			"ListSince",
		).Request(t.Context(), 0)
		require.NoError(t, err)
		typesSeen := make([]string, 0, len(records))
		seen := make(map[string]bool, len(records))
		for _, record := range records {
			typesSeen = append(typesSeen, record.Event.Type())
			seen[record.Event.Type()] = true
		}
		complete := true
		for _, want := range expected {
			if !seen[want] {
				complete = false
				break
			}
		}
		if complete || time.Now().After(deadline) {
			return typesSeen
		}
		time.Sleep(200 * time.Millisecond)
	}
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

	// Verify VPC exists in Moto
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

	// Create VPC directly in Moto
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
	require.Error(t, err, "VPC should be deleted from Moto")
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
	streamKey := "dep-vpc-drift-" + name
	registerVPCEventOwner(t, client, key, streamKey, name)

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
	eventTypes := pollEventTypes(t, client, streamKey, orchestrator.EventTypeDriftDetected, orchestrator.EventTypeDriftCorrected)
	assert.Contains(t, eventTypes, orchestrator.EventTypeDriftDetected)
	assert.Contains(t, eventTypes, orchestrator.EventTypeDriftCorrected)

	// Verify DNS hostnames was restored
	dnsOut, err := ec2Client.DescribeVpcAttribute(context.Background(), &ec2sdk.DescribeVpcAttributeInput{
		VpcId:     aws.String(outputs.VpcId),
		Attribute: ec2types.VpcAttributeNameEnableDnsHostnames,
	})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(dnsOut.EnableDnsHostnames.Value), "DNS hostnames should be restored")
}

func TestVPCReconcile_EmitsExternalDeleteEvent(t *testing.T) {
	client, ec2Client := setupVPCDriver(t)
	name := uniqueVpcName(t)
	cidr := uniqueCidr()
	key := fmt.Sprintf("us-east-1~%s", name)
	streamKey := "dep-vpc-external-delete-" + name
	registerVPCEventOwner(t, client, key, streamKey, name)

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

	_, err = ec2Client.DeleteVpc(context.Background(), &ec2sdk.DeleteVpcInput{VpcId: aws.String(outputs.VpcId)})
	require.NoError(t, err)

	result, err := ingress.Object[restate.Void, types.ReconcileResult](
		client, "VPC", key, "Reconcile",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Contains(t, result.Error, "deleted externally")

	eventTypes := pollEventTypes(t, client, streamKey, orchestrator.EventTypeDriftExternalDelete)
	assert.Contains(t, eventTypes, orchestrator.EventTypeDriftExternalDelete)

	records, err := ingress.Object[string, []orchestrator.SequencedCloudEvent](
		client,
		orchestrator.DeploymentEventStoreServiceName,
		streamKey,
		"ListByType",
	).Request(t.Context(), orchestrator.EventTypeDriftExternalDelete)
	require.NoError(t, err)
	require.NotEmpty(t, records)
	assert.Equal(t, name, records[0].Event.Subject())
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
