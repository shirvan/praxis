package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers/metricalarm"
)

func TestMetricAlarmAdapter_DecodeSpecAndBuildKey(t *testing.T) {
	adapter := NewMetricAlarmAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"MetricAlarm",
		"metadata":{"name":"cpu-high"},
		"spec":{
			"region":"us-east-1",
			"namespace":"AWS/EC2",
			"metricName":"CPUUtilization",
			"statistic":"Average",
			"period":60,
			"evaluationPeriods":2,
			"threshold":80,
			"comparisonOperator":"GreaterThanThreshold",
			"tags":{"env":"dev"}
		}
	}`)

	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~cpu-high", key)

	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed, ok := decoded.(metricalarm.MetricAlarmSpec)
	require.True(t, ok)
	assert.Equal(t, "cpu-high", typed.AlarmName)
	assert.Equal(t, "AWS/EC2", typed.Namespace)
	assert.Equal(t, "CPUUtilization", typed.MetricName)
	assert.Equal(t, "dev", typed.Tags["env"])
}

func TestMetricAlarmAdapter_NormalizeOutputs(t *testing.T) {
	adapter := NewMetricAlarmAdapterWithAuth(nil)
	out, err := adapter.NormalizeOutputs(metricalarm.MetricAlarmOutputs{
		AlarmArn:    "arn:aws:cloudwatch:us-east-1:123456789012:alarm:cpu-high",
		AlarmName:   "cpu-high",
		StateValue:  "OK",
		StateReason: "threshold not breached",
	})
	require.NoError(t, err)
	assert.Equal(t, "cpu-high", out["alarmName"])
	assert.Equal(t, "OK", out["stateValue"])
	assert.Equal(t, "threshold not breached", out["stateReason"])
}
