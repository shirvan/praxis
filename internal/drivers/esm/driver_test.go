package esm

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/pkg/types"
)

func TestESMServiceName(t *testing.T) {
	assert.Equal(t, ServiceName, NewEventSourceMappingDriver(nil).ServiceName())
}

func TestESMValidateProvisionSpec(t *testing.T) {
	spec := applyDefaults(EventSourceMappingSpec{Region: "us-east-1", FunctionName: "processor", EventSourceArn: "arn:aws:sqs:us-east-1:123:queue"})
	require.NoError(t, validateProvisionSpec(spec))
}

func TestESMDefaultImportMode(t *testing.T) {
	assert.Equal(t, types.ModeObserved, defaultImportMode(""))
	assert.Equal(t, types.ModeManaged, defaultImportMode(types.ModeManaged))
}

func TestESMValidateProvisionSpec_MissingRegion(t *testing.T) {
	spec := applyDefaults(EventSourceMappingSpec{FunctionName: "fn", EventSourceArn: "arn:aws:sqs:us-east-1:123:queue"})
	assert.Error(t, validateProvisionSpec(spec))
}

func TestESMValidateProvisionSpec_MissingFunctionName(t *testing.T) {
	spec := applyDefaults(EventSourceMappingSpec{Region: "us-east-1", EventSourceArn: "arn:aws:sqs:us-east-1:123:queue"})
	assert.Error(t, validateProvisionSpec(spec))
}

func TestESMValidateProvisionSpec_MissingEventSourceArn(t *testing.T) {
	spec := applyDefaults(EventSourceMappingSpec{Region: "us-east-1", FunctionName: "fn"})
	assert.Error(t, validateProvisionSpec(spec))
}

func TestESMApplyDefaults_NilResponseTypes(t *testing.T) {
	spec := applyDefaults(EventSourceMappingSpec{})
	assert.NotNil(t, spec.FunctionResponseTypes)
	assert.Empty(t, spec.FunctionResponseTypes)
}

func TestESMApplyDefaults_SortsResponseTypes(t *testing.T) {
	spec := applyDefaults(EventSourceMappingSpec{FunctionResponseTypes: []string{"Zebra", "Alpha"}})
	assert.Equal(t, []string{"Alpha", "Zebra"}, spec.FunctionResponseTypes)
}

func TestESMStartingPositionChanged_Same(t *testing.T) {
	a := EventSourceMappingSpec{StartingPosition: "LATEST"}
	b := EventSourceMappingSpec{StartingPosition: "LATEST"}
	assert.False(t, startingPositionChanged(a, b))
}

func TestESMStartingPositionChanged_Different(t *testing.T) {
	a := EventSourceMappingSpec{StartingPosition: "LATEST"}
	b := EventSourceMappingSpec{StartingPosition: "TRIM_HORIZON"}
	assert.True(t, startingPositionChanged(a, b))
}

func TestESMStartingPositionChanged_TimestampAdded(t *testing.T) {
	ts := "2024-01-01T00:00:00Z"
	a := EventSourceMappingSpec{StartingPosition: "AT_TIMESTAMP"}
	b := EventSourceMappingSpec{StartingPosition: "AT_TIMESTAMP", StartingPositionTimestamp: &ts}
	assert.True(t, startingPositionChanged(a, b))
}

func TestESMStartingPositionChanged_TimestampBothNil(t *testing.T) {
	a := EventSourceMappingSpec{StartingPosition: "AT_TIMESTAMP"}
	b := EventSourceMappingSpec{StartingPosition: "AT_TIMESTAMP"}
	assert.False(t, startingPositionChanged(a, b))
}

func TestESMStartingPositionChanged_TimestampDifferent(t *testing.T) {
	ts1 := "2024-01-01T00:00:00Z"
	ts2 := "2024-06-01T00:00:00Z"
	a := EventSourceMappingSpec{StartingPosition: "AT_TIMESTAMP", StartingPositionTimestamp: &ts1}
	b := EventSourceMappingSpec{StartingPosition: "AT_TIMESTAMP", StartingPositionTimestamp: &ts2}
	assert.True(t, startingPositionChanged(a, b))
}

func TestESMSpecFromObserved(t *testing.T) {
	observed := ObservedState{
		FunctionArn:    "arn:aws:lambda:us-east-1:123:function:fn",
		EventSourceArn: "arn:aws:sqs:us-east-1:123:queue",
		State:          "Enabled",
		BatchSize:      10,
	}
	spec := specFromObserved(observed)
	assert.Equal(t, observed.FunctionArn, spec.FunctionName)
	assert.Equal(t, observed.EventSourceArn, spec.EventSourceArn)
	assert.True(t, spec.Enabled)
	assert.Equal(t, int32(10), *spec.BatchSize)
}

func TestESMSpecFromObserved_Disabled(t *testing.T) {
	observed := ObservedState{State: "Disabled", BatchSize: 5}
	spec := specFromObserved(observed)
	assert.False(t, spec.Enabled)
}

func TestESMEncodedEventSourceKey(t *testing.T) {
	key := EncodedEventSourceKey("arn:aws:sqs:us-east-1:123:queue")
	assert.NotEmpty(t, key)
	assert.NotContains(t, key, "/")
}
