// plan_diff.go contains the shared per-resource diff logic used by both
// the Plan and PlanDeploy handlers.
//
// The core challenge: resources whose specs contain ${resources.X.outputs.Y}
// expressions have unresolved placeholders at plan time. Without resolution,
// the adapter's Plan() method cannot compare desired state against cloud state.
//
// The solution: after planning each resource that already exists in the cloud,
// we collect its live outputs from the driver's virtual-object state. These
// accumulated outputs are used to hydrate downstream expression-bearing
// resources. This avoids the need to look up a prior deployment by key and
// works correctly regardless of how the original deployment was created.
//
// If a dependency hasn't been provisioned (first deploy) or its outputs are
// unavailable, expression resources correctly fall back to OpCreate.
package command

import (
	"encoding/json"
	"fmt"
	"strings"

	restate "github.com/restatedev/sdk-go"

	corediff "github.com/shirvan/praxis/internal/core/diff"
	"github.com/shirvan/praxis/internal/core/orchestrator"
	"github.com/shirvan/praxis/pkg/types"
)

// computeResourceDiffs walks the compiled plan resources in topological order
// and computes per-resource diffs. As each resource is planned, its live outputs
// are collected from the driver and made available for hydrating downstream
// expression-bearing resources. This eliminates the dependency on prior
// deployment state for expression resolution.
func (s *PraxisCommandService) computeResourceDiffs(
	ctx restate.Context,
	resources []orchestrator.PlanResource,
	account string,
	priorOutputs map[string]map[string]any,
	removed []orchestrator.ResourceState,
) (*types.PlanResult, error) {
	plan := corediff.NewPlanResult()

	// Determine which resource names are referenced by expressions so we
	// only collect outputs for resources that downstream dependents need.
	referenced := referencedResourceNames(resources)

	// liveOutputs accumulates outputs from resources that have already been
	// planned and found to exist in the cloud. Keyed by template resource name.
	liveOutputs := make(map[string]map[string]any)

	// Seed with any prior deployment outputs so we have a fallback for
	// resources whose drivers haven't stored state yet.
	for k, v := range priorOutputs {
		liveOutputs[k] = v
	}

	for i := range resources {
		resource := &resources[i]

		if len(resource.Expressions) > 0 {
			op, fields, resolvedKey, err := s.planExpressionResource(ctx, resource, account, liveOutputs)
			if err != nil {
				return nil, err
			}

			displayKey := resource.Key
			if resolvedKey != "" {
				displayKey = resolvedKey
			}
			corediff.Add(plan, resource.Kind, displayKey, op, fields)

			// Collect outputs using the ORIGINAL key (resource.Key) because the
			// deployment workflow provisions driver VOs at the unresolved key.
			if op != types.OpCreate && referenced[resource.Name] {
				s.collectLiveOutputs(ctx, resource.Kind, resource.Name, resource.Key, liveOutputs)
			}
			continue
		}

		adapter, err := s.providers.Get(resource.Kind)
		if err != nil {
			return nil, restate.TerminalError(err, 400)
		}

		desiredSpec, err := adapter.DecodeSpec(resource.Spec)
		if err != nil {
			return nil, restate.TerminalError(err, 400)
		}

		op, fields, err := adapter.Plan(ctx, resource.Key, account, desiredSpec)
		if err != nil {
			return nil, err
		}

		if resource.Lifecycle != nil && len(resource.Lifecycle.IgnoreChanges) > 0 {
			fields = filterIgnoredFields(fields, resource.Lifecycle.IgnoreChanges)
			if op == types.OpUpdate && len(fields) == 0 {
				op = types.OpNoOp
			}
		}

		corediff.Add(plan, resource.Kind, resource.Key, op, fields)

		// Collect outputs from existing resources for downstream expression resolution.
		if op != types.OpCreate && referenced[resource.Name] {
			s.collectLiveOutputs(ctx, resource.Kind, resource.Name, resource.Key, liveOutputs)
		}
	}
	for i := range removed {
		resource := removed[i]
		corediff.Add(plan, resource.Kind, resource.Key, types.OpDelete, nil)
	}

	return plan, nil
}

// referencedResourceNames returns the set of template resource names that are
// referenced by at least one expression in any resource. Only these resources
// need their outputs collected for downstream hydration.
func referencedResourceNames(resources []orchestrator.PlanResource) map[string]bool {
	refs := make(map[string]bool)
	for _, r := range resources {
		for _, expr := range r.Expressions {
			// Expression format: "resources.<name>.outputs.<field>"
			parts := strings.Split(expr, ".")
			if len(parts) >= 2 && parts[0] == "resources" {
				refs[parts[1]] = true
			}
		}
	}
	return refs
}

// collectLiveOutputs reads a driver's current outputs via its GetOutputs
// handler and stores them in the liveOutputs map for downstream expression
// hydration. Failures are non-fatal: if outputs can't be read, downstream
// expression resources will fall back to OpCreate.
func (s *PraxisCommandService) collectLiveOutputs(
	ctx restate.Context,
	kind string,
	resourceName string,
	key string,
	liveOutputs map[string]map[string]any,
) {
	adapter, err := s.providers.Get(kind)
	if err != nil {
		return
	}

	// Call the driver's GetOutputs handler. The response is deserialized as
	// map[string]any since each driver returns a different typed struct, but
	// the JSON wire format is compatible with a generic map.
	outputs, err := restate.Object[map[string]any](
		ctx, adapter.ServiceName(), key, "GetOutputs",
	).Request(restate.Void{})
	if err != nil {
		ctx.Log().Info("plan: could not collect live outputs", "resource", resourceName, "kind", kind, "error", err)
		return
	}
	if len(outputs) == 0 {
		return
	}

	liveOutputs[resourceName] = outputs
}

// planExpressionResource handles the plan for a resource that has unresolved
// ${resources.X.outputs.Y} expressions. If live outputs from upstream resources
// are available, it hydrates the spec and key, then calls the adapter's Plan
// method for an accurate diff. Otherwise it falls back to OpCreate.
//
// Returns the resolved key (empty on fallback) so the caller can use it for
// display and for collecting this resource's own outputs.
func (s *PraxisCommandService) planExpressionResource(
	ctx restate.Context,
	resource *orchestrator.PlanResource,
	account string,
	liveOutputs map[string]map[string]any,
) (types.DiffOperation, []types.FieldDiff, string, error) {
	// No outputs available — cannot resolve expressions.
	if len(liveOutputs) == 0 {
		ctx.Log().Info("plan: no outputs available for expression resource, showing as create",
			"resource", resource.Name, "kind", resource.Kind)
		fields := corediff.FieldDiffsFromJSON(resource.Spec)
		return types.OpCreate, fields, "", nil
	}

	// Try to hydrate expressions using accumulated outputs.
	hydratedSpec, err := orchestrator.HydrateExprs(resource.Spec, resource.Expressions, liveOutputs)
	if err != nil {
		ctx.Log().Info("plan: expression hydration failed, showing as create",
			"resource", resource.Name, "kind", resource.Kind, "error", err)
		fields := corediff.FieldDiffsFromJSON(resource.Spec)
		return types.OpCreate, fields, "", nil
	}

	adapter, err := s.providers.Get(resource.Kind)
	if err != nil {
		return "", nil, "", restate.TerminalError(err, 400)
	}

	// Build the resolved key from the hydrated spec for DISPLAY purposes.
	// The adapter's Plan call uses resource.Key (the original unresolved key)
	// because the deployment workflow stores driver VO state at that key.
	resolvedKey, err := adapter.BuildKey(hydratedSpec)
	if err != nil {
		ctx.Log().Info("plan: BuildKey failed on hydrated spec, showing as create",
			"resource", resource.Name, "kind", resource.Kind, "error", err)
		fields := corediff.FieldDiffsFromJSON(resource.Spec)
		return types.OpCreate, fields, "", nil
	}

	desiredSpec, err := adapter.DecodeSpec(hydratedSpec)
	if err != nil {
		ctx.Log().Info("plan: DecodeSpec failed on hydrated spec, showing as create",
			"resource", resource.Name, "kind", resource.Kind, "error", err)
		fields := corediff.FieldDiffsFromJSON(resource.Spec)
		return types.OpCreate, fields, "", nil
	}

	// Use the ORIGINAL key (resource.Key) for the adapter.Plan call. The
	// deployment workflow provisions driver VOs at the unresolved key
	// (e.g., "${resources.vpc.outputs.vpcId}~e2e-prod-app-a"), not the
	// resolved key. The adapter needs the VO key to call GetOutputs.
	op, fields, err := adapter.Plan(ctx, resource.Key, account, desiredSpec)
	if err != nil {
		return "", nil, "", err
	}

	// Apply lifecycle.ignoreChanges if present.
	if resource.Lifecycle != nil && len(resource.Lifecycle.IgnoreChanges) > 0 {
		fields = filterIgnoredFields(fields, resource.Lifecycle.IgnoreChanges)
		if op == types.OpUpdate && len(fields) == 0 {
			op = types.OpNoOp
		}
	}

	// Annotate field diffs that reference expressions with the expression
	// syntax in their old/new values so the user sees the template reference.
	fields = annotateExpressionFields(fields, resource.Expressions)

	return op, fields, resolvedKey, nil
}

// annotateExpressionFields replaces resolved values in field diffs with the
// original expression syntax for fields that contain ${...} references. This
// preserves the user's view of what the template declares while showing the
// correct operation type.
func annotateExpressionFields(fields []types.FieldDiff, exprs map[string]string) []types.FieldDiff {
	if len(exprs) == 0 {
		return fields
	}

	// Build a lookup of spec paths → expression strings.
	// Expression paths are like "spec.vpcId" in the expressions map.
	exprByPath := make(map[string]string, len(exprs))
	for path, expr := range exprs {
		// Strip leading "spec." since field diffs use "spec.X" paths.
		exprByPath[path] = "${" + expr + "}"
	}

	for i := range fields {
		fd := &fields[i]
		// Check if this field's path matches an expression.
		if exprStr, ok := exprByPath[fd.Path]; ok {
			fd.NewValue = exprStr
		} else {
			// Check parent paths for nested expression fields.
			for exprPath, exprStr := range exprByPath {
				if strings.HasPrefix(fd.Path, exprPath+".") || strings.HasPrefix(fd.Path, exprPath+"[") {
					fd.NewValue = exprStr
					break
				}
			}
		}
	}
	return fields
}

// fetchPriorOutputs retrieves the outputs from a previous deployment. The
// deployment key is either explicitly provided or auto-derived from the
// rendered template specs. Returns nil outputs (not an error) if no prior
// deployment exists, along with any diagnostic warnings.
func (s *PraxisCommandService) fetchPriorOutputs(
	ctx restate.Context,
	deploymentKey string,
	specs map[string]json.RawMessage,
) (map[string]map[string]any, *orchestrator.DeploymentState, []string, error) {
	var warnings []string

	// Derive the deployment key if not explicitly provided.
	if deploymentKey == "" {
		derived, err := deriveDeploymentKey(specs)
		if err != nil {
			ctx.Log().Info("plan: cannot derive deployment key for prior state lookup", "error", err)
			warnings = append(warnings, fmt.Sprintf(
				"Could not derive deployment key from template: %v. Prior deployment outputs will not be used as fallback.",
				err))
			return nil, nil, warnings, nil
		}
		deploymentKey = derived
	}

	ctx.Log().Info("plan: looking up prior deployment state", "deploymentKey", deploymentKey)

	state, err := restate.Object[*orchestrator.DeploymentState](
		ctx, orchestrator.DeploymentStateServiceName, deploymentKey, "GetState",
	).Request(restate.Void{})
	if err != nil {
		ctx.Log().Warn("plan: failed to fetch prior deployment state", "deploymentKey", deploymentKey, "error", err)
		warnings = append(warnings, fmt.Sprintf(
			"Failed to fetch prior deployment state for key %q: %v. Expression-bearing resources will show as create. Use --key to specify the correct deployment key.",
			deploymentKey, err))
		return nil, nil, warnings, nil
	}
	if state == nil {
		ctx.Log().Info("plan: no prior deployment state found", "deploymentKey", deploymentKey)
		warnings = append(warnings, fmt.Sprintf(
			"No prior deployment found for key %q (auto-derived). Expression-bearing resources will show as create. If this stack was deployed with --key, pass the same key to plan.",
			deploymentKey))
		return nil, nil, warnings, nil
	}
	if len(state.Outputs) == 0 {
		ctx.Log().Info("plan: prior deployment exists but has no resource outputs", "deploymentKey", deploymentKey, "status", state.Status)
		warnings = append(warnings, fmt.Sprintf(
			"Prior deployment %q exists (status: %s) but has no resource outputs. Expression-bearing resources will show as create.",
			deploymentKey, state.Status))
		return nil, state, warnings, nil
	}

	ctx.Log().Info("plan: loaded prior deployment outputs", "deploymentKey", deploymentKey, "resourceCount", len(state.Outputs))
	return state.Outputs, state, nil, nil
}
