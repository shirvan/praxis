package orchestrator

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/praxiscloud/praxis/internal/core/dag"
	"github.com/praxiscloud/praxis/pkg/types"
)

func TestExecutionState_ApplyFailurePropagatesSkippedDependents(t *testing.T) {
	resources := []PlanResource{
		testPlanResource("network"),
		testPlanResource("db", "network"),
		testPlanResource("api", "db"),
		testPlanResource("frontend", "api"),
	}

	exec := newExecutionState(resources)
	graph, err := graphFromPlanResources(resources)
	require.NoError(t, err)
	schedule := dag.NewSchedule(graph)

	exec.markProvisioning("network")
	exec.markFailed("network", "aws create failed")
	skipped := exec.skipAffectedDependents(schedule, "network", "skipped because dependency network failed")

	assert.Equal(t, []string{"db", "api", "frontend"}, skipped)
	assert.Equal(t, types.DeploymentResourceError, exec.statuses["network"])
	assert.Equal(t, types.DeploymentResourceSkipped, exec.statuses["db"])
	assert.Equal(t, types.DeploymentResourceSkipped, exec.statuses["api"])
	assert.Equal(t, types.DeploymentResourceSkipped, exec.statuses["frontend"])
	assert.Equal(t, "aws create failed", exec.errors["network"])
	assert.Equal(t, "skipped because dependency network failed", exec.errors["db"])
}

func TestExecutionState_CancellationSkipsOnlyUndispatchedResources(t *testing.T) {
	resources := []PlanResource{
		testPlanResource("network"),
		testPlanResource("db", "network"),
		testPlanResource("api", "db"),
	}

	exec := newExecutionState(resources)
	exec.markReady("network", map[string]any{"id": "net-1"})
	exec.markProvisioning("db")

	skipped := exec.skipPendingForCancellation()

	assert.Equal(t, []string{"api"}, skipped)
	assert.Equal(t, types.DeploymentResourceReady, exec.statuses["network"])
	assert.Equal(t, types.DeploymentResourceProvisioning, exec.statuses["db"])
	assert.Equal(t, types.DeploymentResourceSkipped, exec.statuses["api"])
	assert.Equal(t, "skipped because deployment cancellation was requested", exec.errors["api"])
}

func TestExecutionState_DeleteFailureBlocksDependencies(t *testing.T) {
	resources := []PlanResource{
		testPlanResource("network"),
		testPlanResource("db", "network"),
		testPlanResource("api", "db"),
		testPlanResource("assets"),
	}

	exec := newExecutionState(resources)
	exec.markDeleted("assets")
	exec.markDeleting("api")
	exec.markFailed("api", "security group still attached")
	skipped := exec.skipDependencies("api", "skipped because dependent api failed to delete")

	assert.Equal(t, []string{"network", "db"}, skipped)
	assert.Equal(t, types.DeploymentResourceDeleted, exec.statuses["assets"])
	assert.Equal(t, types.DeploymentResourceError, exec.statuses["api"])
	assert.Equal(t, types.DeploymentResourceSkipped, exec.statuses["db"])
	assert.Equal(t, types.DeploymentResourceSkipped, exec.statuses["network"])
	assert.Equal(t, "skipped because dependent api failed to delete", exec.errors["db"])
}

func TestExecutionState_ResultProducesStablePublicResources(t *testing.T) {
	resources := []PlanResource{
		testPlanResource("network"),
		testPlanResource("db", "network"),
	}

	exec := newExecutionState(resources)
	exec.markReady("network", map[string]any{"vpcId": "vpc-123"})
	exec.markFailed("db", "invalid subnet")

	result := exec.result("deployment-a", types.DeploymentFailed, exec.failureSummary())
	require.Len(t, result.Resources, 2)

	assert.Equal(t, "network", result.Resources[0].Name)
	assert.Equal(t, types.DeploymentResourceReady, result.Resources[0].Status)
	assert.Equal(t, map[string]any{"vpcId": "vpc-123"}, result.Resources[0].Outputs)

	assert.Equal(t, "db", result.Resources[1].Name)
	assert.Equal(t, types.DeploymentResourceError, result.Resources[1].Status)
	assert.Equal(t, "invalid subnet", result.Resources[1].Error)
	assert.Equal(t, "db: invalid subnet", result.Error)
}

func TestPlanResourcesFromState_RebuildsDeleteGraphInputs(t *testing.T) {
	state := &DeploymentState{
		Key:    "deployment-a",
		Status: types.DeploymentComplete,
		Resources: map[string]*ResourceState{
			"db": {
				Name:      "db",
				Kind:      "SecurityGroup",
				Key:       "vpc-1~db",
				DependsOn: []string{"network"},
			},
			"network": {
				Name: "network",
				Kind: "SecurityGroup",
				Key:  "vpc-1~network",
			},
		},
		Outputs: map[string]map[string]any{
			"network": {"vpcId": "vpc-123"},
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	resources := planResourcesFromState(state)
	require.Len(t, resources, 2)
	assert.Equal(t, "db", resources[0].Name)
	assert.Equal(t, []string{"network"}, resources[0].Dependencies)
	assert.Equal(t, "network", resources[1].Name)
}

func TestStateResourcesToPublic_ProjectsOutputsAndDependencies(t *testing.T) {
	state := &DeploymentState{
		Key:    "deployment-a",
		Status: types.DeploymentComplete,
		Resources: map[string]*ResourceState{
			"db": {
				Name:      "db",
				Kind:      "SecurityGroup",
				Key:       "vpc-1~db",
				DependsOn: []string{"network"},
				Status:    types.DeploymentResourceReady,
			},
			"network": {
				Name:   "network",
				Kind:   "SecurityGroup",
				Key:    "vpc-1~network",
				Status: types.DeploymentResourceReady,
			},
		},
		Outputs: map[string]map[string]any{
			"db":      {"groupId": "sg-123"},
			"network": {"vpcId": "vpc-123"},
		},
	}

	resources := stateResourcesToPublic(state)
	require.Len(t, resources, 2)
	assert.Equal(t, "db", resources[0].Name)
	assert.Equal(t, []string{"network"}, resources[0].DependsOn)
	assert.Equal(t, map[string]any{"groupId": "sg-123"}, resources[0].Outputs)
	assert.Equal(t, "network", resources[1].Name)
}

func TestGraphFromPlanResources_HandlesNilSpecForDeleteFlows(t *testing.T) {
	resources := []PlanResource{
		{
			Name:         "network",
			Kind:         "SecurityGroup",
			Key:          "vpc-1~network",
			Dependencies: nil,
			Spec:         nil,
		},
		{
			Name:         "db",
			Kind:         "SecurityGroup",
			Key:          "vpc-1~db",
			Dependencies: []string{"network"},
			Spec:         nil,
		},
	}

	graph, err := graphFromPlanResources(resources)
	require.NoError(t, err)
	assert.Equal(t, []string{"network", "db"}, graph.TopologicalOrder())
	assert.Equal(t, []string{"db", "network"}, graph.ReverseTopo())
}

func testPlanResource(name string, deps ...string) PlanResource {
	return PlanResource{
		Name:          name,
		Kind:          "SecurityGroup",
		DriverService: "SecurityGroup",
		Key:           name,
		Dependencies:  deps,
		Spec:          json.RawMessage(`{"spec":{}}`),
	}
}
