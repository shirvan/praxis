package command

import (
	restate "github.com/restatedev/sdk-go"

	corediff "github.com/shirvan/praxis/internal/core/diff"
)

// Plan runs the full rendering and validation pipeline but stops before any
// workflow submission or durable deployment-state mutation occurs.
func (s *PraxisCommandService) Plan(ctx restate.Context, req PlanRequest) (PlanResponse, error) {
	account, mergedVars, err := s.resolveWorkspaceDefaults(ctx, req.Account, req.Workspace, req.Variables)
	if err != nil {
		return PlanResponse{}, restate.TerminalError(err, 400)
	}

	compiled, err := s.compileTemplate(ctx, req.Template, req.TemplateRef, mergedVars, account, req.Targets)
	if err != nil {
		return PlanResponse{}, err
	}

	plan := corediff.NewPlanResult()
	for _, resource := range compiled.PlanResources {
		adapter, err := s.providers.Get(resource.Kind)
		if err != nil {
			return PlanResponse{}, restate.TerminalError(err, 400)
		}

		desiredSpec, err := adapter.DecodeSpec(resource.Spec)
		if err != nil {
			return PlanResponse{}, restate.TerminalError(err, 400)
		}

		op, fields, err := adapter.Plan(ctx, resource.Key, account, desiredSpec)
		if err != nil {
			return PlanResponse{}, err
		}

		corediff.Add(plan, resource.Kind, resource.Key, op, fields)
	}

	return PlanResponse{
		Plan:     plan,
		Rendered: compiled.Rendered,
	}, nil
}
