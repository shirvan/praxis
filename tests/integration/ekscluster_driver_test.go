//go:build integration

package integration

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ekssdk "github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	"github.com/shirvan/praxis/internal/core/authservice"

	"github.com/shirvan/praxis/internal/drivers/ekscluster"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// uniqueEKSClusterName derives a Moto-safe, collision-free cluster name from the
// test name plus a nanosecond suffix. Moto keeps no cross-test state guarantees,
// so every test provisions under its own name.
func uniqueEKSClusterName(t *testing.T) string {
	t.Helper()
	name := strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	name = strings.ReplaceAll(name, "_", "-")
	if len(name) > 80 {
		name = name[:80]
	}
	return fmt.Sprintf("%s-%d", name, time.Now().UnixNano()%100000)
}

func setupEKSClusterDriver(t *testing.T) (*ingress.Client, *ekssdk.Client) {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := motoAWSConfig(t)
	eksClient := awsclient.NewEKSClient(awsCfg)
	driver := ekscluster.NewGenericEKSClusterDriver(authservice.NewAuthClient())

	ingressClient := setupDriverEventingEnv(t, driver)
	return ingressClient, eksClient
}

// baseClusterSpec builds a spec that Moto accepts. Version is intentionally
// omitted: Moto does not implement UpdateClusterVersion, and leaving the desired
// version empty means the driver tracks whatever version AWS assigns, so version
// never registers as correctable drift.
func baseClusterSpec(name string) ekscluster.EKSClusterSpec {
	return ekscluster.EKSClusterSpec{
		Account:              integrationAccountName,
		Region:               "us-east-1",
		Name:                 name,
		RoleArn:              "arn:aws:iam::123456789012:role/eks-cluster",
		SubnetIds:            []string{"subnet-11111111", "subnet-22222222"},
		EndpointPublicAccess: true,
		Tags:                 map[string]string{"env": "test"},
	}
}

func provisionCluster(t *testing.T, client *ingress.Client, key string, spec ekscluster.EKSClusterSpec) ekscluster.EKSClusterOutputs {
	t.Helper()
	out, err := ingress.Object[ekscluster.EKSClusterSpec, ekscluster.EKSClusterOutputs](
		client, ekscluster.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)
	return out
}

func TestEKSClusterProvision_CreatesCluster(t *testing.T) {
	client, eksClient := setupEKSClusterDriver(t)
	name := uniqueEKSClusterName(t)
	key := url.PathEscape(fmt.Sprintf("us-east-1~%s", name))

	outputs := provisionCluster(t, client, key, baseClusterSpec(name))
	assert.Equal(t, name, outputs.Name)
	assert.Equal(t, "ACTIVE", outputs.Status)
	assert.NotEmpty(t, outputs.ARN)
	assert.NotEmpty(t, outputs.Version)

	got, err := eksClient.DescribeCluster(context.Background(), &ekssdk.DescribeClusterInput{Name: aws.String(name)})
	require.NoError(t, err)
	assert.Equal(t, "test", got.Cluster.Tags["env"])
	assert.Equal(t, name, aws.ToString(got.Cluster.Name))
	assert.Contains(t, got.Cluster.Tags, "praxis:managed-key", "provisioning should stamp the managed-key marker")
}

func TestEKSClusterProvision_Idempotent(t *testing.T) {
	client, _ := setupEKSClusterDriver(t)
	name := uniqueEKSClusterName(t)
	key := url.PathEscape(fmt.Sprintf("us-east-1~%s", name))
	spec := baseClusterSpec(name)

	out1 := provisionCluster(t, client, key, spec)
	out2 := provisionCluster(t, client, key, spec)
	assert.Equal(t, out1.ARN, out2.ARN, "re-provisioning an in-sync cluster must be a no-op")
}

func TestEKSClusterImport_ExistingCluster(t *testing.T) {
	client, eksClient := setupEKSClusterDriver(t)
	name := uniqueEKSClusterName(t)

	_, err := eksClient.CreateCluster(context.Background(), &ekssdk.CreateClusterInput{
		Name:    aws.String(name),
		RoleArn: aws.String("arn:aws:iam::123456789012:role/eks-cluster"),
		ResourcesVpcConfig: &ekstypes.VpcConfigRequest{
			SubnetIds:            []string{"subnet-11111111", "subnet-22222222"},
			EndpointPublicAccess: aws.Bool(true),
		},
		Tags: map[string]string{"env": "preexisting"},
	})
	require.NoError(t, err)

	key := url.PathEscape(fmt.Sprintf("us-east-1~%s", name))
	outputs, err := ingress.Object[types.ImportRef, ekscluster.EKSClusterOutputs](
		client, ekscluster.ServiceName, key, "Import",
	).Request(t.Context(), types.ImportRef{ResourceID: name, Account: integrationAccountName})
	require.NoError(t, err)
	assert.Equal(t, name, outputs.Name)
	assert.NotEmpty(t, outputs.ARN)

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, ekscluster.ServiceName, key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, status.Mode, "default import mode is Observed")
}

func TestEKSClusterDelete_RemovesCluster(t *testing.T) {
	client, eksClient := setupEKSClusterDriver(t)
	name := uniqueEKSClusterName(t)
	key := url.PathEscape(fmt.Sprintf("us-east-1~%s", name))

	provisionCluster(t, client, key, baseClusterSpec(name))

	_, err := ingress.Object[restate.Void, restate.Void](
		client, ekscluster.ServiceName, key, "Delete",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	_, err = eksClient.DescribeCluster(context.Background(), &ekssdk.DescribeClusterInput{Name: aws.String(name)})
	require.Error(t, err, "cluster should be deleted from AWS")
}

func TestEKSClusterReconcile_DetectsAndCorrectsTagDrift(t *testing.T) {
	client, eksClient := setupEKSClusterDriver(t)
	name := uniqueEKSClusterName(t)
	key := url.PathEscape(fmt.Sprintf("us-east-1~%s", name))

	out := provisionCluster(t, client, key, baseClusterSpec(name))

	// Externally mutate the tag set to introduce drift.
	_, err := eksClient.TagResource(context.Background(), &ekssdk.TagResourceInput{
		ResourceArn: aws.String(out.ARN),
		Tags:        map[string]string{"env": "hijacked", "rogue": "1"},
	})
	require.NoError(t, err)

	result, err := ingress.Object[restate.Void, types.ReconcileResult](
		client, ekscluster.ServiceName, key, "Reconcile",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift, "tag drift should be detected")
	assert.True(t, result.Correcting, "managed mode should correct tag drift")

	got, err := eksClient.DescribeCluster(context.Background(), &ekssdk.DescribeClusterInput{Name: aws.String(name)})
	require.NoError(t, err)
	assert.Equal(t, "test", got.Cluster.Tags["env"], "reconcile should restore the desired tag value")
	assert.NotContains(t, got.Cluster.Tags, "rogue", "reconcile should remove externally-added tags")
}

func TestEKSClusterReconcile_DetectsAndCorrectsEndpointDrift(t *testing.T) {
	client, eksClient := setupEKSClusterDriver(t)
	name := uniqueEKSClusterName(t)
	key := url.PathEscape(fmt.Sprintf("us-east-1~%s", name))

	provisionCluster(t, client, key, baseClusterSpec(name))

	// Externally flip the endpoint access configuration.
	_, err := eksClient.UpdateClusterConfig(context.Background(), &ekssdk.UpdateClusterConfigInput{
		Name: aws.String(name),
		ResourcesVpcConfig: &ekstypes.VpcConfigRequest{
			EndpointPublicAccess:  aws.Bool(false),
			EndpointPrivateAccess: aws.Bool(true),
		},
	})
	require.NoError(t, err)

	result, err := ingress.Object[restate.Void, types.ReconcileResult](
		client, ekscluster.ServiceName, key, "Reconcile",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift, "endpoint access drift should be detected")
	assert.True(t, result.Correcting, "managed mode should correct endpoint drift")

	// Moto accepts a second UpdateClusterConfig while its first emulated update
	// is still in progress, but silently leaves the first values in place. The
	// stateful provider suite verifies the corrected endpoint values; this
	// provider integration verifies live drift detection and mutation dispatch.
}

func TestEKSClusterReconcile_DetectsExternalDelete(t *testing.T) {
	client, eksClient := setupEKSClusterDriver(t)
	name := uniqueEKSClusterName(t)
	key := url.PathEscape(fmt.Sprintf("us-east-1~%s", name))

	provisionCluster(t, client, key, baseClusterSpec(name))

	_, err := eksClient.DeleteCluster(context.Background(), &ekssdk.DeleteClusterInput{Name: aws.String(name)})
	require.NoError(t, err)

	result, err := ingress.Object[restate.Void, types.ReconcileResult](
		client, ekscluster.ServiceName, key, "Reconcile",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Contains(t, result.Error, "deleted externally", "external deletion should be surfaced as an error")

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, ekscluster.ServiceName, key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusError, status.Status)
}
