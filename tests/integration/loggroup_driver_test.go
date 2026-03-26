//go:build integration

package integration

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	cwlogssdk "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	restatetest "github.com/restatedev/sdk-go/testing"

	"github.com/shirvan/praxis/internal/drivers/loggroup"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

func uniqueLogGroupName(t *testing.T) string {
	t.Helper()
	name := strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	name = strings.ReplaceAll(name, "_", "-")
	if len(name) > 50 {
		name = name[:50]
	}
	return fmt.Sprintf("/praxis/test/%s-%d", name, time.Now().UnixNano()%100000)
}

func setupLogGroupDriver(t *testing.T) (*ingress.Client, *cwlogssdk.Client) {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := localstackAWSConfig(t)
	cwLogsClient := awsclient.NewCloudWatchLogsClient(awsCfg)
	driver := loggroup.NewLogGroupDriver(nil)

	env := restatetest.Start(t, restate.Reflect(driver))
	return env.Ingress(), cwLogsClient
}

func TestLogGroupProvision_CreatesLogGroup(t *testing.T) {
	client, cwClient := setupLogGroupDriver(t)
	name := uniqueLogGroupName(t)
	key := fmt.Sprintf("us-east-1~%s", name)
	retention := int32(14)

	outputs, err := ingress.Object[loggroup.LogGroupSpec, loggroup.LogGroupOutputs](
		client, loggroup.ServiceName, key, "Provision",
	).Request(t.Context(), loggroup.LogGroupSpec{
		Account:         integrationAccountName,
		Region:          "us-east-1",
		LogGroupName:    name,
		RetentionInDays: &retention,
		Tags:            map[string]string{"env": "test"},
	})
	require.NoError(t, err)
	assert.Equal(t, name, outputs.LogGroupName)
	assert.NotEmpty(t, outputs.ARN)
	assert.Equal(t, int32(14), outputs.RetentionInDays)

	desc, err := cwClient.DescribeLogGroups(context.Background(), &cwlogssdk.DescribeLogGroupsInput{
		LogGroupNamePrefix: aws.String(name),
	})
	require.NoError(t, err)
	require.Len(t, desc.LogGroups, 1)
	assert.Equal(t, name, aws.ToString(desc.LogGroups[0].LogGroupName))
}

func TestLogGroupProvision_Idempotent(t *testing.T) {
	client, _ := setupLogGroupDriver(t)
	name := uniqueLogGroupName(t)
	key := fmt.Sprintf("us-east-1~%s", name)
	retention := int32(7)
	spec := loggroup.LogGroupSpec{
		Account:         integrationAccountName,
		Region:          "us-east-1",
		LogGroupName:    name,
		RetentionInDays: &retention,
		Tags:            map[string]string{"env": "test"},
	}

	out1, err := ingress.Object[loggroup.LogGroupSpec, loggroup.LogGroupOutputs](
		client, loggroup.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)

	out2, err := ingress.Object[loggroup.LogGroupSpec, loggroup.LogGroupOutputs](
		client, loggroup.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)

	assert.Equal(t, out1.ARN, out2.ARN)
	assert.Equal(t, out1.LogGroupName, out2.LogGroupName)
}

func TestLogGroupImport_ExistingLogGroup(t *testing.T) {
	client, cwClient := setupLogGroupDriver(t)
	name := uniqueLogGroupName(t)

	_, err := cwClient.CreateLogGroup(context.Background(), &cwlogssdk.CreateLogGroupInput{
		LogGroupName: aws.String(name),
	})
	require.NoError(t, err)

	key := fmt.Sprintf("us-east-1~%s", name)
	outputs, err := ingress.Object[types.ImportRef, loggroup.LogGroupOutputs](
		client, loggroup.ServiceName, key, "Import",
	).Request(t.Context(), types.ImportRef{
		ResourceID: name,
		Account:    integrationAccountName,
	})
	require.NoError(t, err)
	assert.Equal(t, name, outputs.LogGroupName)
	assert.NotEmpty(t, outputs.ARN)

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, loggroup.ServiceName, key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, status.Mode)
}

func TestLogGroupDelete_RemovesLogGroup(t *testing.T) {
	client, cwClient := setupLogGroupDriver(t)
	name := uniqueLogGroupName(t)
	key := fmt.Sprintf("us-east-1~%s", name)
	retention := int32(7)

	_, err := ingress.Object[loggroup.LogGroupSpec, loggroup.LogGroupOutputs](
		client, loggroup.ServiceName, key, "Provision",
	).Request(t.Context(), loggroup.LogGroupSpec{
		Account:         integrationAccountName,
		Region:          "us-east-1",
		LogGroupName:    name,
		RetentionInDays: &retention,
	})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](
		client, loggroup.ServiceName, key, "Delete",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	desc, err := cwClient.DescribeLogGroups(context.Background(), &cwlogssdk.DescribeLogGroupsInput{
		LogGroupNamePrefix: aws.String(name),
	})
	require.NoError(t, err)
	for _, g := range desc.LogGroups {
		assert.NotEqual(t, name, aws.ToString(g.LogGroupName), "log group should be deleted")
	}
}

func TestLogGroupReconcile_DetectsRetentionDrift(t *testing.T) {
	client, cwClient := setupLogGroupDriver(t)
	name := uniqueLogGroupName(t)
	key := fmt.Sprintf("us-east-1~%s", name)
	retention := int32(14)

	_, err := ingress.Object[loggroup.LogGroupSpec, loggroup.LogGroupOutputs](
		client, loggroup.ServiceName, key, "Provision",
	).Request(t.Context(), loggroup.LogGroupSpec{
		Account:         integrationAccountName,
		Region:          "us-east-1",
		LogGroupName:    name,
		RetentionInDays: &retention,
	})
	require.NoError(t, err)

	// Externally change retention to introduce drift
	_, err = cwClient.PutRetentionPolicy(context.Background(), &cwlogssdk.PutRetentionPolicyInput{
		LogGroupName:    aws.String(name),
		RetentionInDays: aws.Int32(30),
	})
	require.NoError(t, err)

	result, err := ingress.Object[restate.Void, types.ReconcileResult](
		client, loggroup.ServiceName, key, "Reconcile",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift, "drift should be detected")
	assert.True(t, result.Correcting, "managed mode should correct drift")
}
