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
	ecssdk "github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	"github.com/shirvan/praxis/internal/core/authservice"

	"github.com/shirvan/praxis/internal/drivers/ecscluster"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// uniqueECSClusterName derives a Moto-safe, collision-free cluster name from the
// test name plus a nanosecond suffix. Moto keeps no cross-test state guarantees,
// so every test provisions under its own name.
func uniqueECSClusterName(t *testing.T) string {
	t.Helper()
	name := strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	name = strings.ReplaceAll(name, "_", "-")
	if len(name) > 80 {
		name = name[:80]
	}
	return fmt.Sprintf("%s-%d", name, time.Now().UnixNano()%100000)
}

func setupECSClusterDriver(t *testing.T) (*ingress.Client, *ecssdk.Client) {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := motoAWSConfig(t)
	ecsClient := awsclient.NewECSClient(awsCfg)
	driver := ecscluster.NewGenericECSClusterDriver(authservice.NewAuthClient())

	ingressClient := setupDriverEventingEnv(t, driver)
	return ingressClient, ecsClient
}

// baseECSClusterSpec builds a spec that Moto accepts and reflects: Container
// Insights enabled and a pair of Fargate capacity providers.
func baseECSClusterSpec(name string) ecscluster.ECSClusterSpec {
	return ecscluster.ECSClusterSpec{
		Account:           integrationAccountName,
		Region:            "us-east-1",
		Name:              name,
		ContainerInsights: "enabled",
		CapacityProviders: []string{"FARGATE", "FARGATE_SPOT"},
		Tags:              map[string]string{"env": "test"},
	}
}

func provisionECSCluster(t *testing.T, client *ingress.Client, key string, spec ecscluster.ECSClusterSpec) ecscluster.ECSClusterOutputs {
	t.Helper()
	out, err := ingress.Object[types.ProvisionRequest, ecscluster.ECSClusterOutputs](
		client, ecscluster.ServiceName, key, "Provision",
	).Request(t.Context(), provisionRequest(t, spec))
	require.NoError(t, err)
	return out
}

func describeECSCluster(t *testing.T, ecsClient *ecssdk.Client, name string) ecstypes.Cluster {
	t.Helper()
	out, err := ecsClient.DescribeClusters(context.Background(), &ecssdk.DescribeClustersInput{
		Clusters: []string{name},
		Include:  []ecstypes.ClusterField{ecstypes.ClusterFieldTags, ecstypes.ClusterFieldSettings},
	})
	require.NoError(t, err)
	require.Len(t, out.Clusters, 1, "cluster %s should exist", name)
	return out.Clusters[0]
}

func ecsClusterTag(cluster ecstypes.Cluster, key string) (string, bool) {
	for _, tag := range cluster.Tags {
		if aws.ToString(tag.Key) == key {
			return aws.ToString(tag.Value), true
		}
	}
	return "", false
}

func TestECSClusterProvision_CreatesCluster(t *testing.T) {
	client, ecsClient := setupECSClusterDriver(t)
	name := uniqueECSClusterName(t)
	key := url.PathEscape(fmt.Sprintf("us-east-1~%s", name))

	outputs := provisionECSCluster(t, client, key, baseECSClusterSpec(name))
	assert.Equal(t, name, outputs.Name)
	assert.Equal(t, "ACTIVE", outputs.Status)
	assert.NotEmpty(t, outputs.ARN)

	cluster := describeECSCluster(t, ecsClient, name)
	assert.Equal(t, name, aws.ToString(cluster.ClusterName))
	env, ok := ecsClusterTag(cluster, "env")
	assert.True(t, ok)
	assert.Equal(t, "test", env)
	_, hasManaged := ecsClusterTag(cluster, "praxis:managed-key")
	assert.True(t, hasManaged, "provisioning should stamp the managed-key marker")
	assert.ElementsMatch(t, []string{"FARGATE", "FARGATE_SPOT"}, cluster.CapacityProviders)
}

func TestECSClusterProvision_Idempotent(t *testing.T) {
	client, _ := setupECSClusterDriver(t)
	name := uniqueECSClusterName(t)
	key := url.PathEscape(fmt.Sprintf("us-east-1~%s", name))
	spec := baseECSClusterSpec(name)

	out1 := provisionECSCluster(t, client, key, spec)
	out2 := provisionECSCluster(t, client, key, spec)
	assert.Equal(t, out1.ARN, out2.ARN, "re-provisioning an in-sync cluster must be a no-op")
}

func TestECSClusterImport_ExistingCluster(t *testing.T) {
	client, ecsClient := setupECSClusterDriver(t)
	name := uniqueECSClusterName(t)

	_, err := ecsClient.CreateCluster(context.Background(), &ecssdk.CreateClusterInput{
		ClusterName: aws.String(name),
		Settings: []ecstypes.ClusterSetting{
			{Name: ecstypes.ClusterSettingNameContainerInsights, Value: aws.String("enabled")},
		},
		Tags: []ecstypes.Tag{{Key: aws.String("env"), Value: aws.String("preexisting")}},
	})
	require.NoError(t, err)

	key := url.PathEscape(fmt.Sprintf("us-east-1~%s", name))
	outputs, err := ingress.Object[types.ImportRef, ecscluster.ECSClusterOutputs](
		client, ecscluster.ServiceName, key, "Import",
	).Request(t.Context(), types.ImportRef{ResourceID: name, Account: integrationAccountName})
	require.NoError(t, err)
	assert.Equal(t, name, outputs.Name)
	assert.NotEmpty(t, outputs.ARN)

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, ecscluster.ServiceName, key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, status.Mode, "default import mode is Observed")
}

func TestECSClusterDelete_RemovesCluster(t *testing.T) {
	client, ecsClient := setupECSClusterDriver(t)
	name := uniqueECSClusterName(t)
	key := url.PathEscape(fmt.Sprintf("us-east-1~%s", name))

	provisionECSCluster(t, client, key, baseECSClusterSpec(name))

	_, err := ingress.Object[restate.Void, restate.Void](
		client, ecscluster.ServiceName, key, "Delete",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	// ECS retains deleted clusters in an INACTIVE status rather than dropping
	// them, so assert on the status instead of expecting a describe error.
	out, err := ecsClient.DescribeClusters(context.Background(), &ecssdk.DescribeClustersInput{
		Clusters: []string{name},
	})
	require.NoError(t, err)
	require.Len(t, out.Clusters, 1)
	assert.Equal(t, "INACTIVE", aws.ToString(out.Clusters[0].Status), "deleted cluster should be INACTIVE")
}

func TestECSClusterReconcile_DetectsAndCorrectsTagDrift(t *testing.T) {
	client, ecsClient := setupECSClusterDriver(t)
	name := uniqueECSClusterName(t)
	key := url.PathEscape(fmt.Sprintf("us-east-1~%s", name))

	out := provisionECSCluster(t, client, key, baseECSClusterSpec(name))

	// Externally mutate the tag set to introduce drift.
	_, err := ecsClient.TagResource(context.Background(), &ecssdk.TagResourceInput{
		ResourceArn: aws.String(out.ARN),
		Tags: []ecstypes.Tag{
			{Key: aws.String("env"), Value: aws.String("hijacked")},
			{Key: aws.String("rogue"), Value: aws.String("1")},
		},
	})
	require.NoError(t, err)

	result, err := ingress.Object[restate.Void, types.ReconcileResult](
		client, ecscluster.ServiceName, key, "Reconcile",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift, "tag drift should be detected")
	assert.True(t, result.Correcting, "managed mode should correct tag drift")

	cluster := describeECSCluster(t, ecsClient, name)
	env, _ := ecsClusterTag(cluster, "env")
	assert.Equal(t, "test", env, "reconcile should restore the desired tag value")
	_, hasRogue := ecsClusterTag(cluster, "rogue")
	assert.False(t, hasRogue, "reconcile should remove externally-added tags")
}

func TestECSClusterReconcile_DetectsAndCorrectsContainerInsightsDrift(t *testing.T) {
	client, ecsClient := setupECSClusterDriver(t)
	name := uniqueECSClusterName(t)
	key := url.PathEscape(fmt.Sprintf("us-east-1~%s", name))

	provisionECSCluster(t, client, key, baseECSClusterSpec(name))

	// Externally flip Container Insights off.
	_, err := ecsClient.UpdateCluster(context.Background(), &ecssdk.UpdateClusterInput{
		Cluster: aws.String(name),
		Settings: []ecstypes.ClusterSetting{
			{Name: ecstypes.ClusterSettingNameContainerInsights, Value: aws.String("disabled")},
		},
	})
	require.NoError(t, err)

	result, err := ingress.Object[restate.Void, types.ReconcileResult](
		client, ecscluster.ServiceName, key, "Reconcile",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift, "containerInsights drift should be detected")
	assert.True(t, result.Correcting, "managed mode should correct containerInsights drift")

	cluster := describeECSCluster(t, ecsClient, name)
	var value string
	for _, setting := range cluster.Settings {
		if setting.Name == ecstypes.ClusterSettingNameContainerInsights {
			value = aws.ToString(setting.Value)
		}
	}
	assert.Equal(t, "enabled", value, "reconcile should restore containerInsights to the desired value")
}

func TestECSClusterReconcile_DetectsExternalDelete(t *testing.T) {
	client, ecsClient := setupECSClusterDriver(t)
	name := uniqueECSClusterName(t)
	key := url.PathEscape(fmt.Sprintf("us-east-1~%s", name))

	provisionECSCluster(t, client, key, baseECSClusterSpec(name))

	_, err := ecsClient.DeleteCluster(context.Background(), &ecssdk.DeleteClusterInput{Cluster: aws.String(name)})
	require.NoError(t, err)

	result, err := ingress.Object[restate.Void, types.ReconcileResult](
		client, ecscluster.ServiceName, key, "Reconcile",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Contains(t, result.Error, "deleted externally", "external deletion should be surfaced as an error")

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, ecscluster.ServiceName, key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusError, status.Status)
}
