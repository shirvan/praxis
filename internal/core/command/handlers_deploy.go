// handlers_deploy.go implements the Deploy handler for `praxis deploy`.
//
// Deploy is the schema-validated deployment path. Unlike Apply, it requires
// a pre-registered template and validates the caller's variables against the
// template's declared variable schema before evaluation. This provides a
// stricter contract for production workflows and CI/CD pipelines.
//
// Aside from the upfront schema validation, Deploy follows the same pipeline
// as Apply: compileTemplate → deriveDeploymentKey → submitDeployment.
package command

import (
	"fmt"
	"strings"

	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/registry"
	"github.com/shirvan/praxis/internal/core/template"
	"github.com/shirvan/praxis/pkg/types"
)

// Deploy is the user-facing deployment entry point. It requires a pre-registered
// template and validates variables against the template's schema before running
// the full pipeline.
//
// Flow:
//  1. Fetch the template's variable schema from the registry.
//  2. Validate variables against the schema (fast preflight).
//  3. Resolve workspace defaults and compile the template.
//  4. Derive deployment key if not provided.
//  5. Submit the deployment workflow (identical to Apply's submission path).
func (s *PraxisCommandService) Deploy(ctx restate.Context, req DeployRequest) (DeployResponse, error) {
	templateName := strings.TrimSpace(req.Template)
	if templateName == "" {
		return DeployResponse{}, restate.TerminalError(
			fmt.Errorf("template name is required"), 400)
	}

	// Fetch variable schema from registry (shared handler — no lock).
	// The shared handler (ObjectSharedContext) avoids acquiring the per-key
	// lock so concurrent reads don't block each other.
	schema, err := restate.Object[types.VariableSchema](
		ctx, registry.TemplateRegistryServiceName, templateName, "GetVariableSchema",
	).Request(restate.Void{})
	if err != nil {
		return DeployResponse{}, err
	}

	// Fast preflight validation — reject bad variables before the CUE
	// pipeline. This catches missing required vars and type mismatches
	// without paying the cost of full CUE evaluation.
	if err := template.ValidateVariables(schema, req.Variables); err != nil {
		return DeployResponse{}, restate.TerminalError(err, 400)
	}

	account, mergedVars, err := s.resolveWorkspaceDefaults(ctx, req.Account, req.Workspace, req.Variables)
	if err != nil {
		return DeployResponse{}, restate.TerminalError(err, 400)
	}

	// Compile the template via the pipeline using a TemplateRef (the
	// registry lookup fetches the stored CUE source by name).
	compiled, err := s.compileTemplate(ctx, "", &types.TemplateRef{Name: templateName}, mergedVars, account, req.Targets)
	if err != nil {
		return DeployResponse{}, err
	}

	// Derive deployment key if not provided. The key is computed from the
	// first resource's kind and metadata.name, with "-stack" appended for
	// multi-resource templates.
	deploymentKey := strings.TrimSpace(req.DeploymentKey)
	if deploymentKey == "" {
		deploymentKey, err = deriveDeploymentKey(compiled.Specs)
		if err != nil {
			return DeployResponse{}, restate.TerminalError(err, 400)
		}
	}

	// Submit the deployment — from this point forward the path is identical
	// to Apply: init state, update index, emit events, send workflow.
	key, status, err := s.submitDeployment(ctx, deploymentKey, account, req.Workspace, mergedVars, compiled, req.Replace)
	if err != nil {
		return DeployResponse{}, err
	}

	return DeployResponse{
		DeploymentKey: key,
		Status:        status,
	}, nil
}
