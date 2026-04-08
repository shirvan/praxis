// deployment_state.go implements the DeploymentState Restate Virtual Object.
//
// Virtual Objects in Restate are durable, key-addressed entities with
// exclusive-writer semantics. Each deployment gets its own DeploymentStateObj
// instance keyed by the deployment key. The object stores the full lifecycle
// record (status, resource states, outputs, cancel flag) and survives across
// workflow generations.
//
// Handlers are partitioned by access pattern:
//   - Mutating handlers (ObjectContext): InitDeployment, SetStatus, UpdateResource,
//     Finalize, RequestCancel, MoveResource, RemoveResource, AddResource.
//   - Read-only handlers (ObjectSharedContext): GetState, GetDetail, IsCancelled.
//     These can run concurrently without blocking writers.
package orchestrator

import (
	"fmt"
	"sort"

	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/eventing"
	"github.com/shirvan/praxis/pkg/types"
)

// DeploymentStateObj is the durable, per-deployment source of truth.
//
// The object survives across workflow runs. That is the key architectural split
// between lifecycle state and workflow execution: workflows are run-once per key,
// while deployments need a persistent record that supports re-apply, delete, and
// direct reads by the CLI or future automation.
type DeploymentStateObj struct{}

// ServiceName overrides the default reflected service name so the object can be
// addressed through the stable contract surface.
func (DeploymentStateObj) ServiceName() string {
	return DeploymentStateServiceName
}

// InitDeployment creates or re-initializes the durable deployment record.
//
// Re-apply semantics:
//
//   - The first apply starts at generation 1.
//   - A later apply against the same deployment key increments the generation.
//   - Resource statuses are reset to Pending, previous outputs are cleared, and
//     the cancel flag is dropped.
func (DeploymentStateObj) InitDeployment(ctx restate.ObjectContext, plan DeploymentPlan) (int64, error) {
	existing, err := restate.Get[*DeploymentState](ctx, "state")
	if err != nil {
		return 0, err
	}

	// generation tracks how many times this deployment key has been applied.
	// Generation 1 = first apply; each re-apply increments it. This enables
	// events to be correlated with the specific apply run that produced them.
	generation := int64(1)
	if existing != nil {
		generation = existing.Generation + 1
	}

	resources := make(map[string]*ResourceState, len(plan.Resources))
	for i := range plan.Resources {
		resource := &plan.Resources[i]
		dependsOn := append([]string(nil), resource.Dependencies...)
		sort.Strings(dependsOn)

		// Mark resources that existed in the prior generation as PriorReady
		// so the workflow can distinguish "Provisioning" (create) from
		// "Updating" (re-apply of an existing resource).
		priorReady := false
		if existing != nil {
			if prev, ok := existing.Resources[resource.Name]; ok {
				priorReady = prev.Status == types.DeploymentResourceReady || prev.Status == types.DeploymentResourceError
			}
		}

		resources[resource.Name] = &ResourceState{
			Name:          resource.Name,
			Kind:          resource.Kind,
			DriverService: resource.DriverService,
			Key:           resource.Key,
			DependsOn:     dependsOn,
			Status:        types.DeploymentResourcePending,
			Lifecycle:     resource.Lifecycle,
			PriorReady:    priorReady,
		}
	}

	state := &DeploymentState{
		Key:          restate.Key(ctx),
		Account:      plan.Account,
		Workspace:    plan.Workspace,
		Status:       types.DeploymentPending,
		TemplatePath: plan.TemplatePath,
		Resources:    resources,
		Outputs:      make(map[string]map[string]any, len(plan.Resources)),
		Generation:   generation,
		CreatedAt:    plan.CreatedAt,
		UpdatedAt:    plan.CreatedAt,
	}
	restate.Set(ctx, "state", state)

	// Register resource-event-owner mappings so that drift events reported
	// by drivers (keyed by resource key) can be routed back to the correct
	// deployment stream. This is the bridge between the driver world (resource
	// keys) and the orchestrator world (deployment keys + resource names).
	activeKeys := make(map[string]bool, len(plan.Resources))
	for i := range plan.Resources {
		resource := plan.Resources[i]
		activeKeys[resource.Key] = true
		if err := upsertResourceEventOwner(ctx, resource.Key, eventing.ResourceEventOwner{
			StreamKey:    plan.Key,
			Workspace:    plan.Workspace,
			Generation:   generation,
			ResourceName: resource.Name,
			ResourceKind: resource.Kind,
		}); err != nil {
			return 0, err
		}
	}
	// Clean up stale resource-event-owner mappings from the previous generation.
	// If a resource was removed from the template between re-applies, its owner
	// mapping must be deleted to prevent phantom drift events.
	if existing != nil {
		for _, resource := range existing.Resources {
			if resource == nil || activeKeys[resource.Key] {
				continue
			}
			if err := deleteResourceEventOwner(ctx, resource.Key); err != nil {
				return 0, err
			}
		}
	}
	return generation, nil
}

// SetStatus moves the deployment as a whole into a non-terminal lifecycle stage.
//
// Apply workflows use this to record Running, and delete workflows use it to
// record Deleting. Final terminal states go through Finalize.
func (DeploymentStateObj) SetStatus(ctx restate.ObjectContext, update StatusUpdate) error {
	state, err := restate.Get[*DeploymentState](ctx, "state")
	if err != nil {
		return err
	}
	if state == nil {
		return restate.TerminalError(fmt.Errorf("deployment %q not found", restate.Key(ctx)), 404)
	}

	state.Status = update.Status
	state.Error = update.Error
	state.UpdatedAt = update.UpdatedAt
	restate.Set(ctx, "state", state)
	return nil
}

// UpdateResource applies one resource-level status update.
func (DeploymentStateObj) UpdateResource(ctx restate.ObjectContext, update ResourceUpdate) error {
	state, err := restate.Get[*DeploymentState](ctx, "state")
	if err != nil {
		return err
	}
	if state == nil {
		return restate.TerminalError(fmt.Errorf("deployment %q not found", restate.Key(ctx)), 404)
	}

	resource, ok := state.Resources[update.Name]
	if !ok {
		return restate.TerminalError(fmt.Errorf("resource %q not found in deployment %q", update.Name, restate.Key(ctx)), 404)
	}

	now, err := currentTime(ctx)
	if err != nil {
		return err
	}

	resource.Status = update.Status
	resource.Error = update.Error
	if update.Conditions != nil {
		resource.Conditions = append([]types.Condition(nil), update.Conditions...)
	}
	if update.Outputs != nil {
		state.Outputs[update.Name] = update.Outputs
	}
	state.UpdatedAt = now
	restate.Set(ctx, "state", state)
	return nil
}

// Finalize records a terminal deployment status such as Complete, Failed,
// Deleted, or Cancelled.
func (DeploymentStateObj) Finalize(ctx restate.ObjectContext, final FinalizeRequest) error {
	state, err := restate.Get[*DeploymentState](ctx, "state")
	if err != nil {
		return err
	}
	if state == nil {
		return restate.TerminalError(fmt.Errorf("deployment %q not found", restate.Key(ctx)), 404)
	}

	state.Status = final.Status
	state.Error = final.Error
	state.UpdatedAt = final.UpdatedAt
	restate.Set(ctx, "state", state)
	if final.Status == types.DeploymentDeleted {
		for _, resource := range state.Resources {
			if resource == nil || resource.Key == "" {
				continue
			}
			if err := deleteResourceEventOwner(ctx, resource.Key); err != nil {
				return err
			}
		}
	}
	return nil
}

// RequestCancel sets the durable cancel flag. The apply workflow polls this
// flag between dispatch cycles and stops scheduling new work when it becomes
// true.
func (DeploymentStateObj) RequestCancel(ctx restate.ObjectContext, _ restate.Void) error {
	state, err := restate.Get[*DeploymentState](ctx, "state")
	if err != nil {
		return err
	}
	if state == nil {
		return restate.TerminalError(fmt.Errorf("deployment %q not found", restate.Key(ctx)), 404)
	}

	now, err := currentTime(ctx)
	if err != nil {
		return err
	}

	state.Cancelled = true
	state.UpdatedAt = now
	restate.Set(ctx, "state", state)
	return nil
}

// GetState returns the full durable deployment record.
func (DeploymentStateObj) GetState(ctx restate.ObjectSharedContext, _ restate.Void) (*DeploymentState, error) {
	return restate.Get[*DeploymentState](ctx, "state")
}

// GetDetail projects the durable state into the public deployment detail shape.
func (DeploymentStateObj) GetDetail(ctx restate.ObjectSharedContext, _ restate.Void) (*types.DeploymentDetail, error) {
	state, err := restate.Get[*DeploymentState](ctx, "state")
	if err != nil {
		return nil, err
	}
	if state == nil {
		return nil, nil
	}

	resources := stateResourcesToPublic(state)

	var errorCode types.ErrorCode
	if state.Status == types.DeploymentFailed && state.Error != "" {
		// Determine whether the failure occurred during a delete or provision flow.
		// If any resource reached Deleting or Deleted status, the deployment was in
		// a delete workflow when it failed.
		isDeleteFailure := false
		for _, rs := range state.Resources {
			if rs.Status == types.DeploymentResourceDeleting || rs.Status == types.DeploymentResourceDeleted {
				isDeleteFailure = true
				break
			}
		}
		if isDeleteFailure {
			errorCode = types.ErrCodeDeleteFailed
		} else {
			errorCode = types.ErrCodeProvisionFailed
		}
	}

	var resourceErrors map[string]string
	for _, rs := range state.Resources {
		if rs.Error != "" {
			if resourceErrors == nil {
				resourceErrors = make(map[string]string)
			}
			resourceErrors[rs.Name] = rs.Error
		}
	}

	return &types.DeploymentDetail{
		Key:            state.Key,
		Status:         state.Status,
		Workspace:      state.Workspace,
		TemplatePath:   state.TemplatePath,
		Resources:      resources,
		Error:          state.Error,
		ErrorCode:      errorCode,
		ResourceErrors: resourceErrors,
		CreatedAt:      state.CreatedAt,
		UpdatedAt:      state.UpdatedAt,
	}, nil
}

// IsCancelled reads the durable cancel flag.
func (DeploymentStateObj) IsCancelled(ctx restate.ObjectSharedContext, _ restate.Void) (bool, error) {
	state, err := restate.Get[*DeploymentState](ctx, "state")
	if err != nil {
		return false, err
	}
	if state == nil {
		return false, nil
	}
	return state.Cancelled, nil
}

// ReconcileAll fans out Reconcile calls to every eligible resource in the
// deployment.
func (DeploymentStateObj) ReconcileAll(ctx restate.ObjectContext, req ReconcileAllRequest) (ReconcileAllResponse, error) {
	state, err := restate.Get[*DeploymentState](ctx, "state")
	if err != nil {
		return ReconcileAllResponse{}, err
	}
	if state == nil {
		return ReconcileAllResponse{}, restate.TerminalError(fmt.Errorf("deployment %q not found", restate.Key(ctx)), 404)
	}

	var (
		triggered int
		skipped   []string
	)
	for name, resource := range state.Resources {
		if resource == nil {
			continue
		}
		if !req.Force {
			switch resource.Status {
			case types.DeploymentResourcePending, types.DeploymentResourceSkipped, types.DeploymentResourceDeleted, types.DeploymentResourceOrphaned:
				skipped = append(skipped, name)
				continue
			}
		}
		serviceName := resource.DriverService
		if serviceName == "" {
			serviceName = resource.Kind
		}
		restate.ObjectSend(ctx, serviceName, resource.Key, "Reconcile").Send(restate.Void{})
		triggered++
	}
	sort.Strings(skipped)
	return ReconcileAllResponse{Triggered: triggered, Skipped: skipped}, nil
}

// MoveResource renames a resource within this deployment. The deployment must
// be in a terminal state (Complete, Failed, or Cancelled).
func (DeploymentStateObj) MoveResource(ctx restate.ObjectContext, req MoveResourceRequest) error {
	state, err := restate.Get[*DeploymentState](ctx, "state")
	if err != nil {
		return err
	}
	if state == nil {
		return restate.TerminalError(fmt.Errorf("deployment %q not found", restate.Key(ctx)), 404)
	}
	if !isTerminal(state.Status) {
		return restate.TerminalError(fmt.Errorf("deployment %q is %s; state mv requires a terminal state (Complete, Failed, Cancelled, Deleted)", restate.Key(ctx), state.Status), 409)
	}

	rs, ok := state.Resources[req.ResourceName]
	if !ok {
		return restate.TerminalError(fmt.Errorf("resource %q not found in deployment %q", req.ResourceName, restate.Key(ctx)), 404)
	}

	newName := req.NewName
	if newName == "" {
		newName = req.ResourceName
	}

	if newName != req.ResourceName {
		if _, exists := state.Resources[newName]; exists {
			return restate.TerminalError(fmt.Errorf("resource %q already exists in deployment %q", newName, restate.Key(ctx)), 409)
		}
	}

	now, err := currentTime(ctx)
	if err != nil {
		return err
	}

	// Remove old entry.
	delete(state.Resources, req.ResourceName)
	outputs := state.Outputs[req.ResourceName]
	delete(state.Outputs, req.ResourceName)

	// Insert under new name.
	rs.Name = newName
	// Update DependsOn references in all remaining resources.
	for _, other := range state.Resources {
		for i, dep := range other.DependsOn {
			if dep == req.ResourceName {
				other.DependsOn[i] = newName
			}
		}
		sort.Strings(other.DependsOn)
	}
	state.Resources[newName] = rs
	if outputs != nil {
		state.Outputs[newName] = outputs
	}
	state.UpdatedAt = now
	restate.Set(ctx, "state", state)
	if err := upsertResourceEventOwner(ctx, rs.Key, eventing.ResourceEventOwner{
		StreamKey:    state.Key,
		Workspace:    state.Workspace,
		Generation:   state.Generation,
		ResourceName: rs.Name,
		ResourceKind: rs.Kind,
	}); err != nil {
		return err
	}
	// Update the resource index: remove the old name, upsert the new one.
	if err := removeResourceIndex(ctx, state.Key, req.ResourceName); err != nil {
		return err
	}
	if err := upsertResourceIndex(ctx, ResourceIndexEntry{
		Kind:          rs.Kind,
		Key:           rs.Key,
		DeploymentKey: state.Key,
		ResourceName:  newName,
		Workspace:     state.Workspace,
		Status:        string(rs.Status),
		CreatedAt:     state.CreatedAt,
	}); err != nil {
		return err
	}
	return nil
}

// RemoveResource detaches a resource from this deployment and returns it.
// Used for the source side of a cross-deployment move. The deployment must be
// in a terminal state.
func (DeploymentStateObj) RemoveResource(ctx restate.ObjectContext, name string) (*ResourceState, error) {
	state, err := restate.Get[*DeploymentState](ctx, "state")
	if err != nil {
		return nil, err
	}
	if state == nil {
		return nil, restate.TerminalError(fmt.Errorf("deployment %q not found", restate.Key(ctx)), 404)
	}
	if !isTerminal(state.Status) {
		return nil, restate.TerminalError(fmt.Errorf("deployment %q is %s; state mv requires a terminal state (Complete, Failed, Cancelled, Deleted)", restate.Key(ctx), state.Status), 409)
	}

	rs, ok := state.Resources[name]
	if !ok {
		return nil, restate.TerminalError(fmt.Errorf("resource %q not found in deployment %q", name, restate.Key(ctx)), 404)
	}

	now, err := currentTime(ctx)
	if err != nil {
		return nil, err
	}

	delete(state.Resources, name)
	delete(state.Outputs, name)

	// Clean up DependsOn references pointing to the removed resource.
	for _, other := range state.Resources {
		filtered := other.DependsOn[:0]
		for _, dep := range other.DependsOn {
			if dep != name {
				filtered = append(filtered, dep)
			}
		}
		other.DependsOn = filtered
	}

	state.UpdatedAt = now
	restate.Set(ctx, "state", state)
	if err := deleteResourceEventOwner(ctx, rs.Key); err != nil {
		return nil, err
	}
	if err := removeResourceIndex(ctx, state.Key, name); err != nil {
		return nil, err
	}
	return rs, nil
}

// AddResource inserts a resource into this deployment. Used for the destination
// side of a cross-deployment move. The deployment must be in a terminal state.
func (DeploymentStateObj) AddResource(ctx restate.ObjectContext, rs ResourceState) error {
	state, err := restate.Get[*DeploymentState](ctx, "state")
	if err != nil {
		return err
	}
	if state == nil {
		return restate.TerminalError(fmt.Errorf("deployment %q not found", restate.Key(ctx)), 404)
	}
	if !isTerminal(state.Status) {
		return restate.TerminalError(fmt.Errorf("deployment %q is %s; state mv requires a terminal state (Complete, Failed, Cancelled, Deleted)", restate.Key(ctx), state.Status), 409)
	}
	if _, exists := state.Resources[rs.Name]; exists {
		return restate.TerminalError(fmt.Errorf("resource %q already exists in deployment %q", rs.Name, restate.Key(ctx)), 409)
	}

	now, err := currentTime(ctx)
	if err != nil {
		return err
	}

	state.Resources[rs.Name] = &rs
	state.UpdatedAt = now
	restate.Set(ctx, "state", state)
	if err := upsertResourceEventOwner(ctx, rs.Key, eventing.ResourceEventOwner{
		StreamKey:    state.Key,
		Workspace:    state.Workspace,
		Generation:   state.Generation,
		ResourceName: rs.Name,
		ResourceKind: rs.Kind,
	}); err != nil {
		return err
	}
	if err := upsertResourceIndex(ctx, ResourceIndexEntry{
		Kind:          rs.Kind,
		Key:           rs.Key,
		DeploymentKey: state.Key,
		ResourceName:  rs.Name,
		Workspace:     state.Workspace,
		Status:        string(rs.Status),
		CreatedAt:     state.CreatedAt,
	}); err != nil {
		return err
	}
	return nil
}

// isTerminal returns true for deployment statuses that allow state mutations.
func isTerminal(status types.DeploymentStatus) bool {
	switch status {
	case types.DeploymentComplete, types.DeploymentFailed, types.DeploymentCancelled, types.DeploymentDeleted:
		return true
	default:
		return false
	}
}

func stateResourcesToPublic(state *DeploymentState) []types.DeploymentResource {
	if state == nil || len(state.Resources) == 0 {
		return nil
	}

	names := make([]string, 0, len(state.Resources))
	for name := range state.Resources {
		names = append(names, name)
	}
	sort.Strings(names)

	resources := make([]types.DeploymentResource, 0, len(names))
	for _, name := range names {
		resource := state.Resources[name]
		resources = append(resources, types.DeploymentResource{
			Name:       resource.Name,
			Kind:       resource.Kind,
			Key:        resource.Key,
			Status:     resource.Status,
			Outputs:    state.Outputs[name],
			Error:      resource.Error,
			DependsOn:  append([]string(nil), resource.DependsOn...),
			Conditions: append([]types.Condition(nil), resource.Conditions...),
		})
	}
	return resources
}
