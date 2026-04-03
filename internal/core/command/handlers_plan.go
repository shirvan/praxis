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

	corediff "github.com/shirvan/praxis/internal/core/diff"
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

	compiled, err := s.compileTemplate(ctx, req.Template, req.TemplateRef, mergedVars, account, req.Targets)
	if err != nil {
		return PlanResponse{}, err
	}

	// Walk the plan resources in topological order and compute per-resource
	// diffs. Each adapter.Plan call makes a read-only API call to the cloud
	// provider (wrapped in restate.Run inside the adapter) to compare the
	// desired spec against the current state.
	plan := corediff.NewPlanResult()
	for i := range compiled.PlanResources {
		resource := &compiled.PlanResources[i]
		adapter, err := s.providers.Get(resource.Kind)
		if err != nil {
			return PlanResponse{}, restate.TerminalError(err, 400)
		}

		// Decode the raw JSON spec into the adapter's typed Go struct so
		// the adapter can do a structured comparison.
		desiredSpec, err := adapter.DecodeSpec(resource.Spec)
		if err != nil {
			return PlanResponse{}, restate.TerminalError(err, 400)
		}

		// adapter.Plan returns the planned operation (create/update/noop)
		// and the individual field-level diffs.
		op, fields, err := adapter.Plan(ctx, resource.Key, account, desiredSpec)
		if err != nil {
			return PlanResponse{}, err
		}

		// Apply lifecycle.ignoreChanges: if the template declares that
		// certain fields should not trigger updates (e.g., tags managed
		// externally), remove those diffs. If all diffs are removed, the
		// operation downgrades from Update to NoOp.
		if resource.Lifecycle != nil && len(resource.Lifecycle.IgnoreChanges) > 0 {
			fields = filterIgnoredFields(fields, resource.Lifecycle.IgnoreChanges)
			if op == types.OpUpdate && len(fields) == 0 {
				op = types.OpNoOp
			}
		}

		corediff.Add(plan, resource.Kind, resource.Key, op, fields)
	}

	return PlanResponse{
		Plan:        plan,
		Rendered:    compiled.Rendered,
		DataSources: compiled.DataSources,
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
