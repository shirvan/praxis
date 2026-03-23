package lambda

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/praxiscloud/praxis/pkg/types"
)

func TestServiceName(t *testing.T) {
	assert.Equal(t, ServiceName, NewLambdaFunctionDriver(nil).ServiceName())
}

func TestOutputsFromObserved(t *testing.T) {
	obs := ObservedState{FunctionArn: "arn:aws:lambda:us-east-1:123:function:fn", FunctionName: "fn", Version: "$LATEST", State: "Active", LastModified: "2026-03-22T00:00:00Z", LastUpdateStatus: "Successful", CodeSha256: "abc"}
	out := outputsFromObserved(obs)
	assert.Equal(t, obs.FunctionArn, out.FunctionArn)
	assert.Equal(t, obs.FunctionName, out.FunctionName)
	assert.Equal(t, obs.CodeSha256, out.CodeSha256)
}

func TestSpecFromObserved(t *testing.T) {
	obs := ObservedState{FunctionName: "fn", Role: "arn:aws:iam::123:role/test", PackageType: "Image", Description: "hello", Environment: map[string]string{"APP": "x"}, Layers: []string{"layer:1"}, Tags: map[string]string{"env": "dev", "praxis:managed-key": "k"}, ImageURI: "img:1"}
	spec := specFromObserved(obs)
	assert.Equal(t, "fn", spec.FunctionName)
	assert.Equal(t, "Image", spec.PackageType)
	assert.Equal(t, "img:1", spec.Code.ImageURI)
	assert.Equal(t, map[string]string{"env": "dev"}, spec.Tags)
}

func TestDefaultLambdaImportMode(t *testing.T) {
	assert.Equal(t, types.ModeObserved, defaultLambdaImportMode(""))
	assert.Equal(t, types.ModeManaged, defaultLambdaImportMode(types.ModeManaged))
}

func TestValidateProvisionSpec(t *testing.T) {
	spec := applyDefaults(LambdaFunctionSpec{Region: "us-east-1", FunctionName: "fn", Role: "arn:aws:iam::123:role/test", Runtime: "python3.12", Handler: "main.handler", Code: CodeSpec{ZipFile: "Zm9v"}})
	require.NoError(t, validateProvisionSpec(spec))
}