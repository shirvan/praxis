package lambdalayer

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHasDrift_VersionChange(t *testing.T) {
	desired := applyDefaults(LambdaLayerSpec{LayerName: "deps", Permissions: &PermissionsSpec{AccountIds: []string{"123"}}})
	observed := ObservedState{LayerName: "deps", Version: 2, Permissions: PermissionsSpec{AccountIds: []string{"123"}}}
	outputs := LambdaLayerOutputs{LayerName: "deps", Version: 1}
	assert.True(t, HasDrift(desired, observed, outputs))
}

func TestHasDrift_NoDrift(t *testing.T) {
	desired := applyDefaults(LambdaLayerSpec{LayerName: "deps", Description: "runtime deps", CompatibleRuntimes: []string{"python3.12"}, Permissions: &PermissionsSpec{Public: true}})
	observed := ObservedState{LayerName: "deps", Description: "runtime deps", CompatibleRuntimes: []string{"python3.12"}, Permissions: PermissionsSpec{Public: true}, Version: 1}
	outputs := LambdaLayerOutputs{LayerName: "deps", Version: 1}
	assert.False(t, HasDrift(desired, observed, outputs))
}
