package esm

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestESMHasDrift(t *testing.T) {
	batchSize := int32(10)
	desired := applyDefaults(EventSourceMappingSpec{Enabled: true, BatchSize: &batchSize})
	observed := ObservedState{State: "Enabled", BatchSize: 5}
	assert.True(t, HasDrift(desired, observed))
}

func TestESMNoDrift(t *testing.T) {
	batchSize := int32(10)
	desired := applyDefaults(EventSourceMappingSpec{Enabled: true, BatchSize: &batchSize})
	observed := ObservedState{State: "Enabled", BatchSize: 10}
	assert.False(t, HasDrift(desired, observed))
}
