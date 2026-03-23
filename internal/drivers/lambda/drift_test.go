package lambda

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHasDrift(t *testing.T) {
	desired := applyDefaults(LambdaFunctionSpec{FunctionName: "fn", Role: "arn:aws:iam::123:role/test", PackageType: "Image", Code: CodeSpec{ImageURI: "123456789012.dkr.ecr.us-east-1.amazonaws.com/fn:1"}, Description: "v1", Timeout: 10, MemorySize: 256, Environment: map[string]string{"APP": "x"}, Layers: []string{"layer:1"}, Architectures: []string{"arm64"}, Tags: map[string]string{"env": "dev"}})
	observed := ObservedState{FunctionName: "fn", Role: "arn:aws:iam::123:role/test", PackageType: "Image", Description: "v0", Timeout: 10, MemorySize: 256, Environment: map[string]string{"APP": "x"}, Layers: []string{"layer:1"}, Architectures: []string{"arm64"}, Tags: map[string]string{"env": "dev"}, ImageURI: "123456789012.dkr.ecr.us-east-1.amazonaws.com/fn:0"}
	assert.True(t, HasDrift(desired, observed))
	assert.NotEmpty(t, ComputeFieldDiffs(desired, observed))
}

func TestCodeSpecChanged(t *testing.T) {
	assert.False(t, codeSpecChanged(CodeSpec{ImageURI: "img:1"}, CodeSpec{ImageURI: "img:1"}))
	assert.True(t, codeSpecChanged(CodeSpec{ImageURI: "img:1"}, CodeSpec{ImageURI: "img:2"}))
}