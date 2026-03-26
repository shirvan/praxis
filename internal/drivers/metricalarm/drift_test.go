package metricalarm

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHasDrift_NoDrift(t *testing.T) {
	datapoints := int32(2)
	desired := MetricAlarmSpec{
		Namespace:          "AWS/EC2",
		MetricName:         "CPUUtilization",
		Statistic:          "Average",
		Period:             60,
		EvaluationPeriods:  2,
		DatapointsToAlarm:  &datapoints,
		Threshold:          80,
		ComparisonOperator: "GreaterThanThreshold",
		TreatMissingData:   "missing",
		Tags:               map[string]string{"env": "dev"},
	}
	observed := ObservedState{
		Namespace:          "AWS/EC2",
		MetricName:         "CPUUtilization",
		Statistic:          "Average",
		Period:             60,
		EvaluationPeriods:  2,
		DatapointsToAlarm:  2,
		Threshold:          80,
		ComparisonOperator: "GreaterThanThreshold",
		TreatMissingData:   "missing",
		Tags:               map[string]string{"env": "dev"},
	}
	assert.False(t, HasDrift(desired, observed))
}

func TestHasDrift_ThresholdChanged(t *testing.T) {
	assert.True(t, HasDrift(MetricAlarmSpec{Threshold: 80, EvaluationPeriods: 1}, ObservedState{Threshold: 70}))
}

func TestDatapointsMatch_DefaultsToEvaluationPeriods(t *testing.T) {
	assert.True(t, datapointsMatch(nil, 3, 3))
	assert.False(t, datapointsMatch(nil, 2, 3))
}
