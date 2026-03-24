package command

import (
	"strings"

	restate "github.com/restatedev/sdk-go"

	corediff "github.com/shirvan/praxis/internal/core/diff"
	"github.com/shirvan/praxis/pkg/types"
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

		if resource.Lifecycle != nil && len(resource.Lifecycle.IgnoreChanges) > 0 {
			fields = filterIgnoredFields(fields, resource.Lifecycle.IgnoreChanges)
			if op == types.OpUpdate && len(fields) == 0 {
				op = types.OpNoOp
			}
		}

		corediff.Add(plan, resource.Kind, resource.Key, op, fields)
	}

	return PlanResponse{
		Plan:     plan,
		Rendered: compiled.Rendered,
	}, nil
}

// filterIgnoredFields removes field diffs whose paths match any of the
// ignoreChanges patterns. A pattern matches if the diff path equals the
// pattern or starts with it as a prefix (so "tags" ignores "tags.env").
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
// A pattern matches if it equals the path exactly or is a prefix followed by a dot.
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
