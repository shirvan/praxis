package esm

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/praxiscloud/praxis/pkg/types"
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