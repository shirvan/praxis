package concierge

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToolRegistryContainsAllTools(t *testing.T) {
	r := NewToolRegistry()
	names := r.Names()

	// Read tools
	assert.Contains(t, names, "getDeploymentStatus")
	assert.Contains(t, names, "listDeployments")
	assert.Contains(t, names, "listTemplates")
	assert.Contains(t, names, "describeTemplate")
	assert.Contains(t, names, "getTemplateSource")
	assert.Contains(t, names, "getResourceOutputs")
	assert.Contains(t, names, "getDrift")
	assert.Contains(t, names, "planDeployment")
	assert.Contains(t, names, "listWorkspaces")

	// Write tools
	assert.Contains(t, names, "applyTemplate")
	assert.Contains(t, names, "deployTemplate")
	assert.Contains(t, names, "deleteDeployment")
	assert.Contains(t, names, "importResource")

	// Explain tools
	assert.Contains(t, names, "explainError")
	assert.Contains(t, names, "explainResource")
	assert.Contains(t, names, "suggestFix")

	// Migration tools
	assert.Contains(t, names, "migrateTerraform")
	assert.Contains(t, names, "migrateCloudFormation")
	assert.Contains(t, names, "migrateCrossplane")
	assert.Contains(t, names, "validateTemplate")
}

func TestToolRegistryGet(t *testing.T) {
	r := NewToolRegistry()

	tool := r.Get("getDeploymentStatus")
	require.NotNil(t, tool)
	assert.Equal(t, "getDeploymentStatus", tool.Name)
	assert.False(t, tool.RequiresApproval)

	tool = r.Get("deployTemplate")
	require.NotNil(t, tool)
	assert.True(t, tool.RequiresApproval)

	assert.Nil(t, r.Get("nonexistent"))
}

func TestToolRegistryDefinitions(t *testing.T) {
	r := NewToolRegistry()
	defs := r.Definitions()

	assert.NotEmpty(t, defs)
	for _, d := range defs {
		assert.NotEmpty(t, d.Name)
		assert.NotEmpty(t, d.Description)
	}
}

func TestWriteToolsRequireApproval(t *testing.T) {
	r := NewToolRegistry()
	writeTools := []string{"applyTemplate", "deployTemplate", "deleteDeployment", "importResource"}

	for _, name := range writeTools {
		tool := r.Get(name)
		require.NotNil(t, tool, "tool %s should exist", name)
		assert.True(t, tool.RequiresApproval, "tool %s should require approval", name)
	}
}

func TestReadToolsDontRequireApproval(t *testing.T) {
	r := NewToolRegistry()
	readTools := []string{"getDeploymentStatus", "listDeployments", "listTemplates", "planDeployment"}

	for _, name := range readTools {
		tool := r.Get(name)
		require.NotNil(t, tool, "tool %s should exist", name)
		assert.False(t, tool.RequiresApproval, "tool %s should not require approval", name)
	}
}
