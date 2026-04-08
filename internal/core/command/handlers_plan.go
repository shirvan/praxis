// handlers_plan.go implements the Plan handler for `praxis plan`.
//
// Plan is the dry-run counterpart to Apply. It runs the identical template
// evaluation pipeline (CUE → data sources → SSM → DAG) and then invokes
// each provider adapter's Plan method to compute a per-resource diff against
// current cloud state. The result is returned to the caller without any
// side effects — no durable state is created, no workflow is submitted, and
// no cloud resources are modified.
//
// This allows operators to preview exactly what Apply would do before
// committing.
package command

import (
	"strings"

	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/orchestrator"
	"github.com/shirvan/praxis/pkg/types"
)

// Plan runs the full rendering and validation pipeline but stops before any
// workflow submission or durable deployment-state mutation occurs.
//
// For each resource in the compiled template, Plan asks the provider adapter
// to compare desired state against current cloud state and returns the
// resulting diff (create / update / delete / no-op per resource).
//
// The response includes:
//   - Plan: structured per-resource diff with field-level changes.
//   - Rendered: the fully-resolved template (with SSM values masked).
//   - DataSources: resolved data source outputs for transparency.
func (s *PraxisCommandService) Plan(ctx restate.Context, req PlanRequest) (PlanResponse, error) {
	account, mergedVars, err := s.resolveWorkspaceDefaults(ctx, req.Account, req.Workspace, req.Variables)
	if err != nil {
		return PlanResponse{}, restate.TerminalError(err, 400)
	}

	compiled, err := s.compileTemplate(ctx, req.Template, req.TemplateRef, mergedVars, account, req.Targets, req.TemplatePath)
	if err != nil {
		return PlanResponse{}, err
	}

	// Look up prior deployment state so expression-bearing resources can be
	// compared against cloud state rather than blindly shown as "create".
	priorOutputs, priorState, warnings, err := s.fetchPriorOutputs(ctx, req.DeploymentKey, compiled.Specs)
	if err != nil {
		return PlanResponse{}, err
	}

	// Walk the plan resources in topological order and compute per-resource
	// diffs. Each adapter.Plan call makes a read-only API call to the cloud
	// provider (wrapped in restate.Run inside the adapter) to compare the
	// desired spec against the current state.
	var removed []orchestrator.ResourceState
	if len(req.Targets) == 0 {
		removed = missingDeletionsForPlan(priorState, compiled.PlanResources)
	}
	plan, err := s.computeResourceDiffs(ctx, compiled.PlanResources, account, priorOutputs, removed)
	if err != nil {
		return PlanResponse{}, err
	}

	deploymentKey := strings.TrimSpace(req.DeploymentKey)
	if deploymentKey == "" {
		deploymentKey, err = deriveDeploymentKey(compiled.Specs)
		if err != nil {
			return PlanResponse{}, restate.TerminalError(err, 400)
		}
	}

	// Build a lightweight graph description for the response so the CLI
	// can render the dependency DAG without reconstructing it.
	graphNodes := make([]types.GraphNode, len(compiled.Nodes))
	for i, node := range compiled.Nodes {
		graphNodes[i] = types.GraphNode{
			Name:         node.Name,
			Kind:         node.Kind,
			Dependencies: node.Dependencies,
		}
	}

	return PlanResponse{
		Plan:          plan,
		ExecutionPlan: executionPlanFromCompiled(compiled, deploymentKey, account, req.Workspace, mergedVars, req.Targets),
		Rendered:      compiled.Rendered,
		TemplateHash:  compiled.TemplateHash,
		DataSources:   compiled.DataSources,
		Graph:         graphNodes,
		Warnings:      warnings,
	}, nil
}

// filterIgnoredFields removes field diffs whose paths match any of the
// ignoreChanges patterns. A pattern matches if the diff path equals the
// pattern or starts with it as a prefix (so "tags" ignores "tags.env").
//
// This implements the lifecycle.ignoreChanges feature that lets operators
// exclude externally-managed fields from drift detection.
func filterIgnoredFields(fields []types.FieldDiff, ignoreChanges []string) []types.FieldDiff {
	filtered := make([]types.FieldDiff, 0, len(fields))
	for _, fd := range fields {
		if isIgnoredPath(fd.Path, ignoreChanges) {
			continue
		}
		filtered = append(filtered, fd)
	}
	return filtered
}

// isIgnoredPath returns true if the given path matches any ignore pattern.
// A pattern matches if it equals the path exactly or is a prefix followed
// by a dot. Example: pattern "tags" matches "tags" and "tags.env" but not
// "tagsExtra".
func isIgnoredPath(path string, patterns []string) bool {
	for _, pattern := range patterns {
		if path == pattern {
			return true
		}
		if strings.HasPrefix(path, pattern+".") {
			return true
		}
	}
	return false
}
