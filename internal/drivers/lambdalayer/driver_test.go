package lambdalayer

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/praxiscloud/praxis/pkg/types"
)

func TestServiceName(t *testing.T) {
	assert.Equal(t, ServiceName, NewLambdaLayerDriver(nil).ServiceName())
}

func TestOutputsFromObserved(t *testing.T) {
	obs := ObservedState{LayerArn: "arn:aws:lambda:us-east-1:123:layer:deps", LayerVersionArn: "arn:aws:lambda:us-east-1:123:layer:deps:3", LayerName: "deps", Version: 3, CodeSize: 123, CodeSha256: "abc"}
	out := outputsFromObserved(obs)
	assert.Equal(t, obs.LayerVersionArn, out.LayerVersionArn)
	assert.Equal(t, int64(3), out.Version)
	assert.Equal(t, "abc", out.CodeSha256)
}

func TestValidateProvisionSpec(t *testing.T) {
	spec := applyDefaults(LambdaLayerSpec{Region: "us-east-1", LayerName: "deps", Code: CodeSpec{ZipFile: "Zm9v"}})
	require.NoError(t, validateProvisionSpec(spec))
}

func TestDefaultImportMode(t *testing.T) {
	assert.Equal(t, types.ModeObserved, defaultImportMode(""))
	assert.Equal(t, types.ModeManaged, defaultImportMode(types.ModeManaged))
}
