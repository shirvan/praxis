// handlers_plan_deploy.go implements the PlanDeploy handler, which is the
// dry-run counterpart to the Deploy handler. It requires a pre-registered
// template and validates variables against the template's declared schema
// before running the plan pipeline.
//
// PlanDeploy differs from Plan (handlers_plan.go) in two ways:
//  1. It requires a registered template name (not inline CUE).
//  2. It validates API-supplied variables against the template's variable
//     schema before evaluation, giving faster feedback on invalid input.
//
// Like Plan, PlanDeploy never mutates durable state or submits a workflow.
package command

import (
	"fmt"
	"strings"

	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/registry"
	"github.com/shirvan/praxis/internal/core/template"
	"github.com/shirvan/praxis/pkg/types"
)

// PlanDeploy is the dry-run variant of Deploy. It validates variables against
// the registered template's schema, runs the full pipeline, and returns the
// plan diff without submitting a workflow.
//
// Flow:
//  1. Fetch the variable schema from the template registry (shared handler,
//     no exclusive lock on the virtual object).
//  2. Validate request variables against the schema (fast preflight).
//  3. Resolve workspace defaults and compile the template.
//  4. Compute per-resource diffs via provider adapters.
func (s *PraxisCommandService) PlanDeploy(ctx restate.Context, req PlanDeployRequest) (PlanDeployResponse, error) {
	templateName := strings.TrimSpace(req.Template)
	if templateName == "" {
		return PlanDeployResponse{}, restate.TerminalError(
			fmt.Errorf("template name is required"), 400)
	}

	// Fetch variable schema from the registry virtual object via a shared
	// (read-only) handler. This does not acquire the per-key lock, so
	// multiple PlanDeploy calls for the same template can run concurrently.
	schema, err := restate.Object[types.VariableSchema](
		ctx, registry.TemplateRegistryServiceName, templateName, "GetVariableSchema",
	).Request(restate.Void{})
	if err != nil {
		return PlanDeployResponse{}, err
	}

	// Fast preflight validation: reject bad variables before the expensive
	// CUE evaluation pipeline. This catches missing required variables and
	// type mismatches early.
	if err := template.ValidateVariables(schema, req.Variables); err != nil {
		return PlanDeployResponse{}, restate.TerminalError(err, 400)
	}

	account, mergedVars, err := s.resolveWorkspaceDefaults(ctx, req.Account, req.Workspace, req.Variables)
	if err != nil {
		return PlanDeployResponse{}, restate.TerminalError(err, 400)
	}

	// Compile using a TemplateRef so the pipeline fetches the source from
	// the registry rather than expecting an inline template body.
	compiled, err := s.compileTemplate(ctx, "", &types.TemplateRef{Name: templateName}, mergedVars, account, req.Targets, "")
	if err != nil {
		return PlanDeployResponse{}, err
	}

	// Look up prior deployment state so expression-bearing resources can be
	// compared against cloud state rather than blindly shown as "create".
	priorOutputs, warnings, err := s.fetchPriorOutputs(ctx, req.DeploymentKey, compiled.Specs)
	if err != nil {
		return PlanDeployResponse{}, err
	}

	// Per-resource diff loop with expression resolution from prior state.
	plan, err := s.computeResourceDiffs(ctx, compiled.PlanResources, account, priorOutputs)
	if err != nil {
		return PlanDeployResponse{}, err
	}

	return PlanDeployResponse{
		Plan:        plan,
		Rendered:    compiled.Rendered,
		DataSources: compiled.DataSources,
		Warnings:    warnings,
	}, nil
}
