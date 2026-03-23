//go:build integration

package integration

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	rdssdk "github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	restatetest "github.com/restatedev/sdk-go/testing"

	"github.com/praxiscloud/praxis/internal/drivers/auroracluster"
	"github.com/praxiscloud/praxis/internal/infra/awsclient"
	"github.com/praxiscloud/praxis/pkg/types"
)

func uniqueClusterName(t *testing.T) string {
	t.Helper()
	name := strings.ReplaceAll(t.Name(), "/", "-")
	name = strings.ReplaceAll(name, "_", "-")
	name = strings.ToLower(name)
	if len(name) > 50 {
		name = name[:50]
	}
	return fmt.Sprintf("%s-%d", name, time.Now().UnixNano()%100000)
}

func setupAuroraClusterDriver(t *testing.T) (*ingress.Client, *rdssdk.Client) {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := localstackAWSConfig(t)
	rdsClient := awsclient.NewRDSClient(awsCfg)
	driver := auroracluster.NewAuroraClusterDriver(nil)

	env := restatetest.Start(t, restate.Reflect(driver))
	return env.Ingress(), rdsClient
}

func TestAuroraClusterProvision_Creates(t *testing.T) {
	client, rdsClient := setupAuroraClusterDriver(t)
	name := uniqueClusterName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	outputs, err := ingress.Object[auroracluster.AuroraClusterSpec, auroracluster.AuroraClusterOutputs](
		client, "AuroraCluster", key, "Provision",
	).Request(t.Context(), auroracluster.AuroraClusterSpec{
		Account:            integrationAccountName,
		Region:             "us-east-1",
		ClusterIdentifier:  name,
		Engine:             "aurora-mysql",
		EngineVersion:      "8.0.mysql_aurora.3.04.0",
		MasterUsername:     "admin",
		MasterUserPassword: "TestPass1234!",
		Tags:               map[string]string{"env": "test"},
	})
	require.NoError(t, err)
	assert.Equal(t, name, outputs.ClusterIdentifier)
	assert.NotEmpty(t, outputs.ARN)

	desc, err := rdsClient.DescribeDBClusters(context.Background(), &rdssdk.DescribeDBClustersInput{
		DBClusterIdentifier: aws.String(name),
	})
	require.NoError(t, err)
	require.Len(t, desc.DBClusters, 1)
	assert.Equal(t, name, aws.ToString(desc.DBClusters[0].DBClusterIdentifier))
}

func TestAuroraClusterProvision_Idempotent(t *testing.T) {
	client, _ := setupAuroraClusterDriver(t)
	name := uniqueClusterName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	spec := auroracluster.AuroraClusterSpec{
		Account:            integrationAccountName,
		Region:             "us-east-1",
		ClusterIdentifier:  name,
		Engine:             "aurora-mysql",
		EngineVersion:      "8.0.mysql_aurora.3.04.0",
		MasterUsername:     "admin",
		MasterUserPassword: "TestPass1234!",
		Tags:               map[string]string{"env": "test"},
	}

	out1, err := ingress.Object[auroracluster.AuroraClusterSpec, auroracluster.AuroraClusterOutputs](
		client, "AuroraCluster", key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)

	out2, err := ingress.Object[auroracluster.AuroraClusterSpec, auroracluster.AuroraClusterOutputs](
		client, "AuroraCluster", key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)
	assert.Equal(t, out1.ARN, out2.ARN, "re-provision should return same ARN")
}

func TestAuroraClusterImport_ExistingCluster(t *testing.T) {
	client, rdsClient := setupAuroraClusterDriver(t)
	name := uniqueClusterName(t)

	_, err := rdsClient.CreateDBCluster(context.Background(), &rdssdk.CreateDBClusterInput{
		DBClusterIdentifier: aws.String(name),
		Engine:              aws.String("aurora-mysql"),
		MasterUsername:      aws.String("admin"),
		MasterUserPassword:  aws.String("TestPass1234!"),
	})
	require.NoError(t, err)

	key := fmt.Sprintf("us-east-1~%s", name)
	outputs, err := ingress.Object[types.ImportRef, auroracluster.AuroraClusterOutputs](
		client, "AuroraCluster", key, "Import",
	).Request(t.Context(), types.ImportRef{
		ResourceID: name,
		Account:    integrationAccountName,
	})
	require.NoError(t, err)
	assert.Equal(t, name, outputs.ClusterIdentifier)

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, "AuroraCluster", key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, status.Mode)
}

func TestAuroraClusterDelete_Removes(t *testing.T) {
	client, rdsClient := setupAuroraClusterDriver(t)
	name := uniqueClusterName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	_, err := ingress.Object[auroracluster.AuroraClusterSpec, auroracluster.AuroraClusterOutputs](
		client, "AuroraCluster", key, "Provision",
	).Request(t.Context(), auroracluster.AuroraClusterSpec{
		Account:            integrationAccountName,
		Region:             "us-east-1",
		ClusterIdentifier:  name,
		Engine:             "aurora-mysql",
		EngineVersion:      "8.0.mysql_aurora.3.04.0",
		MasterUsername:     "admin",
		MasterUserPassword: "TestPass1234!",
	})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](
		client, "AuroraCluster", key, "Delete",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	_, err = rdsClient.DescribeDBClusters(context.Background(), &rdssdk.DescribeDBClustersInput{
		DBClusterIdentifier: aws.String(name),
	})
	require.Error(t, err, "cluster should be gone")
}

func TestAuroraClusterGetStatus_ReturnsReady(t *testing.T) {
	client, _ := setupAuroraClusterDriver(t)
	name := uniqueClusterName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	_, err := ingress.Object[auroracluster.AuroraClusterSpec, auroracluster.AuroraClusterOutputs](
		client, "AuroraCluster", key, "Provision",
	).Request(t.Context(), auroracluster.AuroraClusterSpec{
		Account:            integrationAccountName,
		Region:             "us-east-1",
		ClusterIdentifier:  name,
		Engine:             "aurora-mysql",
		EngineVersion:      "8.0.mysql_aurora.3.04.0",
		MasterUsername:     "admin",
		MasterUserPassword: "TestPass1234!",
	})
	require.NoError(t, err)

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, "AuroraCluster", key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeManaged, status.Mode)
	assert.Greater(t, status.Generation, int64(0))
}
