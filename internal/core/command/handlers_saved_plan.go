package command

import (
	"encoding/json"
	"fmt"
	"strings"

	restate "github.com/restatedev/sdk-go"
	"github.com/shirvan/praxis/internal/core/orchestrator"
	"github.com/shirvan/praxis/pkg/types"
)

// ApplySavedPlan submits a previously saved, workflow-ready execution plan
// without re-evaluating template source.
func (s *PraxisCommandService) ApplySavedPlan(ctx restate.Context, req ApplySavedPlanRequest) (DeployResponse, error) {
	if strings.TrimSpace(req.Plan.DeploymentKey) == "" {
		return DeployResponse{}, restate.TerminalError(fmt.Errorf("saved plan deployment key is required"), 400)
	}
	if len(req.Plan.Resources) == 0 {
		return DeployResponse{}, restate.TerminalError(fmt.Errorf("saved plan contains no resources"), 400)
	}

	key, status, err := s.submitExecutionPlan(ctx, req.Plan, req.OrphanRemoved, req.MaxParallelism, req.MaxRetries)
	if err != nil {
		return DeployResponse{}, err
	}

	return DeployResponse{DeploymentKey: key, Status: status}, nil
}

func (s *PraxisCommandService) submitExecutionPlan(
	ctx restate.Context,
	plan types.ExecutionPlan,
	orphanRemoved bool,
	maxParallelism int,
	maxRetries *int,
) (string, types.DeploymentStatus, error) {
	return s.submitPlanResources(
		ctx,
		plan.DeploymentKey,
		plan.Account,
		plan.Workspace,
		plan.Variables,
		executionPlanToPlanResources(plan.Resources),
		plan.TemplatePath,
		len(plan.Targets) == 0,
		orphanRemoved,
		nil,
		false,
		maxParallelism,
		maxRetries,
	)
}

func executionPlanToPlanResources(resources []types.ExecutionPlanResource) []orchestrator.PlanResource {
	if len(resources) == 0 {
		return nil
	}
	out := make([]orchestrator.PlanResource, 0, len(resources))
	for i := range resources {
		resource := resources[i]
		out = append(out, orchestrator.PlanResource{
			Name:          resource.Name,
			Kind:          resource.Kind,
			DriverService: resource.DriverService,
			Key:           resource.Key,
			Spec:          append(json.RawMessage(nil), resource.Spec...),
			Dependencies:  append([]string(nil), resource.Dependencies...),
			Expressions:   cloneStringMap(resource.Expressions),
			Lifecycle:     cloneLifecycle(resource.Lifecycle),
		})
	}
	return out
}
