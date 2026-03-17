package command

import (
	"fmt"
	"strings"

	restate "github.com/restatedev/sdk-go"

	corediff "github.com/praxiscloud/praxis/internal/core/diff"
	"github.com/praxiscloud/praxis/internal/core/registry"
	"github.com/praxiscloud/praxis/internal/core/template"
	"github.com/praxiscloud/praxis/pkg/types"
)

// PlanDeploy is the dry-run variant of Deploy. It validates variables against
// the registered template's schema, runs the full pipeline, and returns the
// plan diff without submitting a workflow.
func (s *PraxisCommandService) PlanDeploy(ctx restate.Context, req PlanDeployRequest) (PlanDeployResponse, error) {
	templateName := strings.TrimSpace(req.Template)
	if templateName == "" {
		return PlanDeployResponse{}, restate.TerminalError(
			fmt.Errorf("template name is required"), 400)
	}

	// Fetch variable schema from registry (shared handler — no lock).
	schema, err := restate.Object[types.VariableSchema](
		ctx, registry.TemplateRegistryServiceName, templateName, "GetVariableSchema",
	).Request(restate.Void{})
	if err != nil {
		return PlanDeployResponse{}, err
	}

	// Fast preflight validation.
	if err := template.ValidateVariables(schema, req.Variables); err != nil {
		return PlanDeployResponse{}, restate.TerminalError(err, 400)
	}

	account, err := s.resolveRequestAccount(req.Account, req.Variables)
	if err != nil {
		return PlanDeployResponse{}, restate.TerminalError(err, 400)
	}

	compiled, err := s.compileTemplate(ctx, "", &types.TemplateRef{Name: templateName}, req.Variables, account.Name)
	if err != nil {
		return PlanDeployResponse{}, err
	}

	plan := corediff.NewPlanResult()
	for _, resource := range compiled.PlanResources {
		adapter, err := s.providers.Get(resource.Kind)
		if err != nil {
			return PlanDeployResponse{}, restate.TerminalError(err, 400)
		}

		desiredSpec, err := adapter.DecodeSpec(resource.Spec)
		if err != nil {
			return PlanDeployResponse{}, restate.TerminalError(err, 400)
		}

		op, fields, err := adapter.Plan(ctx, resource.Key, account.Name, desiredSpec)
		if err != nil {
			return PlanDeployResponse{}, err
		}

		corediff.Add(plan, resource.Kind, resource.Key, op, fields)
	}

	return PlanDeployResponse{
		Plan:     plan,
		Rendered: compiled.Rendered,
	}, nil
}
