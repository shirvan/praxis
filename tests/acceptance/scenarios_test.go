//go:build acceptance

package acceptance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/pkg/types"
)

// managedDeploymentScenario exercises one deployment through the operator
// boundary and verifies its effects independently through the provider API.
// Resources maps template-local names to their expected production drivers.
// Dependencies is the exact DAG expected from plan evaluation.
type managedDeploymentScenario struct {
	DeploymentKey string
	Template      string
	Resources     map[string]string
	Dependencies  map[string][]string
	AssertAbsent  func(*testing.T)
	AssertPresent func(*testing.T, types.DeploymentDetail)
}

func (env *topology) runManagedDeploymentScenario(t *testing.T, scenario managedDeploymentScenario) {
	t.Helper()
	require.NotEmpty(t, scenario.DeploymentKey)
	require.NotEmpty(t, scenario.Template)
	require.NotEmpty(t, scenario.Resources)

	templatePath := filepath.Join(t.TempDir(), "scenario.cue")
	require.NoError(t, os.WriteFile(templatePath, []byte(scenario.Template), 0o600))

	if scenario.AssertAbsent != nil {
		scenario.AssertAbsent(t)
	}

	var plan types.PlanResponse
	env.runCLIJSON(t, &plan, "plan", templatePath, "--account", "local", "--key", scenario.DeploymentKey)
	require.NotNil(t, plan.Plan)
	assert.Equal(t, len(scenario.Resources), plan.Plan.Summary.ToCreate)
	assert.Zero(t, plan.Plan.Summary.ToUpdate)
	assert.Zero(t, plan.Plan.Summary.ToDelete)
	assertPlanGraph(t, plan.Graph, scenario.Resources, scenario.Dependencies)
	if scenario.AssertAbsent != nil {
		scenario.AssertAbsent(t) // plan must remain read-only
	}

	cleanupNeeded := true
	t.Cleanup(func() {
		if !cleanupNeeded {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		_, _ = env.runCLIContext(ctx, "delete", "Deployment/"+scenario.DeploymentKey, "--yes", "--wait", "--timeout", "2m")
	})

	var deployed types.DeploymentDetail
	env.runCLIJSON(t, &deployed,
		"deploy", templatePath,
		"--account", "local",
		"--key", scenario.DeploymentKey,
		"--yes", "--wait",
		"--poll-interval", "100ms",
		"--timeout", "3m",
	)
	require.Equal(t, types.DeploymentComplete, deployed.Status, "deployment error: %s", deployed.Error)
	require.Len(t, deployed.Resources, len(scenario.Resources))
	assertDeploymentResources(t, deployed, scenario.Resources, scenario.Dependencies)
	if scenario.AssertPresent != nil {
		scenario.AssertPresent(t, deployed)
	}

	var inspected deploymentJSON
	env.runCLIJSON(t, &inspected, "get", "Deployment/"+scenario.DeploymentKey, "--all")
	assert.Equal(t, scenario.DeploymentKey, inspected.Deployment.Key)
	assert.Equal(t, types.DeploymentComplete, inspected.Deployment.Status)
	require.Len(t, inspected.Inputs, len(scenario.Resources))
	assertDeploymentResources(t, inspected.Deployment, scenario.Resources, scenario.Dependencies)

	var deleted types.DeploymentDetail
	env.runCLIJSON(t, &deleted,
		"delete", "Deployment/"+scenario.DeploymentKey,
		"--yes", "--wait", "--timeout", "3m",
	)
	require.Equal(t, types.DeploymentDeleted, deleted.Status, "delete error: %s", deleted.Error)
	if scenario.AssertAbsent != nil {
		scenario.AssertAbsent(t)
	}
	cleanupNeeded = false
}

func assertPlanGraph(t *testing.T, graph []types.GraphNode, resources map[string]string, dependencies map[string][]string) {
	t.Helper()
	require.Len(t, graph, len(resources), "plan graph must cover every resource")
	for _, node := range graph {
		expectedKind, ok := resources[node.Name]
		require.True(t, ok, "unexpected graph node %q", node.Name)
		assert.Equal(t, expectedKind, node.Kind, node.Name)
		assert.ElementsMatch(t, dependencies[node.Name], node.Dependencies, node.Name)
	}
}

func assertDeploymentResources(t *testing.T, detail types.DeploymentDetail, resources map[string]string, dependencies map[string][]string) {
	t.Helper()
	byName := make(map[string]types.DeploymentResource, len(detail.Resources))
	for _, resource := range detail.Resources {
		byName[resource.Name] = resource
	}
	for name, kind := range resources {
		resource, ok := byName[name]
		require.True(t, ok, "deployment is missing resource %q", name)
		assert.Equal(t, kind, resource.Kind, name)
		assert.Equal(t, types.DeploymentResourceReady, resource.Status, name)
		assert.NotEmpty(t, resource.Key, name)
		assert.NotEmpty(t, resource.Outputs, name)
		assert.ElementsMatch(t, dependencies[name], resource.DependsOn, name)
	}
}

func deploymentResource(t *testing.T, detail types.DeploymentDetail, name string) types.DeploymentResource {
	t.Helper()
	for _, resource := range detail.Resources {
		if resource.Name == name {
			return resource
		}
	}
	require.FailNow(t, fmt.Sprintf("deployment resource %q not found", name))
	return types.DeploymentResource{}
}

func outputString(t *testing.T, resource types.DeploymentResource, name string) string {
	t.Helper()
	value, ok := resource.Outputs[name].(string)
	require.True(t, ok, "%s output %q must be a string (got %T)", resource.Name, name, resource.Outputs[name])
	require.NotEmpty(t, value, "%s output %q", resource.Name, name)
	return value
}

func sortedStrings(values ...string) []string {
	sort.Strings(values)
	return values
}
