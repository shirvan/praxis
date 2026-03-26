//go:build integration

package integration

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	cwsdk "github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	restatetest "github.com/restatedev/sdk-go/testing"

	"github.com/shirvan/praxis/internal/drivers/dashboard"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

func uniqueDashboardName(t *testing.T) string {
	t.Helper()
	name := strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	name = strings.ReplaceAll(name, "_", "-")
	if len(name) > 50 {
		name = name[:50]
	}
	return fmt.Sprintf("%s-%d", name, time.Now().UnixNano()%100000)
}

func setupDashboardDriver(t *testing.T) (*ingress.Client, *cwsdk.Client) {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := localstackAWSConfig(t)
	cwClient := awsclient.NewCloudWatchClient(awsCfg)
	driver := dashboard.NewDashboardDriver(nil)

	env := restatetest.Start(t, restate.Reflect(driver))
	return env.Ingress(), cwClient
}

const testDashboardBody = `{"widgets":[{"type":"metric","x":0,"y":0,"width":12,"height":6,"properties":{"metrics":[["AWS/EC2","CPUUtilization"]],"period":300,"stat":"Average","region":"us-east-1","title":"EC2 CPU"}}]}`

func TestDashboardProvision_CreatesDashboard(t *testing.T) {
	client, cwClient := setupDashboardDriver(t)
	name := uniqueDashboardName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	outputs, err := ingress.Object[dashboard.DashboardSpec, dashboard.DashboardOutputs](
		client, dashboard.ServiceName, key, "Provision",
	).Request(t.Context(), dashboard.DashboardSpec{
		Account:       integrationAccountName,
		Region:        "us-east-1",
		DashboardName: name,
		DashboardBody: testDashboardBody,
	})
	require.NoError(t, err)
	assert.Equal(t, name, outputs.DashboardName)
	assert.NotEmpty(t, outputs.DashboardArn)

	desc, err := cwClient.GetDashboard(context.Background(), &cwsdk.GetDashboardInput{
		DashboardName: aws.String(name),
	})
	require.NoError(t, err)
	assert.Equal(t, name, aws.ToString(desc.DashboardName))
	assert.NotEmpty(t, aws.ToString(desc.DashboardBody))
}

func TestDashboardProvision_Idempotent(t *testing.T) {
	client, _ := setupDashboardDriver(t)
	name := uniqueDashboardName(t)
	key := fmt.Sprintf("us-east-1~%s", name)
	spec := dashboard.DashboardSpec{
		Account:       integrationAccountName,
		Region:        "us-east-1",
		DashboardName: name,
		DashboardBody: testDashboardBody,
	}

	out1, err := ingress.Object[dashboard.DashboardSpec, dashboard.DashboardOutputs](
		client, dashboard.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)

	out2, err := ingress.Object[dashboard.DashboardSpec, dashboard.DashboardOutputs](
		client, dashboard.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)

	assert.Equal(t, out1.DashboardArn, out2.DashboardArn)
	assert.Equal(t, out1.DashboardName, out2.DashboardName)
}

func TestDashboardImport_ExistingDashboard(t *testing.T) {
	client, cwClient := setupDashboardDriver(t)
	name := uniqueDashboardName(t)

	_, err := cwClient.PutDashboard(context.Background(), &cwsdk.PutDashboardInput{
		DashboardName: aws.String(name),
		DashboardBody: aws.String(testDashboardBody),
	})
	require.NoError(t, err)

	key := fmt.Sprintf("us-east-1~%s", name)
	outputs, err := ingress.Object[types.ImportRef, dashboard.DashboardOutputs](
		client, dashboard.ServiceName, key, "Import",
	).Request(t.Context(), types.ImportRef{
		ResourceID: name,
		Account:    integrationAccountName,
	})
	require.NoError(t, err)
	assert.Equal(t, name, outputs.DashboardName)
	assert.NotEmpty(t, outputs.DashboardArn)

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, dashboard.ServiceName, key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, status.Mode)
}

func TestDashboardDelete_RemovesDashboard(t *testing.T) {
	client, cwClient := setupDashboardDriver(t)
	name := uniqueDashboardName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	_, err := ingress.Object[dashboard.DashboardSpec, dashboard.DashboardOutputs](
		client, dashboard.ServiceName, key, "Provision",
	).Request(t.Context(), dashboard.DashboardSpec{
		Account:       integrationAccountName,
		Region:        "us-east-1",
		DashboardName: name,
		DashboardBody: testDashboardBody,
	})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](
		client, dashboard.ServiceName, key, "Delete",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	_, err = cwClient.GetDashboard(context.Background(), &cwsdk.GetDashboardInput{
		DashboardName: aws.String(name),
	})
	require.Error(t, err, "dashboard should be deleted")
}

func TestDashboardReconcile_DetectsBodyDrift(t *testing.T) {
	client, cwClient := setupDashboardDriver(t)
	name := uniqueDashboardName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	_, err := ingress.Object[dashboard.DashboardSpec, dashboard.DashboardOutputs](
		client, dashboard.ServiceName, key, "Provision",
	).Request(t.Context(), dashboard.DashboardSpec{
		Account:       integrationAccountName,
		Region:        "us-east-1",
		DashboardName: name,
		DashboardBody: testDashboardBody,
	})
	require.NoError(t, err)

	// Externally change dashboard body to introduce drift
	driftedBody := `{"widgets":[{"type":"text","x":0,"y":0,"width":6,"height":3,"properties":{"markdown":"drifted"}}]}`
	_, err = cwClient.PutDashboard(context.Background(), &cwsdk.PutDashboardInput{
		DashboardName: aws.String(name),
		DashboardBody: aws.String(driftedBody),
	})
	require.NoError(t, err)

	result, err := ingress.Object[restate.Void, types.ReconcileResult](
		client, dashboard.ServiceName, key, "Reconcile",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift, "drift should be detected")
	assert.True(t, result.Correcting, "managed mode should correct drift")
}
