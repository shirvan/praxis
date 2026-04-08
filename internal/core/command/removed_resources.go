package command

import (
	"fmt"
	"sort"

	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/dag"
	"github.com/shirvan/praxis/internal/core/orchestrator"
	"github.com/shirvan/praxis/internal/core/provider"
	"github.com/shirvan/praxis/pkg/types"
)

func missingDeploymentResources(existing *orchestrator.DeploymentState, desired []orchestrator.PlanResource) []orchestrator.ResourceState {
	if existing == nil || len(existing.Resources) == 0 {
		return nil
	}

	desiredNames := make(map[string]bool, len(desired))
	for i := range desired {
		desiredNames[desired[i].Name] = true
	}

	missingByName := make(map[string]orchestrator.ResourceState)
	for name, resource := range existing.Resources {
		if resource == nil || desiredNames[name] {
			continue
		}
		dependsOn := append([]string(nil), resource.DependsOn...)
		sort.Strings(dependsOn)
		missingByName[name] = orchestrator.ResourceState{
			Name:          resource.Name,
			Kind:          resource.Kind,
			DriverService: resource.DriverService,
			Key:           resource.Key,
			DependsOn:     dependsOn,
			Status:        resource.Status,
			Error:         resource.Error,
			Lifecycle:     cloneLifecycle(resource.Lifecycle),
			PriorReady:    resource.PriorReady,
			Conditions:    append([]types.Condition(nil), resource.Conditions...),
		}
	}
	if len(missingByName) == 0 {
		return nil
	}

	nodes := make([]*types.ResourceNode, 0, len(existing.Resources))
	for _, resource := range existing.Resources {
		if resource == nil {
			continue
		}
		nodes = append(nodes, &types.ResourceNode{
			Name:         resource.Name,
			Kind:         resource.Kind,
			Key:          resource.Key,
			Dependencies: append([]string(nil), resource.DependsOn...),
		})
	}

	ordered := make([]orchestrator.ResourceState, 0, len(missingByName))
	graph, err := dag.NewGraph(nodes)
	if err == nil {
		for _, name := range graph.ReverseTopo() {
			if resource, ok := missingByName[name]; ok {
				ordered = append(ordered, resource)
			}
		}
		return ordered
	}

	names := make([]string, 0, len(missingByName))
	for name := range missingByName {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		ordered = append(ordered, missingByName[name])
	}
	return ordered
}

func (s *PraxisCommandService) cleanupMissingResources(
	ctx restate.Context,
	deploymentKey string,
	resources []orchestrator.ResourceState,
	orphanRemoved bool,
) error {
	for i := range resources {
		resource := resources[i]
		if shouldDetachMissingResource(resource, orphanRemoved) {
			if err := s.detachMissingResource(ctx, deploymentKey, resource.Name); err != nil {
				return err
			}
			continue
		}

		adapter, err := s.providers.Get(resource.Kind)
		if err != nil {
			return err
		}
		if preDeleter, ok := adapter.(provider.PreDeleter); ok {
			if err := preDeleter.PreDelete(ctx, resource.Key); err != nil {
				return fmt.Errorf("pre-delete removed resource %s: %w", resource.Name, err)
			}
		}
		invocation, err := adapter.Delete(ctx, resource.Key)
		if err != nil {
			return fmt.Errorf("dispatch delete for removed resource %s: %w", resource.Name, err)
		}
		if err := invocation.Done(); err != nil {
			return fmt.Errorf("delete removed resource %s: %w", resource.Name, err)
		}
		if postDeleter, ok := adapter.(provider.PostDeleter); ok {
			if err := postDeleter.PostDelete(ctx, resource.Key); err != nil {
				ctx.Log().Warn("post-delete hook failed for removed resource", "resource", resource.Name, "kind", resource.Kind, "error", err.Error())
			}
		}
		if err := s.detachMissingResource(ctx, deploymentKey, resource.Name); err != nil {
			return err
		}
	}
	return nil
}

func (s *PraxisCommandService) detachMissingResource(ctx restate.Context, deploymentKey, resourceName string) error {
	_, err := restate.WithRequestType[string, *orchestrator.ResourceState](
		restate.Object[*orchestrator.ResourceState](ctx, orchestrator.DeploymentStateServiceName, deploymentKey, "RemoveResource"),
	).Request(resourceName)
	if err != nil {
		return fmt.Errorf("remove %q from deployment %q: %w", resourceName, deploymentKey, err)
	}
	return nil
}

func shouldDetachMissingResource(resource orchestrator.ResourceState, orphanRemoved bool) bool {
	if orphanRemoved {
		return true
	}
	if resource.Lifecycle != nil && resource.Lifecycle.DeletionPolicy == types.DeletionPolicyOrphan {
		return true
	}
	switch resource.Status {
	case types.DeploymentResourcePending, types.DeploymentResourceDeleted, types.DeploymentResourceOrphaned:
		return true
	default:
		return false
	}
}

func missingDeletionsForPlan(existing *orchestrator.DeploymentState, desired []orchestrator.PlanResource) []orchestrator.ResourceState {
	missing := missingDeploymentResources(existing, desired)
	if len(missing) == 0 {
		return nil
	}
	filtered := missing[:0]
	for _, resource := range missing {
		if shouldDetachMissingResource(resource, false) {
			continue
		}
		filtered = append(filtered, resource)
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}
