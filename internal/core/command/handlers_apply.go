// handlers_apply.go implements the Apply handler, which is the primary
// deployment entry point for the Praxis CLI's `praxis apply` command.
//
// Apply is the most permissive deployment path: it accepts either an inline
// CUE template or a template registry reference, evaluates the full pipeline
// (template → data sources → SSM → DAG → plan resources), and then submits
// an asynchronous deployment workflow via Restate.
//
// In contrast to Deploy (handlers_deploy.go), Apply does NOT require the
// template to be pre-registered or have a variable schema. This makes it
// suitable for ad-hoc deployments and development workflows.
package command

import (
	"strings"

	restate "github.com/restatedev/sdk-go"
)

// Apply evaluates the template, initializes durable deployment state, and then
// asynchronously starts the deployment workflow.
//
// The handler is synchronous from the caller's perspective: it returns as soon
// as the deployment has been enqueued and durable state initialized. The actual
// cloud resource provisioning occurs later inside the DeploymentWorkflow.
//
// Flow:
//  1. Resolve workspace defaults and merge variables (account, vars).
//  2. compileTemplate — evaluate CUE, resolve data sources & SSM, build DAG.
//  3. Derive or validate the deployment key.
//  4. submitDeployment — initialise DeploymentStateObj, update index, emit
//     CloudEvents, and send the async workflow.
//
// Error handling:
//   - Validation / input errors → TerminalError (no retry).
//   - Infrastructure errors from Restate calls → plain error (auto-retry).
func (s *PraxisCommandService) Apply(ctx restate.Context, req ApplyRequest) (ApplyResponse, error) {
	// Step 1: Resolve the effective account name and merge workspace-level
	// variable defaults with the request-level overrides.
	account, mergedVars, err := s.resolveWorkspaceDefaults(ctx, req.Account, req.Workspace, req.Variables)
	if err != nil {
		return ApplyResponse{}, restate.TerminalError(err, 400)
	}

	// Step 2: Run the full template evaluation pipeline. This is the
	// expensive synchronous work — CUE evaluation, data source lookups,
	// SSM parameter resolution, and DAG construction. Any failure here
	// is typically a TerminalError (bad template, missing resource, etc.).
	compiled, err := s.compileTemplate(ctx, req.Template, req.TemplateRef, mergedVars, account, req.Targets, req.TemplatePath)
	if err != nil {
		return ApplyResponse{}, err
	}

	// Step 3: If the caller didn't provide an explicit deployment key,
	// derive one from the rendered template (first resource's kind + name).
	deploymentKey := strings.TrimSpace(req.DeploymentKey)
	if deploymentKey == "" {
		deploymentKey, err = deriveDeploymentKey(compiled.Specs)
		if err != nil {
			return ApplyResponse{}, restate.TerminalError(err, 400)
		}
	}

	// Step 4: Submit the deployment — init state, update global index,
	// emit audit events, and send the workflow. After this returns, the
	// deployment is durable and will proceed even if this handler crashes.
	key, status, err := s.submitDeployment(ctx, deploymentKey, account, req.Workspace, mergedVars, compiled, len(req.Targets) == 0, req.OrphanRemoved, req.Replace, req.AllowReplace, req.MaxParallelism, req.MaxRetries)
	if err != nil {
		return ApplyResponse{}, err
	}

	return ApplyResponse{
		DeploymentKey: key,
		Status:        status,
	}, nil
}
