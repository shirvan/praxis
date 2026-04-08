package orchestrator

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/pkg/types"
)

func TestDefaultTimeoutsForAdapter_NoProvider(t *testing.T) {
	// Using nil since defaultTimeoutsForAdapter does a type assertion
	result := defaultTimeoutsForAdapter(nil)
	assert.Equal(t, types.ResourceTimeouts{}, result)
}

func TestParseDurationField_Valid(t *testing.T) {
	d, err := parseDurationField("test", "30m")
	require.NoError(t, err)
	assert.Equal(t, 30*time.Minute, d)
}

func TestParseDurationField_Invalid(t *testing.T) {
	_, err := parseDurationField("test", "not-a-duration")
	assert.Error(t, err)
}

func TestParseDurationField_Hour(t *testing.T) {
	d, err := parseDurationField("test", "1h")
	require.NoError(t, err)
	assert.Equal(t, 1*time.Hour, d)
}

func TestParseDurationField_Second(t *testing.T) {
	d, err := parseDurationField("test", "30s")
	require.NoError(t, err)
	assert.Equal(t, 30*time.Second, d)
}
