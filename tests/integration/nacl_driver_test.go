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

	"github.com/praxiscloud/praxis/internal/drivers/nacl"
	"github.com/praxiscloud/praxis/internal/infra/awsclient"
	"github.com/praxiscloud/praxis/pkg/types"
)

func uniqueNACLName(t *testing.T) string {
	t.Helper()
	name := strings.ReplaceAll(t.Name(), "/", "-")
	name = strings.ReplaceAll(name, "_", "-")
	if len(name) > 50 {
		name = name[:50]
	}
	return fmt.Sprintf("%s-%d", name, time.Now().UnixNano()%100000)
}

func setupNACLDriver(t *testing.T) (*ingress.Client, *ec2sdk.Client) {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := localstackAWSConfig(t)
	ec2Client := awsclient.NewEC2Client(awsCfg)
	driver := nacl.NewNetworkACLDriver(nil)

	env := restatetest.Start(t, restate.Reflect(driver))
	return env.Ingress(), ec2Client
}

func createTestVPC(t *testing.T, ec2Client *ec2sdk.Client, cidr string) string {
	t.Helper()
	out, err := ec2Client.CreateVpc(context.Background(), &ec2sdk.CreateVpcInput{CidrBlock: aws.String(cidr)})
	require.NoError(t, err)
	return aws.ToString(out.Vpc.VpcId)
}

func createTestSubnet(t *testing.T, ec2Client *ec2sdk.Client, vpcID string, cidr string) string {
	t.Helper()
	out, err := ec2Client.CreateSubnet(context.Background(), &ec2sdk.CreateSubnetInput{VpcId: aws.String(vpcID), CidrBlock: aws.String(cidr)})
	require.NoError(t, err)
	return aws.ToString(out.Subnet.SubnetId)
}

func defaultNetworkACLID(t *testing.T, ec2Client *ec2sdk.Client, vpcID string) string {
	t.Helper()
	out, err := ec2Client.DescribeNetworkAcls(context.Background(), &ec2sdk.DescribeNetworkAclsInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("vpc-id"), Values: []string{vpcID}},
			{Name: aws.String("default"), Values: []string{"true"}},
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, out.NetworkAcls)
	return aws.ToString(out.NetworkAcls[0].NetworkAclId)
}

func TestNACLProvision_CreatesNetworkACL(t *testing.T) {
	client, ec2Client := setupNACLDriver(t)
	vpcID := createTestVPC(t, ec2Client, "10.20.0.0/16")
	name := uniqueNACLName(t)
	key := fmt.Sprintf("%s~%s", vpcID, name)

	outputs, err := ingress.Object[nacl.NetworkACLSpec, nacl.NetworkACLOutputs](client, nacl.ServiceName, key, "Provision").Request(t.Context(), nacl.NetworkACLSpec{
		Account:      integrationAccountName,
		Region:       "us-east-1",
		VpcId:        vpcID,
		IngressRules: []nacl.NetworkACLRule{{RuleNumber: 100, Protocol: "tcp", RuleAction: "allow", CidrBlock: "0.0.0.0/0", FromPort: 80, ToPort: 80}},
		EgressRules:  []nacl.NetworkACLRule{{RuleNumber: 100, Protocol: "-1", RuleAction: "allow", CidrBlock: "0.0.0.0/0"}},
		Tags:         map[string]string{"Name": name, "env": "test"},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, outputs.NetworkAclId)

	desc, err := ec2Client.DescribeNetworkAcls(context.Background(), &ec2sdk.DescribeNetworkAclsInput{NetworkAclIds: []string{outputs.NetworkAclId}})
	require.NoError(t, err)
	require.Len(t, desc.NetworkAcls, 1)
	assert.Equal(t, vpcID, aws.ToString(desc.NetworkAcls[0].VpcId))
}

func TestNACLProvision_WithAssociation(t *testing.T) {
	client, ec2Client := setupNACLDriver(t)
	vpcID := createTestVPC(t, ec2Client, "10.21.0.0/16")
	subnetID := createTestSubnet(t, ec2Client, vpcID, "10.21.1.0/24")
	name := uniqueNACLName(t)
	key := fmt.Sprintf("%s~%s", vpcID, name)

	outputs, err := ingress.Object[nacl.NetworkACLSpec, nacl.NetworkACLOutputs](client, nacl.ServiceName, key, "Provision").Request(t.Context(), nacl.NetworkACLSpec{
		Account:            integrationAccountName,
		Region:             "us-east-1",
		VpcId:              vpcID,
		SubnetAssociations: []string{subnetID},
		Tags:               map[string]string{"Name": name},
	})
	require.NoError(t, err)
	require.Len(t, outputs.Associations, 1)
	assert.Equal(t, subnetID, outputs.Associations[0].SubnetId)
}

func TestNACLImport_DefaultNetworkACL(t *testing.T) {
	client, ec2Client := setupNACLDriver(t)
	vpcID := createTestVPC(t, ec2Client, "10.22.0.0/16")
	defaultACLID := defaultNetworkACLID(t, ec2Client, vpcID)
	key := fmt.Sprintf("us-east-1~%s", defaultACLID)

	outputs, err := ingress.Object[types.ImportRef, nacl.NetworkACLOutputs](client, nacl.ServiceName, key, "Import").Request(t.Context(), types.ImportRef{ResourceID: defaultACLID, Account: integrationAccountName})
	require.NoError(t, err)
	assert.True(t, outputs.IsDefault)

	status, err := ingress.Object[restate.Void, types.StatusResponse](client, nacl.ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, status.Mode)
}

func TestNACLDelete_DisassociatesAndDeletes(t *testing.T) {
	client, ec2Client := setupNACLDriver(t)
	vpcID := createTestVPC(t, ec2Client, "10.23.0.0/16")
	subnetID := createTestSubnet(t, ec2Client, vpcID, "10.23.1.0/24")
	name := uniqueNACLName(t)
	key := fmt.Sprintf("%s~%s", vpcID, name)

	outputs, err := ingress.Object[nacl.NetworkACLSpec, nacl.NetworkACLOutputs](client, nacl.ServiceName, key, "Provision").Request(t.Context(), nacl.NetworkACLSpec{
		Account:            integrationAccountName,
		Region:             "us-east-1",
		VpcId:              vpcID,
		SubnetAssociations: []string{subnetID},
		Tags:               map[string]string{"Name": name},
	})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](client, nacl.ServiceName, key, "Delete").Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	_, err = ec2Client.DescribeNetworkAcls(context.Background(), &ec2sdk.DescribeNetworkAclsInput{NetworkAclIds: []string{outputs.NetworkAclId}})
	require.Error(t, err)

	defaultACLID := defaultNetworkACLID(t, ec2Client, vpcID)
	desc, err := ec2Client.DescribeNetworkAcls(context.Background(), &ec2sdk.DescribeNetworkAclsInput{NetworkAclIds: []string{defaultACLID}})
	require.NoError(t, err)
	require.Len(t, desc.NetworkAcls, 1)
	associated := false
	for _, association := range desc.NetworkAcls[0].Associations {
		if aws.ToString(association.SubnetId) == subnetID {
			associated = true
			break
		}
	}
	assert.True(t, associated)
}

func TestNACLReconcile_RuleDrift(t *testing.T) {
	client, ec2Client := setupNACLDriver(t)
	vpcID := createTestVPC(t, ec2Client, "10.24.0.0/16")
	name := uniqueNACLName(t)
	key := fmt.Sprintf("%s~%s", vpcID, name)

	outputs, err := ingress.Object[nacl.NetworkACLSpec, nacl.NetworkACLOutputs](client, nacl.ServiceName, key, "Provision").Request(t.Context(), nacl.NetworkACLSpec{
		Account:      integrationAccountName,
		Region:       "us-east-1",
		VpcId:        vpcID,
		IngressRules: []nacl.NetworkACLRule{{RuleNumber: 100, Protocol: "tcp", RuleAction: "allow", CidrBlock: "0.0.0.0/0", FromPort: 80, ToPort: 80}},
		Tags:         map[string]string{"Name": name},
	})
	require.NoError(t, err)

	_, err = ec2Client.CreateNetworkAclEntry(context.Background(), &ec2sdk.CreateNetworkAclEntryInput{
		NetworkAclId: aws.String(outputs.NetworkAclId),
		RuleNumber:   aws.Int32(200),
		Protocol:     aws.String("6"),
		RuleAction:   ec2types.RuleActionAllow,
		Egress:       aws.Bool(false),
		CidrBlock:    aws.String("0.0.0.0/0"),
		PortRange:    &ec2types.PortRange{From: aws.Int32(22), To: aws.Int32(22)},
	})
	require.NoError(t, err)

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, nacl.ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)

	desc, err := ec2Client.DescribeNetworkAcls(context.Background(), &ec2sdk.DescribeNetworkAclsInput{NetworkAclIds: []string{outputs.NetworkAclId}})
	require.NoError(t, err)
	require.Len(t, desc.NetworkAcls, 1)
	for _, entry := range desc.NetworkAcls[0].Entries {
		if aws.ToInt32(entry.RuleNumber) == 200 {
			t.Fatal("drift rule 200 should have been removed during reconcile")
		}
	}
}

func TestNACLProvision_Idempotent(t *testing.T) {
	client, ec2Client := setupNACLDriver(t)
	vpcID := createTestVPC(t, ec2Client, "10.25.0.0/16")
	name := uniqueNACLName(t)
	key := fmt.Sprintf("%s~%s", vpcID, name)

	spec := nacl.NetworkACLSpec{
		Account:      integrationAccountName,
		Region:       "us-east-1",
		VpcId:        vpcID,
		IngressRules: []nacl.NetworkACLRule{{RuleNumber: 100, Protocol: "tcp", RuleAction: "allow", CidrBlock: "0.0.0.0/0", FromPort: 443, ToPort: 443}},
		Tags:         map[string]string{"Name": name},
	}

	out1, err := ingress.Object[nacl.NetworkACLSpec, nacl.NetworkACLOutputs](client, nacl.ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	assert.NotEmpty(t, out1.NetworkAclId)

	out2, err := ingress.Object[nacl.NetworkACLSpec, nacl.NetworkACLOutputs](client, nacl.ServiceName, key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	assert.Equal(t, out1.NetworkAclId, out2.NetworkAclId, "re-provision should reuse same NACL")
}

func TestNACLProvision_RuleConvergence(t *testing.T) {
	client, ec2Client := setupNACLDriver(t)
	vpcID := createTestVPC(t, ec2Client, "10.26.0.0/16")
	name := uniqueNACLName(t)
	key := fmt.Sprintf("%s~%s", vpcID, name)

	// Initial provision with two ingress rules
	out1, err := ingress.Object[nacl.NetworkACLSpec, nacl.NetworkACLOutputs](client, nacl.ServiceName, key, "Provision").Request(t.Context(), nacl.NetworkACLSpec{
		Account: integrationAccountName,
		Region:  "us-east-1",
		VpcId:   vpcID,
		IngressRules: []nacl.NetworkACLRule{
			{RuleNumber: 100, Protocol: "tcp", RuleAction: "allow", CidrBlock: "0.0.0.0/0", FromPort: 80, ToPort: 80},
			{RuleNumber: 200, Protocol: "tcp", RuleAction: "allow", CidrBlock: "0.0.0.0/0", FromPort: 22, ToPort: 22},
		},
		Tags: map[string]string{"Name": name},
	})
	require.NoError(t, err)

	// Re-provision: remove rule 200, add rule 300, change rule 100's port
	_, err = ingress.Object[nacl.NetworkACLSpec, nacl.NetworkACLOutputs](client, nacl.ServiceName, key, "Provision").Request(t.Context(), nacl.NetworkACLSpec{
		Account: integrationAccountName,
		Region:  "us-east-1",
		VpcId:   vpcID,
		IngressRules: []nacl.NetworkACLRule{
			{RuleNumber: 100, Protocol: "tcp", RuleAction: "allow", CidrBlock: "0.0.0.0/0", FromPort: 443, ToPort: 443},
			{RuleNumber: 300, Protocol: "tcp", RuleAction: "allow", CidrBlock: "10.0.0.0/8", FromPort: 8080, ToPort: 8080},
		},
		Tags: map[string]string{"Name": name},
	})
	require.NoError(t, err)

	// Verify via AWS SDK
	desc, err := ec2Client.DescribeNetworkAcls(context.Background(), &ec2sdk.DescribeNetworkAclsInput{NetworkAclIds: []string{out1.NetworkAclId}})
	require.NoError(t, err)
	require.Len(t, desc.NetworkAcls, 1)

	ingressRules := map[int32]bool{}
	for _, entry := range desc.NetworkAcls[0].Entries {
		if !aws.ToBool(entry.Egress) && aws.ToInt32(entry.RuleNumber) != 32767 {
			ingressRules[aws.ToInt32(entry.RuleNumber)] = true
			if aws.ToInt32(entry.RuleNumber) == 100 {
				assert.Equal(t, int32(443), aws.ToInt32(entry.PortRange.From), "rule 100 should have been updated to port 443")
			}
		}
	}
	assert.True(t, ingressRules[100], "rule 100 should exist")
	assert.False(t, ingressRules[200], "rule 200 should have been removed")
	assert.True(t, ingressRules[300], "rule 300 should have been added")
}

func TestNACLImport_Existing(t *testing.T) {
	client, ec2Client := setupNACLDriver(t)
	vpcID := createTestVPC(t, ec2Client, "10.27.0.0/16")

	// Create NACL directly via AWS SDK
	createOut, err := ec2Client.CreateNetworkAcl(context.Background(), &ec2sdk.CreateNetworkAclInput{
		VpcId: aws.String(vpcID),
	})
	require.NoError(t, err)
	naclID := aws.ToString(createOut.NetworkAcl.NetworkAclId)

	// Import via driver
	key := fmt.Sprintf("us-east-1~%s", naclID)
	outputs, err := ingress.Object[types.ImportRef, nacl.NetworkACLOutputs](client, nacl.ServiceName, key, "Import").Request(t.Context(), types.ImportRef{
		ResourceID: naclID,
		Account:    integrationAccountName,
	})
	require.NoError(t, err)
	assert.Equal(t, naclID, outputs.NetworkAclId)
	assert.Equal(t, vpcID, outputs.VpcId)
	assert.False(t, outputs.IsDefault)
}

func TestNACLDelete_DefaultBlocked(t *testing.T) {
	client, ec2Client := setupNACLDriver(t)
	vpcID := createTestVPC(t, ec2Client, "10.28.0.0/16")
	defaultACLID := defaultNetworkACLID(t, ec2Client, vpcID)
	key := fmt.Sprintf("us-east-1~%s", defaultACLID)

	// Import the default NACL
	_, err := ingress.Object[types.ImportRef, nacl.NetworkACLOutputs](client, nacl.ServiceName, key, "Import").Request(t.Context(), types.ImportRef{
		ResourceID: defaultACLID,
		Account:    integrationAccountName,
	})
	require.NoError(t, err)

	// Attempt to delete — should be blocked (Observed mode)
	_, err = ingress.Object[restate.Void, restate.Void](client, nacl.ServiceName, key, "Delete").Request(t.Context(), restate.Void{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Observed mode")
}

func TestNACLReconcile_AssociationDrift(t *testing.T) {
	client, ec2Client := setupNACLDriver(t)
	vpcID := createTestVPC(t, ec2Client, "10.29.0.0/16")
	subnetID := createTestSubnet(t, ec2Client, vpcID, "10.29.1.0/24")
	name := uniqueNACLName(t)
	key := fmt.Sprintf("%s~%s", vpcID, name)

	// Provision with subnet association
	outputs, err := ingress.Object[nacl.NetworkACLSpec, nacl.NetworkACLOutputs](client, nacl.ServiceName, key, "Provision").Request(t.Context(), nacl.NetworkACLSpec{
		Account:            integrationAccountName,
		Region:             "us-east-1",
		VpcId:              vpcID,
		SubnetAssociations: []string{subnetID},
		Tags:               map[string]string{"Name": name},
	})
	require.NoError(t, err)
	require.Len(t, outputs.Associations, 1)

	// Introduce drift: reassociate subnet to default NACL via AWS SDK
	defaultACLID := defaultNetworkACLID(t, ec2Client, vpcID)
	_, err = ec2Client.ReplaceNetworkAclAssociation(context.Background(), &ec2sdk.ReplaceNetworkAclAssociationInput{
		AssociationId: aws.String(outputs.Associations[0].AssociationId),
		NetworkAclId:  aws.String(defaultACLID),
	})
	require.NoError(t, err)

	// Reconcile should detect and correct drift
	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, nacl.ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)

	// Verify subnet is re-associated with our NACL
	desc, err := ec2Client.DescribeNetworkAcls(context.Background(), &ec2sdk.DescribeNetworkAclsInput{NetworkAclIds: []string{outputs.NetworkAclId}})
	require.NoError(t, err)
	require.Len(t, desc.NetworkAcls, 1)
	associated := false
	for _, assoc := range desc.NetworkAcls[0].Associations {
		if aws.ToString(assoc.SubnetId) == subnetID {
			associated = true
			break
		}
	}
	assert.True(t, associated, "subnet should be re-associated after reconcile")
}

func TestNACLGetStatus_ReturnsReady(t *testing.T) {
	client, ec2Client := setupNACLDriver(t)
	vpcID := createTestVPC(t, ec2Client, "10.30.0.0/16")
	name := uniqueNACLName(t)
	key := fmt.Sprintf("%s~%s", vpcID, name)

	_, err := ingress.Object[nacl.NetworkACLSpec, nacl.NetworkACLOutputs](client, nacl.ServiceName, key, "Provision").Request(t.Context(), nacl.NetworkACLSpec{
		Account: integrationAccountName,
		Region:  "us-east-1",
		VpcId:   vpcID,
		Tags:    map[string]string{"Name": name},
	})
	require.NoError(t, err)

	status, err := ingress.Object[restate.Void, types.StatusResponse](client, nacl.ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeManaged, status.Mode)
	assert.Greater(t, status.Generation, int64(0))
}
