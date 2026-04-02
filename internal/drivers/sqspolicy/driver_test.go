package sqspolicy

import (
	"testing"

	"github.com/shirvan/praxis/pkg/types"
	"github.com/stretchr/testify/assert"
)

func TestServiceName(t *testing.T) {
	drv := NewSQSQueuePolicyDriver(nil)
	assert.Equal(t, ServiceName, drv.ServiceName())
}

func TestDefaultImportMode(t *testing.T) {
	assert.Equal(t, types.ModeObserved, defaultImportMode(""))
	assert.Equal(t, types.ModeManaged, defaultImportMode(types.ModeManaged))
}

func TestRegionFromQueueARN(t *testing.T) {
	assert.Equal(t, "us-east-1", regionFromQueueARN("arn:aws:sqs:us-east-1:123456789012:orders"))
}
