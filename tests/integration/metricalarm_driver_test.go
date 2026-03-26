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
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	restatetest "github.com/restatedev/sdk-go/testing"

	"github.com/shirvan/praxis/internal/drivers/metricalarm"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

func uniqueAlarmName(t *testing.T) string {
	t.Helper()
	name := strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	name = strings.ReplaceAll(name, "_", "-")
	if len(name) > 50 {
		name = name[:50]
	}
	return fmt.Sprintf("%s-%d", name, time.Now().UnixNano()%100000)
}

func setupMetricAlarmDriver(t *testing.T) (*ingress.Client, *cwsdk.Client) {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := localstackAWSConfig(t)
	cwClient := awsclient.NewCloudWatchClient(awsCfg)
	driver := metricalarm.NewMetricAlarmDriver(nil)

	env := restatetest.Start(t, restate.Reflect(driver))
	return env.Ingress(), cwClient
}

func defaultAlarmSpec(name string) metricalarm.MetricAlarmSpec {
	return metricalarm.MetricAlarmSpec{
		Account:            integrationAccountName,
		Region:             "us-east-1",
		AlarmName:          name,
		Namespace:          "AWS/EC2",
		MetricName:         "CPUUtilization",
		Statistic:          "Average",
		Period:             300,
		EvaluationPeriods:  3,
		Threshold:          80.0,
		ComparisonOperator: "GreaterThanThreshold",
		TreatMissingData:   "missing",
		ActionsEnabled:     true,
		Tags:               map[string]string{"env": "test"},
	}
}

func TestMetricAlarmProvision_CreatesAlarm(t *testing.T) {
	client, cwClient := setupMetricAlarmDriver(t)
	name := uniqueAlarmName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	outputs, err := ingress.Object[metricalarm.MetricAlarmSpec, metricalarm.MetricAlarmOutputs](
		client, metricalarm.ServiceName, key, "Provision",
	).Request(t.Context(), defaultAlarmSpec(name))
	require.NoError(t, err)
	assert.Equal(t, name, outputs.AlarmName)
	assert.NotEmpty(t, outputs.AlarmArn)

	desc, err := cwClient.DescribeAlarms(context.Background(), &cwsdk.DescribeAlarmsInput{
		AlarmNames: []string{name},
	})
	require.NoError(t, err)
	require.Len(t, desc.MetricAlarms, 1)
	assert.Equal(t, name, aws.ToString(desc.MetricAlarms[0].AlarmName))
	assert.InDelta(t, 80.0, aws.ToFloat64(desc.MetricAlarms[0].Threshold), 0.01)
}

func TestMetricAlarmProvision_Idempotent(t *testing.T) {
	client, _ := setupMetricAlarmDriver(t)
	name := uniqueAlarmName(t)
	key := fmt.Sprintf("us-east-1~%s", name)
	spec := defaultAlarmSpec(name)

	out1, err := ingress.Object[metricalarm.MetricAlarmSpec, metricalarm.MetricAlarmOutputs](
		client, metricalarm.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)

	out2, err := ingress.Object[metricalarm.MetricAlarmSpec, metricalarm.MetricAlarmOutputs](
		client, metricalarm.ServiceName, key, "Provision",
	).Request(t.Context(), spec)
	require.NoError(t, err)

	assert.Equal(t, out1.AlarmArn, out2.AlarmArn)
	assert.Equal(t, out1.AlarmName, out2.AlarmName)
}

func TestMetricAlarmImport_ExistingAlarm(t *testing.T) {
	client, cwClient := setupMetricAlarmDriver(t)
	name := uniqueAlarmName(t)

	_, err := cwClient.PutMetricAlarm(context.Background(), &cwsdk.PutMetricAlarmInput{
		AlarmName:          aws.String(name),
		Namespace:          aws.String("AWS/EC2"),
		MetricName:         aws.String("CPUUtilization"),
		Statistic:          cwtypes.StatisticAverage,
		Period:             aws.Int32(300),
		EvaluationPeriods:  aws.Int32(3),
		Threshold:          aws.Float64(80.0),
		ComparisonOperator: cwtypes.ComparisonOperatorGreaterThanThreshold,
	})
	require.NoError(t, err)

	key := fmt.Sprintf("us-east-1~%s", name)
	outputs, err := ingress.Object[types.ImportRef, metricalarm.MetricAlarmOutputs](
		client, metricalarm.ServiceName, key, "Import",
	).Request(t.Context(), types.ImportRef{
		ResourceID: name,
		Account:    integrationAccountName,
	})
	require.NoError(t, err)
	assert.Equal(t, name, outputs.AlarmName)
	assert.NotEmpty(t, outputs.AlarmArn)

	status, err := ingress.Object[restate.Void, types.StatusResponse](
		client, metricalarm.ServiceName, key, "GetStatus",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, status.Mode)
}

func TestMetricAlarmDelete_RemovesAlarm(t *testing.T) {
	client, cwClient := setupMetricAlarmDriver(t)
	name := uniqueAlarmName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	_, err := ingress.Object[metricalarm.MetricAlarmSpec, metricalarm.MetricAlarmOutputs](
		client, metricalarm.ServiceName, key, "Provision",
	).Request(t.Context(), defaultAlarmSpec(name))
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](
		client, metricalarm.ServiceName, key, "Delete",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	desc, err := cwClient.DescribeAlarms(context.Background(), &cwsdk.DescribeAlarmsInput{
		AlarmNames: []string{name},
	})
	require.NoError(t, err)
	assert.Empty(t, desc.MetricAlarms, "alarm should be deleted")
}

func TestMetricAlarmReconcile_DetectsThresholdDrift(t *testing.T) {
	client, cwClient := setupMetricAlarmDriver(t)
	name := uniqueAlarmName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	_, err := ingress.Object[metricalarm.MetricAlarmSpec, metricalarm.MetricAlarmOutputs](
		client, metricalarm.ServiceName, key, "Provision",
	).Request(t.Context(), defaultAlarmSpec(name))
	require.NoError(t, err)

	// Externally change threshold to introduce drift
	_, err = cwClient.PutMetricAlarm(context.Background(), &cwsdk.PutMetricAlarmInput{
		AlarmName:          aws.String(name),
		Namespace:          aws.String("AWS/EC2"),
		MetricName:         aws.String("CPUUtilization"),
		Statistic:          cwtypes.StatisticAverage,
		Period:             aws.Int32(300),
		EvaluationPeriods:  aws.Int32(3),
		Threshold:          aws.Float64(95.0),
		ComparisonOperator: cwtypes.ComparisonOperatorGreaterThanThreshold,
	})
	require.NoError(t, err)

	result, err := ingress.Object[restate.Void, types.ReconcileResult](
		client, metricalarm.ServiceName, key, "Reconcile",
	).Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift, "drift should be detected")
	assert.True(t, result.Correcting, "managed mode should correct drift")
}
