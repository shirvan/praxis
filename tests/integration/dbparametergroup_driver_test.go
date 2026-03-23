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
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	restatetest "github.com/restatedev/sdk-go/testing"

	"github.com/shirvan/praxis/internal/drivers/dbparametergroup"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

func uniqueParamGroupName(t *testing.T) string {
	t.Helper()
	name := strings.ReplaceAll(t.Name(), "/", "-")
	name = strings.ReplaceAll(name, "_", "-")
	name = strings.ToLower(name)
	if len(name) > 50 {
		name = name[:50]
	}
	return fmt.Sprintf("%s-%d", name, time.Now().UnixNano()%100000)
}

func setupDBParameterGroupDriver(t *testing.T) (*ingress.Client, *rdssdk.Client) {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := localstackAWSConfig(t)
	rdsClient := awsclient.NewRDSClient(awsCfg)
	driver := dbparametergroup.NewDBParameterGroupDriver(nil)

	env := restatetest.Start(t, restate.Reflect(driver))
	return env.Ingress(), rdsClient
}

func TestDBParameterGroupProvision_CreatesDB(t *testing.T) {
	client, rdsClient := setupDBParameterGroupDriver(t)
	name := uniqueParamGroupName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	outputs, err := ingress.Object[dbparametergroup.DBParameterGroupSpec, dbparametergroup.DBParameterGroupOutputs](
		client, "DBParameterGroup", key, "Provision",
	).Request(t.Context(), dbparametergroup.DBParameterGroupSpec{
		Account:     integrationAccountName,
		Region:      "us-east-1",
		GroupName:   name,
		Type:        "db",
		Family:      "mysql8.0",
		Description: "Integration test DB parameter group",
		Tags:        map[string]string{"env": "test"},
	})
	require.NoError(t, err)
	assert.Equal(t, name, outputs.GroupName)
	assert.NotEmpty(t, outputs.ARN)
	assert.Equal(t, "mysql8.0", outputs.Family)

	desc, err := rdsClient.DescribeDBParameterGroups(context.Background(), &rdssdk.DescribeDBParameterGroupsInput{
		DBParameterGroupName: aws.String(name),
	})
	require.NoError(t, err)
	require.Len(t, desc.DBParameterGroups, 1)
	assert.Equal(t, name, aws.ToString(desc.DBParameterGroups[0].DBParameterGroupName))
}

func TestDBParameterGroupProvision_CreatesCluster(t *testing.T) {
	client, rdsClient := setupDBParameterGroupDriver(t)
	name := uniqueParamGroupName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	outputs, err := ingress.Object[dbparametergroup.DBParameterGroupSpec, dbparametergroup.DBParameterGroupOutputs](
		client, "DBParameterGroup", key, "Provision",
	).Request(t.Context(), dbparametergroup.DBParameterGroupSpec{
		Account:     integrationAccountName,
		Region:      "us-east-1",
		GroupName:   name,
		Type:        "cluster",
		Family:      "aurora-mysql8.0",
		Description: "Integration test cluster parameter group",
		Tags:        map[string]string{"env": "test"},
	})
	require.NoError(t, err)
	assert.Equal(t, name, outputs.GroupName)
	assert.Equal(t, "cluster", outputs.Type)

	desc, err := rdsClient.DescribeDBClusterParameterGroups(context.Background(), &rdssdk.DescribeDBClusterParameterGroupsInput{
		DBClusterParameterGroupName: aws.String(name),
	})
	require.NoError(t, err)
	require.Len(t, desc.DBClusterParameterGroups, 1)
	assert.Equal(t, name, aws.ToString(desc.DBClusterParameterGroups[0].DBClusterParameterGroupName))
}

func TestDBParameterGroupProvision_Idempotent(t *testing.T) {
	client, _ := setupDBParameterGroupDriver(t)
	name := uniqueParamGroupName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	spec := dbparametergroup.DBParameterGroupSpec{
		Account:     integrationAccountName,
		Region:      "us-east-1",
		GroupName:   name,
		Type:        "db",
		Family:      "mysql8.0",
		Description: "Idempotent test",
		Tags:        map[string]string{"env": "test"},
	}

	out1, err := ingress.Object[dbparametergroup.DBParameterGroupSpec, dbparametergroup.DBParameterGroupOutputs](
		client, "DBParameterGroup", key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)

	out2, err := ingress.Object[dbparametergroup.DBParameterGroupSpec, dbparametergroup.DBParameterGroupOutputs](
		client, "DBParameterGroup", key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)
	assert.Equal(t, out1.ARN, out2.ARN)
}

func TestDBParameterGroupImport_ExistingGroup(t *testing.T) {
	client, rdsClient := setupDBParameterGroupDriver(t)
	name := uniqueParamGroupName(t)

	_, err := rdsClient.CreateDBParameterGroup(context.Background(), &rdssdk.CreateDBParameterGroupInput{
		DBParameterGroupName:   aws.String(name),
		DBParameterGroupFamily: aws.String("mysql8.0"),
		Description:            aws.String("Pre-existing group"),
		Tags:                   []rdstypes.Tag{{Key: aws.String("env"), Value: aws.String("import")}},
	})
	require.NoError(t, err)

	key := fmt.Sprintf("us-east-1~%s", name)
	outputs, err := ingress.Object[types.ImportRef, dbparametergroup.DBParameterGroupOutputs](
		client, "DBParameterGroup", key, "Import",
	).Request(t.Context(), types.ImportRef{
		ResourceID: name,
		Account:    integrationAccountName,
	})
	require.NoError(t, err)
	assert.Equal(t, name, outputs.GroupName)

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, "DBParameterGroup", key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, status.Mode)
}

func TestDBParameterGroupDelete_Removes(t *testing.T) {
	client, rdsClient := setupDBParameterGroupDriver(t)
	name := uniqueParamGroupName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	_, err := ingress.Object[dbparametergroup.DBParameterGroupSpec, dbparametergroup.DBParameterGroupOutputs](
		client, "DBParameterGroup", key, "Provision",
	).Request(t.Context(), dbparametergroup.DBParameterGroupSpec{
		Account:     integrationAccountName,
		Region:      "us-east-1",
		GroupName:   name,
		Type:        "db",
		Family:      "mysql8.0",
		Description: "To be deleted",
	})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](
		client, "DBParameterGroup", key, "Delete",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	_, err = rdsClient.DescribeDBParameterGroups(context.Background(), &rdssdk.DescribeDBParameterGroupsInput{
		DBParameterGroupName: aws.String(name),
	})
	require.Error(t, err, "parameter group should be gone")
}

func TestDBParameterGroupGetStatus_ReturnsReady(t *testing.T) {
	client, _ := setupDBParameterGroupDriver(t)
	name := uniqueParamGroupName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	_, err := ingress.Object[dbparametergroup.DBParameterGroupSpec, dbparametergroup.DBParameterGroupOutputs](
		client, "DBParameterGroup", key, "Provision",
	).Request(t.Context(), dbparametergroup.DBParameterGroupSpec{
		Account:     integrationAccountName,
		Region:      "us-east-1",
		GroupName:   name,
		Type:        "db",
		Family:      "mysql8.0",
		Description: "Status check",
	})
	require.NoError(t, err)

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, "DBParameterGroup", key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeManaged, status.Mode)
	assert.Greater(t, status.Generation, int64(0))
}
