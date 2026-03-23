package orchestrator

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/dag"
	"github.com/shirvan/praxis/pkg/types"
)

type executionState struct {
	order      []string
	plan       map[string]PlanResource
	statuses   map[string]types.DeploymentResourceStatus
	errors     map[string]string
	outputs    map[string]map[string]any
	completed  map[string]bool
	dispatched map[string]bool
	failed     map[string]bool
	skipped    map[string]bool
}

func newExecutionState(resources []PlanResource) *executionState {
	state := &executionState{
		order:      make([]string, 0, len(resources)),
		plan:       make(map[string]PlanResource, len(resources)),
		statuses:   make(map[string]types.DeploymentResourceStatus, len(resources)),
		errors:     make(map[string]string, len(resources)),
		outputs:    make(map[string]map[string]any, len(resources)),
		completed:  make(map[string]bool, len(resources)),
		dispatched: make(map[string]bool, len(resources)),
		failed:     make(map[string]bool, len(resources)),
		skipped:    make(map[string]bool, len(resources)),
	}
	for _, resource := range resources {
		state.order = append(state.order, resource.Name)
		state.plan[resource.Name] = resource
		state.statuses[resource.Name] = types.DeploymentResourcePending
	}
	return state
}

func (s *executionState) loadOutputs(outputs map[string]map[string]any) {
	for name, outputMap := range outputs {
		s.outputs[name] = outputMap
	}
}

func (s *executionState) ready(schedule *dag.Schedule) []string {
	return schedule.Ready(s.completed, s.dispatched)
}

func (s *executionState) markProvisioning(name string) {
	s.statuses[name] = types.DeploymentResourceProvisioning
	s.dispatched[name] = true
}

func (s *executionState) markDeleting(name string) {
	s.statuses[name] = types.DeploymentResourceDeleting
	s.dispatched[name] = true
}

func (s *executionState) markReady(name string, outputs map[string]any) {
	s.statuses[name] = types.DeploymentResourceReady
	s.outputs[name] = outputs
	s.completed[name] = true
	delete(s.errors, name)
	delete(s.failed, name)
	delete(s.skipped, name)
}

func (s *executionState) markDeleted(name string) {
	s.statuses[name] = types.DeploymentResourceDeleted
	s.completed[name] = true
	delete(s.errors, name)
	delete(s.failed, name)
	delete(s.skipped, name)
}

func (s *executionState) markFailed(name, errMsg string) {
	s.statuses[name] = types.DeploymentResourceError
	s.errors[name] = errMsg
	s.failed[name] = true
	s.dispatched[name] = true
}

func (s *executionState) markSkipped(name, errMsg string) bool {
	if s.dispatched[name] || s.completed[name] || s.failed[name] || s.skipped[name] {
		return false
	}
	s.statuses[name] = types.DeploymentResourceSkipped
	s.errors[name] = errMsg
	s.skipped[name] = true
	return true
}

func (s *executionState) skipAffectedDependents(schedule *dag.Schedule, failed, errMsg string) []string {
	affected := schedule.AffectedByFailure(failed)
	skipped := make([]string, 0)
	for _, name := range affected {
		if s.markSkipped(name, errMsg) {
			skipped = append(skipped, name)
		}
	}
	return skipped
}

func (s *executionState) skipPendingForCancellation() []string {
	pending := make([]string, 0)
	for _, name := range s.order {
		if s.markSkipped(name, "skipped because deployment cancellation was requested") {
			pending = append(pending, name)
		}
	}
	return pending
}

func (s *executionState) dependencyClosure(root string) []string {
	visited := make(map[string]bool)
	var visit func(string)
	visit = func(name string) {
		for _, dep := range s.plan[name].Dependencies {
			if visited[dep] {
				continue
			}
			visited[dep] = true
			visit(dep)
		}
	}
	visit(root)

	if len(visited) == 0 {
		return nil
	}

	ordered := make([]string, 0, len(visited))
	for _, name := range s.order {
		if visited[name] {
			ordered = append(ordered, name)
		}
	}
	return ordered
}

func (s *executionState) skipDependencies(root, errMsg string) []string {
	closure := s.dependencyClosure(root)
	skipped := make([]string, 0, len(closure))
	for _, name := range closure {
		if s.markSkipped(name, errMsg) {
			skipped = append(skipped, name)
		}
	}
	return skipped
}

func (s *executionState) hasFailures() bool {
	return len(s.failed) > 0
}

func (s *executionState) publicResources() []types.DeploymentResource {
	resources := make([]types.DeploymentResource, 0, len(s.order))
	for _, name := range s.order {
		planResource := s.plan[name]
		dependsOn := append([]string(nil), planResource.Dependencies...)
		resources = append(resources, types.DeploymentResource{
			Name:      name,
			Kind:      planResource.Kind,
			Key:       planResource.Key,
			Status:    s.statuses[name],
			Outputs:   s.outputs[name],
			Error:     s.errors[name],
			DependsOn: dependsOn,
		})
	}
	return resources
}

func (s *executionState) result(key string, status types.DeploymentStatus, errMsg string) DeploymentResult {
	return DeploymentResult{
		Key:       key,
		Status:    status,
		Resources: s.publicResources(),
		Outputs:   s.outputs,
		Error:     errMsg,
	}
}

func (s *executionState) failureSummary() string {
	if len(s.failed) == 0 {
		return ""
	}
	names := make([]string, 0, len(s.failed))
	for name := range s.failed {
		names = append(names, name)
	}
	sort.Strings(names)

	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s: %s", name, s.errors[name]))
	}
	return strings.Join(parts, "; ")
}

func graphFromPlanResources(resources []PlanResource) (*dag.Graph, error) {
	nodes := make([]*types.ResourceNode, 0, len(resources))
	for _, resource := range resources {
		spec := resource.Spec
		if spec == nil {
			spec = json.RawMessage(`{}`)
		}
		nodes = append(nodes, &types.ResourceNode{
			Name:         resource.Name,
			Kind:         resource.Kind,
			Key:          resource.Key,
			Spec:         spec,
			Dependencies: resource.Dependencies,
			Expressions:  resource.Expressions,
		})
	}
	return dag.NewGraph(nodes)
}

func planResourcesFromState(state *DeploymentState) []PlanResource {
	if state == nil || len(state.Resources) == 0 {
		return nil
	}

	names := make([]string, 0, len(state.Resources))
	for name := range state.Resources {
		names = append(names, name)
	}
	sort.Strings(names)

	resources := make([]PlanResource, 0, len(names))
	for _, name := range names {
		resource := state.Resources[name]
		resources = append(resources, PlanResource{
			Name:         resource.Name,
			Kind:         resource.Kind,
			Key:          resource.Key,
			Dependencies: append([]string(nil), resource.DependsOn...),
		})
	}
	return resources
}

func deploymentSummaryFromState(state *DeploymentState) types.DeploymentSummary {
	resources := 0
	if state != nil {
		resources = len(state.Resources)
	}
	return types.DeploymentSummary{
		Key:       state.Key,
		Status:    state.Status,
		Resources: resources,
		CreatedAt: state.CreatedAt,
		UpdatedAt: state.UpdatedAt,
	}
}

func currentTime(ctx restate.Context) (time.Time, error) {
	return restate.Run(ctx, func(runCtx restate.RunContext) (time.Time, error) {
		return time.Now().UTC(), nil
	})
}

func getDeploymentState(ctx restate.Context, deploymentKey string) (*DeploymentState, error) {
	return restate.Object[*DeploymentState](ctx, DeploymentStateServiceName, deploymentKey, "GetState").Request(restate.Void{})
}

func setDeploymentStatus(ctx restate.Context, deploymentKey string, update StatusUpdate) error {
	_, err := restate.WithRequestType[StatusUpdate, restate.Void](
		restate.Object[restate.Void](ctx, DeploymentStateServiceName, deploymentKey, "SetStatus"),
	).Request(update)
	return err
}

func updateDeploymentResource(ctx restate.Context, deploymentKey string, update ResourceUpdate) error {
	_, err := restate.WithRequestType[ResourceUpdate, restate.Void](
		restate.Object[restate.Void](ctx, DeploymentStateServiceName, deploymentKey, "UpdateResource"),
	).Request(update)
	return err
}

func finalizeDeployment(ctx restate.Context, deploymentKey string, final FinalizeRequest) error {
	_, err := restate.WithRequestType[FinalizeRequest, restate.Void](
		restate.Object[restate.Void](ctx, DeploymentStateServiceName, deploymentKey, "Finalize"),
	).Request(final)
	return err
}

func deploymentCancelled(ctx restate.Context, deploymentKey string) (bool, error) {
	return restate.Object[bool](ctx, DeploymentStateServiceName, deploymentKey, "IsCancelled").Request(restate.Void{})
}

func upsertDeploymentSummary(ctx restate.Context, summary types.DeploymentSummary) error {
	_, err := restate.WithRequestType[types.DeploymentSummary, restate.Void](
		restate.Object[restate.Void](ctx, DeploymentIndexServiceName, DeploymentIndexGlobalKey, "Upsert"),
	).Request(summary)
	return err
}

func removeDeploymentSummary(ctx restate.Context, deploymentKey string) error {
	_, err := restate.WithRequestType[string, restate.Void](
		restate.Object[restate.Void](ctx, DeploymentIndexServiceName, DeploymentIndexGlobalKey, "Remove"),
	).Request(deploymentKey)
	return err
}

func appendEvent(ctx restate.Context, deploymentKey string, event DeploymentEvent) error {
	_, err := restate.WithRequestType[DeploymentEvent, restate.Void](
		restate.Object[restate.Void](ctx, DeploymentEventsServiceName, deploymentKey, "Append"),
	).Request(event)
	return err
}
