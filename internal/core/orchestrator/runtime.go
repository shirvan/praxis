// runtime.go provides the shared runtime helpers used by the apply, delete,
// and rollback workflows. It contains:
//
//   - executionState: the in-memory tracker that mirrors resource progress
//     during a DAG dispatch loop (provisioning, ready, failed, skipped).
//   - Graph/plan conversion helpers that bridge PlanResource slices and the
//     DAG scheduler.
//   - Restate RPC wrappers for the DeploymentState Virtual Object, the
//     DeploymentIndex, and the ResourceEventOwner bridge.
//   - CloudEvent emission helpers that route events through the EventBus.
//
// All Restate interactions in this file go through restate.Object or
// restate.WithRequestType to invoke Virtual Object handlers, keeping the
// workflows agnostic of serialization details.
package orchestrator

import (
	"encoding/json"
	"fmt"
	"maps"
	"sort"
	"strings"
	"time"

	cloudevents "github.com/cloudevents/sdk-go/v2"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/dag"
	"github.com/shirvan/praxis/internal/eventing"
	"github.com/shirvan/praxis/pkg/types"
)

// executionState is the in-memory bookkeeper for a single DAG dispatch loop.
// It tracks every resource's current status, outputs, and whether it has been
// dispatched, completed, failed, or skipped. The workflows (apply, delete,
// rollback) mutate this state as futures resolve and use it to build the
// final DeploymentResult.
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

// newExecutionState initialises an executionState from a plan's resource list,
// setting every resource to Pending status with empty outputs.
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
	for i := range resources {
		state.order = append(state.order, resources[i].Name)
		state.plan[resources[i].Name] = resources[i]
		state.statuses[resources[i].Name] = types.DeploymentResourcePending
	}
	return state
}

// loadOutputs merges previously-stored outputs (from a prior generation) into
// the execution state so that expression hydration can reference them.
func (s *executionState) loadOutputs(outputs map[string]map[string]any) {
	maps.Copy(s.outputs, outputs)
}

// ready returns resource names whose dependencies are satisfied and that have
// not yet been dispatched, delegating to the DAG scheduler.
func (s *executionState) ready(schedule *dag.Schedule) []string {
	return schedule.Ready(s.completed, s.dispatched)
}

// markProvisioning transitions a resource to Provisioning and records it as dispatched.
func (s *executionState) markProvisioning(name string) {
	s.statuses[name] = types.DeploymentResourceProvisioning
	s.dispatched[name] = true
}

// resetToPending clears dispatch/failure tracking for a resource so it can be
// re-dispatched. Used by auto-replace when a 409 immutable-field conflict
// triggers a delete+re-provision cycle.
func (s *executionState) resetToPending(name string) {
	s.statuses[name] = types.DeploymentResourcePending
	delete(s.dispatched, name)
	delete(s.completed, name)
	delete(s.failed, name)
	delete(s.skipped, name)
	delete(s.errors, name)
}

// markDeleting transitions a resource to Deleting and records it as dispatched.
func (s *executionState) markDeleting(name string) {
	s.statuses[name] = types.DeploymentResourceDeleting
	s.dispatched[name] = true
}

// markReady transitions a resource to Ready, stores its outputs, clears any
// prior error/failure/skip flags, and marks it completed.
func (s *executionState) markReady(name string, outputs map[string]any) {
	s.statuses[name] = types.DeploymentResourceReady
	s.outputs[name] = outputs
	s.completed[name] = true
	delete(s.errors, name)
	delete(s.failed, name)
	delete(s.skipped, name)
}

// markDeleted transitions a resource to Deleted and marks it completed.
func (s *executionState) markDeleted(name string) {
	s.statuses[name] = types.DeploymentResourceDeleted
	s.completed[name] = true
	delete(s.errors, name)
	delete(s.failed, name)
	delete(s.skipped, name)
}

// markFailed transitions a resource to Error status, records the error message,
// and marks it as both dispatched and failed.
func (s *executionState) markFailed(name, errMsg string) {
	s.statuses[name] = types.DeploymentResourceError
	s.errors[name] = errMsg
	s.failed[name] = true
	s.dispatched[name] = true
}

// markSkipped transitions a resource to Skipped status if it hasn't already
// been dispatched, completed, failed, or skipped. Returns true if the skip
// was applied.
func (s *executionState) markSkipped(name, errMsg string) bool {
	if s.dispatched[name] || s.completed[name] || s.failed[name] || s.skipped[name] {
		return false
	}
	s.statuses[name] = types.DeploymentResourceSkipped
	s.errors[name] = errMsg
	s.skipped[name] = true
	return true
}

// skipAffectedDependents marks all transitive dependents of a failed resource
// as Skipped, using the DAG scheduler's AffectedByFailure traversal. Returns
// the names of resources that were newly skipped.
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

// skipPendingForCancellation marks all resources that have not yet been
// dispatched or completed as Skipped due to a deployment cancellation request.
func (s *executionState) skipPendingForCancellation() []string {
	pending := make([]string, 0)
	for _, name := range s.order {
		if s.markSkipped(name, "skipped because deployment cancellation was requested") {
			pending = append(pending, name)
		}
	}
	return pending
}

// dependencyClosure returns all transitive dependencies of root in plan order.
// This is used by the delete workflow's skipDependencies to cascade a failure
// down to the resources that root depends on.
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

// skipDependencies marks all transitive dependencies of root as Skipped.
// Used by the delete workflow when a resource's deletion fails: its upstream
// dependencies must not be deleted since they may still be in use.
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

// hasFailures reports whether any resource failed during execution.
func (s *executionState) hasFailures() bool {
	return len(s.failed) > 0
}

// publicResources converts the internal execution state into the public
// DeploymentResource slice used in API responses.
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

// result builds the final DeploymentResult from the execution state, including
// any resource errors and aggregated outputs.
func (s *executionState) result(key string, status types.DeploymentStatus, errMsg string) DeploymentResult {
	return DeploymentResult{
		Key:            key,
		Status:         status,
		Resources:      s.publicResources(),
		Outputs:        s.outputs,
		Error:          errMsg,
		ResourceErrors: s.failureMap(),
	}
}

// failureSummary returns a human-readable summary of all failed resources,
// suitable for inclusion in terminal events and error messages.
func (s *executionState) failureSummary() string {
	if len(s.failed) == 0 {
		return ""
	}
	names := make([]string, 0, len(s.failed))
	for name := range s.failed {
		names = append(names, name)
	}
	sort.Strings(names)

	if len(names) == 1 {
		return fmt.Sprintf("%s: %s", names[0], s.errors[names[0]])
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d resource(s) failed:\n", len(names))
	for i, name := range names {
		fmt.Fprintf(&b, "  %d. %s: %s", i+1, name, s.errors[name])
		if i < len(names)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// failureMap returns a map of resource name → error message for all failed
// resources, or nil if there are no failures.
func (s *executionState) failureMap() map[string]string {
	if len(s.failed) == 0 {
		return nil
	}
	result := make(map[string]string, len(s.failed))
	for name := range s.failed {
		result[name] = s.errors[name]
	}
	return result
}

// graphFromPlanResources builds a DAG graph from the plan's resource list,
// used to compute scheduling order and dependency relationships.
func graphFromPlanResources(resources []PlanResource) (*dag.Graph, error) {
	nodes := make([]*types.ResourceNode, 0, len(resources))
	for i := range resources {
		spec := resources[i].Spec
		if spec == nil {
			spec = json.RawMessage(`{}`)
		}
		nodes = append(nodes, &types.ResourceNode{
			Name:         resources[i].Name,
			Kind:         resources[i].Kind,
			Key:          resources[i].Key,
			Spec:         spec,
			Dependencies: resources[i].Dependencies,
			Expressions:  resources[i].Expressions,
		})
	}
	return dag.NewGraph(nodes)
}

// planResourcesFromState reconstructs PlanResource slices from an existing
// DeploymentState, enabling delete and rollback workflows to build a DAG
// from the last known state rather than from a fresh plan.
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
			Lifecycle:    resource.Lifecycle,
		})
	}
	return resources
}

// deploymentSummaryFromState converts a DeploymentState into a lightweight
// DeploymentSummary for index storage.
func deploymentSummaryFromState(state *DeploymentState) types.DeploymentSummary {
	resources := 0
	if state != nil {
		resources = len(state.Resources)
	}
	return types.DeploymentSummary{
		Key:       state.Key,
		Status:    state.Status,
		Resources: resources,
		Workspace: state.Workspace,
		CreatedAt: state.CreatedAt,
		UpdatedAt: state.UpdatedAt,
	}
}

// currentTime returns the current UTC time, wrapped in restate.Run so the
// value is journaled and deterministic on replay.
func currentTime(ctx restate.Context) (time.Time, error) {
	return restate.Run(ctx, func(runCtx restate.RunContext) (time.Time, error) {
		return time.Now().UTC(), nil
	})
}

// getDeploymentState fetches the full DeploymentState from the Virtual Object.
func getDeploymentState(ctx restate.Context, deploymentKey string) (*DeploymentState, error) {
	return restate.Object[*DeploymentState](ctx, DeploymentStateServiceName, deploymentKey, "GetState").Request(restate.Void{})
}

// setDeploymentStatus sends a status update to the DeploymentState Virtual Object.
func setDeploymentStatus(ctx restate.Context, deploymentKey string, update StatusUpdate) error {
	_, err := restate.WithRequestType[StatusUpdate, restate.Void](
		restate.Object[restate.Void](ctx, DeploymentStateServiceName, deploymentKey, "SetStatus"),
	).Request(update)
	return err
}

// updateDeploymentResource sends a per-resource status update to the
// DeploymentState Virtual Object.
func updateDeploymentResource(ctx restate.Context, deploymentKey string, update ResourceUpdate) error {
	_, err := restate.WithRequestType[ResourceUpdate, restate.Void](
		restate.Object[restate.Void](ctx, DeploymentStateServiceName, deploymentKey, "UpdateResource"),
	).Request(update)
	return err
}

// finalizeDeployment sends the terminal finalization request to the
// DeploymentState Virtual Object, setting the final status and resources.
func finalizeDeployment(ctx restate.Context, deploymentKey string, final FinalizeRequest) error {
	_, err := restate.WithRequestType[FinalizeRequest, restate.Void](
		restate.Object[restate.Void](ctx, DeploymentStateServiceName, deploymentKey, "Finalize"),
	).Request(final)
	return err
}

// deploymentCancelled checks whether a cancellation has been requested for the
// given deployment, used by the dispatch loop to break early.
func deploymentCancelled(ctx restate.Context, deploymentKey string) (bool, error) {
	return restate.Object[bool](ctx, DeploymentStateServiceName, deploymentKey, "IsCancelled").Request(restate.Void{})
}

// upsertDeploymentSummary updates the global DeploymentIndex with the latest
// summary for this deployment.
func upsertDeploymentSummary(ctx restate.Context, summary types.DeploymentSummary) error {
	_, err := restate.WithRequestType[types.DeploymentSummary, restate.Void](
		restate.Object[restate.Void](ctx, DeploymentIndexServiceName, DeploymentIndexGlobalKey, "Upsert"),
	).Request(summary)
	return err
}

// removeDeploymentSummary deletes a deployment's entry from the global index,
// called after a successful complete deletion.
func removeDeploymentSummary(ctx restate.Context, deploymentKey string) error {
	_, err := restate.WithRequestType[string, restate.Void](
		restate.Object[restate.Void](ctx, DeploymentIndexServiceName, DeploymentIndexGlobalKey, "Remove"),
	).Request(deploymentKey)
	return err
}

// upsertResourceIndex sends a resource entry upsert to the global ResourceIndex.
func upsertResourceIndex(ctx restate.Context, entry ResourceIndexEntry) error {
	_, err := restate.WithRequestType[ResourceIndexEntry, restate.Void](
		restate.Object[restate.Void](ctx, ResourceIndexServiceName, ResourceIndexGlobalKey, "Upsert"),
	).Request(entry)
	return err
}

// removeResourceIndex removes a single resource entry from the global ResourceIndex.
func removeResourceIndex(ctx restate.Context, deploymentKey, resourceName string) error {
	_, err := restate.WithRequestType[ResourceIndexRemoveRequest, restate.Void](
		restate.Object[restate.Void](ctx, ResourceIndexServiceName, ResourceIndexGlobalKey, "Remove"),
	).Request(ResourceIndexRemoveRequest{
		DeploymentKey: deploymentKey,
		ResourceName:  resourceName,
	})
	return err
}

// removeResourceIndexByDeployment removes all entries for a deployment from the ResourceIndex.
func removeResourceIndexByDeployment(ctx restate.Context, deploymentKey string) error {
	_, err := restate.WithRequestType[string, restate.Void](
		restate.Object[restate.Void](ctx, ResourceIndexServiceName, ResourceIndexGlobalKey, "RemoveByDeployment"),
	).Request(deploymentKey)
	return err
}

// upsertResourceEventOwner registers a resource key → deployment mapping in
// the ResourceEventOwner bridge so drift events can be routed.
func upsertResourceEventOwner(ctx restate.Context, resourceKey string, owner eventing.ResourceEventOwner) error {
	_, err := restate.WithRequestType[eventing.ResourceEventOwner, restate.Void](
		restate.Object[restate.Void](ctx, eventing.ResourceEventOwnerServiceName, resourceKey, "Upsert"),
	).Request(owner)
	return err
}

// deleteResourceEventOwner removes a resource key's ownership mapping,
// called when a resource is deleted from a deployment.
func deleteResourceEventOwner(ctx restate.Context, resourceKey string) error {
	_, err := restate.WithRequestType[restate.Void, restate.Void](
		restate.Object[restate.Void](ctx, eventing.ResourceEventOwnerServiceName, resourceKey, "Delete"),
	).Request(restate.Void{})
	return err
}

// EmitDeploymentCloudEvent validates the deployment extension is present, sets
// e journaled timestamp if missing, and routes the event through the EventBus.
func EmitDeploymentCloudEvent(ctx restate.Context, event cloudevents.Event) error {
	deploymentKey := strings.TrimSpace(eventStringExtension(event, EventExtensionDeployment))
	if deploymentKey == "" {
		return fmt.Errorf("CloudEvent %q is missing %q extension", event.Type(), EventExtensionDeployment)
	}
	if event.Time().IsZero() {
		now, err := currentTime(ctx)
		if err != nil {
			return err
		}
		event.SetTime(now)
	}
	_, err := restate.WithRequestType[cloudevents.Event, restate.Void](
		restate.Object[restate.Void](ctx, EventBusServiceName, EventBusGlobalKey, "Emit"),
	).Request(event)
	return err
}

// EmitCloudEvent routes an arbitrary CloudEvent through the EventBus without
// requiring a deployment extension.
func EmitCloudEvent(ctx restate.Context, event cloudevents.Event) error {
	_, err := restate.WithRequestType[cloudevents.Event, restate.Void](
		restate.Object[restate.Void](ctx, EventBusServiceName, EventBusGlobalKey, "Emit"),
	).Request(event)
	return err
}
