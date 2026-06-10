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

func TestHasDrift_ImmutableFieldsDoNotTrigger(t *testing.T) {
	desired := applyDefaults(LambdaFunctionSpec{FunctionName: "fn", Role: "arn:aws:iam::123:role/test", PackageType: "Zip", Runtime: "go1.x", Handler: "main", Code: CodeSpec{ZipFile: "aGVsbG8="}, Timeout: 3, MemorySize: 128, Architectures: []string{"arm64"}})
	observed := ObservedState{FunctionName: "other-fn", Role: "arn:aws:iam::123:role/test", PackageType: "Image", Runtime: "go1.x", Handler: "main", Timeout: 3, MemorySize: 128, Architectures: []string{"x86_64"}}
	assert.False(t, HasDrift(desired, observed))
	// Full diffs still report the immutable differences for visibility.
	assert.NotEmpty(t, ComputeFieldDiffs(desired, observed))
}

func TestHasDrift_CodeImageURIDoesNotTrigger(t *testing.T) {
	desired := applyDefaults(LambdaFunctionSpec{FunctionName: "fn", Role: "arn:aws:iam::123:role/test", PackageType: "Image", Code: CodeSpec{ImageURI: "repo/fn:2"}, Timeout: 3, MemorySize: 128})
	observed := ObservedState{FunctionName: "fn", Role: "arn:aws:iam::123:role/test", PackageType: "Image", Timeout: 3, MemorySize: 128, ImageURI: "repo/fn:1"}
	assert.False(t, HasDrift(desired, observed))
	diffs := ComputeFieldDiffs(desired, observed)
	found := false
	for _, d := range diffs {
		if d.Path == "spec.code.imageUri" {
			found = true
		}
	}
	assert.True(t, found)
}

func TestIsCorrectablePath(t *testing.T) {
	assert.True(t, isCorrectablePath("spec.memorySize"))
	assert.True(t, isCorrectablePath("spec.tags"))
	assert.False(t, isCorrectablePath("spec.architectures (immutable, ignored)"))
	assert.False(t, isCorrectablePath("spec.packageType (immutable, ignored)"))
	assert.False(t, isCorrectablePath("spec.functionName (immutable, ignored)"))
	assert.False(t, isCorrectablePath("spec.code.imageUri"))
}

func TestCodeSpecChanged(t *testing.T) {
	assert.False(t, codeSpecChanged(CodeSpec{ImageURI: "img:1"}, CodeSpec{ImageURI: "img:1"}))
	assert.True(t, codeSpecChanged(CodeSpec{ImageURI: "img:1"}, CodeSpec{ImageURI: "img:2"}))
}
