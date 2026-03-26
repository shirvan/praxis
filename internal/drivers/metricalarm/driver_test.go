package metricalarm

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/pkg/types"
)

func TestServiceName(t *testing.T) {
	drv := NewMetricAlarmDriver(nil)
	assert.Equal(t, "MetricAlarm", drv.ServiceName())
}

func TestSpecFromObserved(t *testing.T) {
	obs := ObservedState{
		AlarmArn:                "arn:aws:cloudwatch:us-east-1:123456789012:alarm:cpu-high",
		AlarmName:               "cpu-high",
		Namespace:               "AWS/EC2",
		MetricName:              "CPUUtilization",
		Dimensions:              map[string]string{"InstanceId": "i-123"},
		Statistic:               "Average",
		Period:                  60,
		EvaluationPeriods:       2,
		DatapointsToAlarm:       2,
		Threshold:               80,
		ComparisonOperator:      "GreaterThanThreshold",
		TreatMissingData:        "missing",
		AlarmDescription:        "CPU high",
		ActionsEnabled:          true,
		AlarmActions:            []string{"arn:aws:sns:us-east-1:123:topic"},
		OKActions:               []string{},
		InsufficientDataActions: []string{},
		Unit:                    "Percent",
		StateValue:              "OK",
		Tags:                    map[string]string{"env": "dev", "praxis:managed-key": "us-east-1~cpu-high"},
	}

	spec := specFromObserved(obs)
	assert.Equal(t, obs.AlarmName, spec.AlarmName)
	assert.Equal(t, obs.Namespace, spec.Namespace)
	assert.Equal(t, obs.MetricName, spec.MetricName)
	assert.Equal(t, obs.Dimensions, spec.Dimensions)
	assert.Equal(t, obs.Statistic, spec.Statistic)
	assert.Equal(t, obs.Period, spec.Period)
	assert.Equal(t, obs.EvaluationPeriods, spec.EvaluationPeriods)
	require.NotNil(t, spec.DatapointsToAlarm)
	assert.Equal(t, int32(2), *spec.DatapointsToAlarm)
	assert.Equal(t, obs.Threshold, spec.Threshold)
	assert.Equal(t, obs.ComparisonOperator, spec.ComparisonOperator)
	assert.Equal(t, obs.TreatMissingData, spec.TreatMissingData)
	assert.Equal(t, obs.Unit, spec.Unit)
	assert.Equal(t, map[string]string{"env": "dev"}, spec.Tags, "praxis: tags should be filtered out")
}

func TestOutputsFromObserved(t *testing.T) {
	out := outputsFromObserved(ObservedState{
		AlarmArn:    "arn:aws:cloudwatch:us-east-1:123:alarm:cpu-high",
		AlarmName:   "cpu-high",
		StateValue:  "OK",
		StateReason: "Threshold not crossed",
	})

	assert.Equal(t, "arn:aws:cloudwatch:us-east-1:123:alarm:cpu-high", out.AlarmArn)
	assert.Equal(t, "cpu-high", out.AlarmName)
	assert.Equal(t, "OK", out.StateValue)
	assert.Equal(t, "Threshold not crossed", out.StateReason)
}

func TestDefaultImportMode(t *testing.T) {
	assert.Equal(t, types.ModeObserved, defaultImportMode(""))
	assert.Equal(t, types.ModeManaged, defaultImportMode(types.ModeManaged))
	assert.Equal(t, types.ModeObserved, defaultImportMode(types.ModeObserved))
}

func TestApplyDefaults(t *testing.T) {
	spec := applyDefaults(MetricAlarmSpec{AlarmName: "  cpu-high  ", Region: "  us-east-1  "})
	assert.Equal(t, "cpu-high", spec.AlarmName)
	assert.Equal(t, "us-east-1", spec.Region)
	assert.Equal(t, "missing", spec.TreatMissingData)
	assert.NotNil(t, spec.Dimensions)
	assert.NotNil(t, spec.Tags)
}

func TestApplyDefaults_PreservesExplicit(t *testing.T) {
	spec := applyDefaults(MetricAlarmSpec{TreatMissingData: "breaching"})
	assert.Equal(t, "breaching", spec.TreatMissingData)
}

func TestApplyDefaults_SortsActions(t *testing.T) {
	spec := applyDefaults(MetricAlarmSpec{
		AlarmActions: []string{"arn:b", "arn:a"},
		OKActions:    []string{"arn:z", "arn:y"},
	})
	assert.Equal(t, []string{"arn:a", "arn:b"}, spec.AlarmActions)
	assert.Equal(t, []string{"arn:y", "arn:z"}, spec.OKActions)
}

func TestValidateSpec(t *testing.T) {
	base := applyDefaults(MetricAlarmSpec{
		Region:             "us-east-1",
		AlarmName:          "cpu-high",
		Namespace:          "AWS/EC2",
		MetricName:         "CPUUtilization",
		Statistic:          "Average",
		Period:             60,
		EvaluationPeriods:  2,
		Threshold:          80,
		ComparisonOperator: "GreaterThanThreshold",
	})
	assert.NoError(t, validateSpec(base))

	noRegion := base
	noRegion.Region = ""
	assert.Error(t, validateSpec(noRegion))

	noAlarmName := base
	noAlarmName.AlarmName = ""
	assert.Error(t, validateSpec(noAlarmName))

	noNamespace := base
	noNamespace.Namespace = ""
	assert.Error(t, validateSpec(noNamespace))

	noMetric := base
	noMetric.MetricName = ""
	assert.Error(t, validateSpec(noMetric))

	bothStats := base
	bothStats.ExtendedStatistic = "p99"
	assert.Error(t, validateSpec(bothStats))

	noStats := base
	noStats.Statistic = ""
	noStats.ExtendedStatistic = ""
	assert.Error(t, validateSpec(noStats))

	badPeriod := base
	badPeriod.Period = 0
	assert.Error(t, validateSpec(badPeriod))

	badEvalPeriods := base
	badEvalPeriods.EvaluationPeriods = 0
	assert.Error(t, validateSpec(badEvalPeriods))

	badDatapoints := base
	dp := int32(10)
	badDatapoints.DatapointsToAlarm = &dp
	assert.Error(t, validateSpec(badDatapoints))

	noComparison := base
	noComparison.ComparisonOperator = ""
	assert.Error(t, validateSpec(noComparison))
}
