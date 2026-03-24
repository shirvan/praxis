package orchestrator

import (
	"fmt"
	"sort"

	restate "github.com/restatedev/sdk-go"

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

	generation := int64(1)
	if existing != nil {
		generation = existing.Generation + 1
	}

	resources := make(map[string]*ResourceState, len(plan.Resources))
	for _, resource := range plan.Resources {
		dependsOn := append([]string(nil), resource.Dependencies...)
		sort.Strings(dependsOn)
		resources[resource.Name] = &ResourceState{
			Name:      resource.Name,
			Kind:      resource.Kind,
			Key:       resource.Key,
			DependsOn: dependsOn,
			Status:    types.DeploymentResourcePending,
			Lifecycle: resource.Lifecycle,
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
		errorCode = types.ErrCodeProvisionFailed
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
		return restate.TerminalError(fmt.Errorf("deployment %q is %s; state mv requires a terminal state (Complete, Failed, Cancelled)", restate.Key(ctx), state.Status), 409)
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
		return nil, restate.TerminalError(fmt.Errorf("deployment %q is %s; state mv requires a terminal state (Complete, Failed, Cancelled)", restate.Key(ctx), state.Status), 409)
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
		return restate.TerminalError(fmt.Errorf("deployment %q is %s; state mv requires a terminal state (Complete, Failed, Cancelled)", restate.Key(ctx), state.Status), 409)
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
	return nil
}

// isTerminal returns true for deployment statuses that allow state mutations.
func isTerminal(status types.DeploymentStatus) bool {
	switch status {
	case types.DeploymentComplete, types.DeploymentFailed, types.DeploymentCancelled:
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
			Name:      resource.Name,
			Kind:      resource.Kind,
			Key:       resource.Key,
			Status:    resource.Status,
			Outputs:   state.Outputs[name],
			Error:     resource.Error,
			DependsOn: append([]string(nil), resource.DependsOn...),
		})
	}
	return resources
}
