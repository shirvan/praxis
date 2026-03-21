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

	"github.com/praxiscloud/praxis/internal/drivers/eip"
	"github.com/praxiscloud/praxis/internal/infra/awsclient"
	"github.com/praxiscloud/praxis/pkg/types"
)

func uniqueEIPName(t *testing.T) string {
	t.Helper()
	name := strings.ReplaceAll(t.Name(), "/", "-")
	name = strings.ReplaceAll(name, "_", "-")
	if len(name) > 50 {
		name = name[:50]
	}
	return fmt.Sprintf("%s-%d", name, time.Now().UnixNano()%1000000000)
}

func setupEIPDriver(t *testing.T) (*ingress.Client, *ec2sdk.Client) {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := localstackAWSConfig(t)
	ec2Client := awsclient.NewEC2Client(awsCfg)
	driver := eip.NewElasticIPDriver(nil)

	env := restatetest.Start(t, restate.Reflect(driver))
	return env.Ingress(), ec2Client
}

func TestEIPProvision_AllocatesAddress(t *testing.T) {
	client, ec2Client := setupEIPDriver(t)
	name := uniqueEIPName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	outputs, err := ingress.Object[eip.ElasticIPSpec, eip.ElasticIPOutputs](client, "ElasticIP", key, "Provision").Request(t.Context(), eip.ElasticIPSpec{
		Account:    integrationAccountName,
		Region:     "us-east-1",
		ManagedKey: key,
		Tags:       map[string]string{"Name": name, "env": "test"},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, outputs.AllocationId)
	assert.NotEmpty(t, outputs.PublicIp)

	desc, err := ec2Client.DescribeAddresses(context.Background(), &ec2sdk.DescribeAddressesInput{AllocationIds: []string{outputs.AllocationId}})
	require.NoError(t, err)
	require.Len(t, desc.Addresses, 1)
	assert.Equal(t, outputs.AllocationId, aws.ToString(desc.Addresses[0].AllocationId))
}

func TestEIPProvision_Idempotent(t *testing.T) {
	client, _ := setupEIPDriver(t)
	name := uniqueEIPName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	spec := eip.ElasticIPSpec{
		Account:    integrationAccountName,
		Region:     "us-east-1",
		ManagedKey: key,
		Tags:       map[string]string{"Name": name},
	}

	out1, err := ingress.Object[eip.ElasticIPSpec, eip.ElasticIPOutputs](client, "ElasticIP", key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	out2, err := ingress.Object[eip.ElasticIPSpec, eip.ElasticIPOutputs](client, "ElasticIP", key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	assert.Equal(t, out1.AllocationId, out2.AllocationId)
}

func TestEIPImport_ExistingAllocation(t *testing.T) {
	client, ec2Client := setupEIPDriver(t)
	name := uniqueEIPName(t)

	createOut, err := ec2Client.AllocateAddress(context.Background(), &ec2sdk.AllocateAddressInput{
		Domain: ec2types.DomainTypeVpc,
	})
	require.NoError(t, err)
	allocationID := aws.ToString(createOut.AllocationId)

	_, err = ec2Client.CreateTags(context.Background(), &ec2sdk.CreateTagsInput{
		Resources: []string{allocationID},
		Tags:      []ec2types.Tag{{Key: aws.String("Name"), Value: aws.String(name)}},
	})
	require.NoError(t, err)

	key := fmt.Sprintf("us-east-1~%s", allocationID)
	outputs, err := ingress.Object[types.ImportRef, eip.ElasticIPOutputs](client, "ElasticIP", key, "Import").Request(t.Context(), types.ImportRef{
		ResourceID: allocationID,
		Account:    integrationAccountName,
	})
	require.NoError(t, err)
	assert.Equal(t, allocationID, outputs.AllocationId)

	status, err := ingress.Object[restate.Void, types.StatusResponse](client, "ElasticIP", key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, status.Mode)
}

func TestEIPDelete_ReleasesAddress(t *testing.T) {
	client, ec2Client := setupEIPDriver(t)
	name := uniqueEIPName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	out, err := ingress.Object[eip.ElasticIPSpec, eip.ElasticIPOutputs](client, "ElasticIP", key, "Provision").Request(t.Context(), eip.ElasticIPSpec{
		Account:    integrationAccountName,
		Region:     "us-east-1",
		ManagedKey: key,
		Tags:       map[string]string{"Name": name},
	})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](client, "ElasticIP", key, "Delete").Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	_, err = ec2Client.DescribeAddresses(context.Background(), &ec2sdk.DescribeAddressesInput{AllocationIds: []string{out.AllocationId}})
	require.Error(t, err, "allocation should be deleted from LocalStack")
}

func TestEIPReconcile_DetectsAndFixesTagDrift(t *testing.T) {
	client, ec2Client := setupEIPDriver(t)
	name := uniqueEIPName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	out, err := ingress.Object[eip.ElasticIPSpec, eip.ElasticIPOutputs](client, "ElasticIP", key, "Provision").Request(t.Context(), eip.ElasticIPSpec{
		Account:    integrationAccountName,
		Region:     "us-east-1",
		ManagedKey: key,
		Tags:       map[string]string{"Name": name, "env": "managed"},
	})
	require.NoError(t, err)

	_, err = ec2Client.DeleteTags(context.Background(), &ec2sdk.DeleteTagsInput{
		Resources: []string{out.AllocationId},
		Tags:      []ec2types.Tag{{Key: aws.String("env")}},
	})
	require.NoError(t, err)

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, "ElasticIP", key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)

	desc, err := ec2Client.DescribeAddresses(context.Background(), &ec2sdk.DescribeAddressesInput{AllocationIds: []string{out.AllocationId}})
	require.NoError(t, err)
	require.Len(t, desc.Addresses, 1)
	assert.Contains(t, desc.Addresses[0].Tags, ec2types.Tag{Key: aws.String("env"), Value: aws.String("managed")})
}

func TestEIPGetStatus_ReturnsReady(t *testing.T) {
	client, _ := setupEIPDriver(t)
	name := uniqueEIPName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	_, err := ingress.Object[eip.ElasticIPSpec, eip.ElasticIPOutputs](client, "ElasticIP", key, "Provision").Request(t.Context(), eip.ElasticIPSpec{
		Account:    integrationAccountName,
		Region:     "us-east-1",
		ManagedKey: key,
		Tags:       map[string]string{"Name": name},
	})
	require.NoError(t, err)

	status, err := ingress.Object[restate.Void, types.StatusResponse](client, "ElasticIP", key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeManaged, status.Mode)
	assert.Greater(t, status.Generation, int64(0))
}
