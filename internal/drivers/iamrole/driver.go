package iamrole

import (
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers"
	"github.com/shirvan/praxis/internal/eventing"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// IAMRoleDriver is the Restate virtual object that manages the lifecycle of a single AWS IAM role.
// It holds an auth client for cross-account credential resolution and a factory for constructing
// the AWS API adapter, enabling dependency injection for testing.
type IAMRoleDriver struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) IAMRoleAPI
}

// NewIAMRoleDriver constructs a driver with the default AWS API factory that creates
// real IAM SDK clients from resolved AWS credentials.
func NewIAMRoleDriver(auth authservice.AuthClient) *IAMRoleDriver {
	return NewIAMRoleDriverWithFactory(auth, func(cfg aws.Config) IAMRoleAPI {
		return NewIAMRoleAPI(awsclient.NewIAMClient(cfg))
	})
}

// NewIAMRoleDriverWithFactory constructs a driver with a custom API factory.
// This is the primary constructor for tests, which inject mock IAMRoleAPI implementations.
func NewIAMRoleDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) IAMRoleAPI) *IAMRoleDriver {
	if factory == nil {
		factory = func(cfg aws.Config) IAMRoleAPI {
			return NewIAMRoleAPI(awsclient.NewIAMClient(cfg))
		}
	}
	return &IAMRoleDriver{auth: auth, apiFactory: factory}
}

// ServiceName returns the Restate virtual object service name for registration.
func (d *IAMRoleDriver) ServiceName() string {
	return ServiceName
}

// Provision implements the idempotent create-or-converge pattern for IAM roles.
// It validates the spec, resolves AWS credentials for the target account, then:
//  1. Checks if the role already exists (by stored outputs or via Describe).
//  2. If not found, creates the role via the CreateRole API.
//  3. Runs correctDrift to converge all mutable fields (trust policy, description,
//     session duration, permissions boundary, inline policies, managed policies, tags).
//  4. Performs a final Describe to capture the authoritative observed state.
//  5. Persists state with StatusReady and schedules periodic reconciliation.
//
// Errors are classified as terminal (400/409 for bad input or conflicts) vs retryable.
func (d *IAMRoleDriver) Provision(ctx restate.ObjectContext, spec IAMRoleSpec) (IAMRoleOutputs, error) {
	ctx.Log().Info("provisioning iam role", "key", restate.Key(ctx), "roleName", spec.RoleName)
	api, err := d.apiForAccount(ctx, spec.Account)
	if err != nil {
		return IAMRoleOutputs{}, restate.TerminalError(err, 400)
	}

	spec = applyDefaults(spec)
	if spec.RoleName == "" {
		return IAMRoleOutputs{}, restate.TerminalError(fmt.Errorf("roleName is required"), 400)
	}
	if spec.AssumeRolePolicyDocument == "" {
		return IAMRoleOutputs{}, restate.TerminalError(fmt.Errorf("assumeRolePolicyDocument is required"), 400)
	}

	state, err := restate.Get[IAMRoleState](ctx, drivers.StateKey)
	if err != nil {
		return IAMRoleOutputs{}, err
	}
	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++

	outputs := state.Outputs
	roleExists := outputs.RoleName != "" || outputs.Arn != ""
	currentObserved := state.Observed
	if roleExists {
		descResult, descErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			obs, runErr := api.DescribeRole(rc, spec.RoleName)
			if runErr != nil {
				if IsNotFound(runErr) {
					return ObservedState{}, restate.TerminalError(runErr, 404)
				}
				return ObservedState{}, runErr
			}
			return obs, nil
		})
		if descErr != nil || descResult.Arn == "" {
			roleExists = false
		} else {
			currentObserved = descResult
			state.Observed = descResult
			outputs = outputsFromObserved(descResult)
		}
	}

	if roleExists && currentObserved.Path != "" && spec.Path != currentObserved.Path {
		return IAMRoleOutputs{}, restate.TerminalError(fmt.Errorf("path is immutable; delete and recreate the role to change the path"), 409)
	}

	if !roleExists {
		created, runErr := restate.Run(ctx, func(rc restate.RunContext) (IAMRoleOutputs, error) {
			arn, roleID, createErr := api.CreateRole(rc, spec)
			if createErr != nil {
				if IsAlreadyExists(createErr) {
					return IAMRoleOutputs{}, restate.TerminalError(createErr, 409)
				}
				if IsMalformedPolicy(createErr) {
					return IAMRoleOutputs{}, restate.TerminalError(createErr, 400)
				}
				return IAMRoleOutputs{}, createErr
			}
			return IAMRoleOutputs{Arn: arn, RoleId: roleID, RoleName: spec.RoleName}, nil
		})
		if runErr != nil {
			state.Status = types.StatusError
			state.Error = runErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return IAMRoleOutputs{}, runErr
		}
		outputs = created

		observedAfterCreate, descErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			return api.DescribeRole(rc, spec.RoleName)
		})
		if descErr != nil {
			state.Status = types.StatusError
			state.Error = descErr.Error()
			state.Outputs = outputs
			restate.Set(ctx, drivers.StateKey, state)
			return IAMRoleOutputs{}, descErr
		}
		currentObserved = observedAfterCreate
	}

	if correctionErr := d.correctDrift(ctx, api, spec.RoleName, spec, currentObserved); correctionErr != nil {
		state.Status = types.StatusError
		state.Error = correctionErr.Error()
		state.Outputs = outputs
		restate.Set(ctx, drivers.StateKey, state)
		return IAMRoleOutputs{}, correctionErr
	}

	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeRole(rc, spec.RoleName)
		if runErr != nil {
			if IsNotFound(runErr) {
				return ObservedState{}, restate.TerminalError(runErr, 404)
			}
			return ObservedState{}, runErr
		}
		return obs, nil
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		state.Outputs = outputs
		restate.Set(ctx, drivers.StateKey, state)
		return IAMRoleOutputs{}, err
	}

	outputs = outputsFromObserved(observed)
	state.Observed = observed
	state.Outputs = outputs
	state.Status = types.StatusReady
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)

	d.scheduleReconcile(ctx, &state)
	return outputs, nil
}

// Import adopts an existing AWS IAM role into Praxis management. It reads the role's current
// state via Describe, constructs a spec from the observed state, and persists it as the desired
// spec. The mode parameter controls whether Praxis will actively correct drift (ModeManaged)
// or only observe and report (ModeObserved). Defaults to ModeObserved.
func (d *IAMRoleDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (IAMRoleOutputs, error) {
	ctx.Log().Info("importing iam role", "resourceId", ref.ResourceID, "mode", ref.Mode)
	api, err := d.apiForAccount(ctx, ref.Account)
	if err != nil {
		return IAMRoleOutputs{}, restate.TerminalError(err, 400)
	}

	mode := ref.Mode
	if mode == "" {
		mode = types.ModeObserved
	}

	state, err := restate.Get[IAMRoleState](ctx, drivers.StateKey)
	if err != nil {
		return IAMRoleOutputs{}, err
	}
	state.Generation++

	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeRole(rc, ref.ResourceID)
		if runErr != nil {
			if IsNotFound(runErr) {
				return ObservedState{}, restate.TerminalError(fmt.Errorf("import failed: role %s does not exist", ref.ResourceID), 404)
			}
			return ObservedState{}, runErr
		}
		return obs, nil
	})
	if err != nil {
		return IAMRoleOutputs{}, err
	}

	spec := specFromObserved(observed)
	spec.Account = ref.Account
	outputs := outputsFromObserved(observed)

	state.Desired = spec
	state.Observed = observed
	state.Outputs = outputs
	state.Status = types.StatusReady
	state.Mode = mode
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)

	d.scheduleReconcile(ctx, &state)
	return outputs, nil
}

// Delete removes an IAM role and all its associated resources. It follows this sequence:
// 1. Guards against deleting observed-mode resources (requires re-import as managed).
// 2. Describes the role to discover current instance profiles, managed policies, and inline policies.
// 3. Detaches the role from all instance profiles.
// 4. Detaches all managed policies and deletes all inline policies.
// 5. Removes the permissions boundary if present.
// 6. Deletes the role itself.
// 7. Persists StatusDeleted state. All steps are idempotent (NotFound errors are swallowed).
func (d *IAMRoleDriver) Delete(ctx restate.ObjectContext) error {
	ctx.Log().Info("deleting iam role", "key", restate.Key(ctx))
	state, err := restate.Get[IAMRoleState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	if state.Status == types.StatusDeleted {
		return nil
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(fmt.Errorf("cannot delete iam role in Observed mode; re-import with --mode managed to allow deletion"), 409)
	}

	name := state.Desired.RoleName
	if name == "" {
		name = state.Outputs.RoleName
	}
	if name == "" {
		restate.Set(ctx, drivers.StateKey, IAMRoleState{Status: types.StatusDeleted})
		return nil
	}

	api, err := d.apiForAccount(ctx, state.Desired.Account)
	if err != nil {
		return restate.TerminalError(err, 400)
	}

	state.Status = types.StatusDeleting
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)

	_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		observed, descErr := api.DescribeRole(rc, name)
		if descErr != nil {
			if IsNotFound(descErr) {
				return restate.Void{}, nil
			}
			return restate.Void{}, descErr
		}

		profiles, listErr := api.ListInstanceProfilesForRole(rc, name)
		if listErr != nil && !IsNotFound(listErr) {
			return restate.Void{}, listErr
		}
		for _, profileName := range profiles {
			if runErr := api.RemoveRoleFromInstanceProfile(rc, name, profileName); runErr != nil && !IsNotFound(runErr) {
				return restate.Void{}, runErr
			}
		}
		for _, policyArn := range observed.ManagedPolicyArns {
			if runErr := api.DetachManagedPolicy(rc, name, policyArn); runErr != nil && !IsNotFound(runErr) {
				return restate.Void{}, runErr
			}
		}
		for policyName := range observed.InlinePolicies {
			if runErr := api.DeleteInlinePolicy(rc, name, policyName); runErr != nil && !IsNotFound(runErr) {
				return restate.Void{}, runErr
			}
		}
		if observed.PermissionsBoundary != "" {
			if runErr := api.DeletePermissionsBoundary(rc, name); runErr != nil && !IsNotFound(runErr) {
				return restate.Void{}, runErr
			}
		}
		if runErr := api.DeleteRole(rc, name); runErr != nil {
			if IsDeleteConflict(runErr) {
				return restate.Void{}, restate.TerminalError(runErr, 409)
			}
			if IsNotFound(runErr) {
				return restate.Void{}, nil
			}
			return restate.Void{}, runErr
		}
		return restate.Void{}, nil
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return err
	}

	restate.Set(ctx, drivers.StateKey, IAMRoleState{Status: types.StatusDeleted})
	return nil
}

// Reconcile is the periodic health-check handler invoked on a timer. It:
// 1. Skips reconciliation for resources not in Ready or Error state.
// 2. Describes the current AWS state of the role.
// 3. Detects if the resource was deleted externally (reports DriftEventExternalDelete).
// 4. Compares desired vs observed state using HasDrift.
// 5. In ModeManaged: auto-corrects drift via correctDrift and reports DriftEventCorrected.
// 6. In ModeObserved: reports drift without correcting.
// 7. Re-schedules the next reconcile using a delayed Restate self-send.
func (d *IAMRoleDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[IAMRoleState](ctx, drivers.StateKey)
	if err != nil {
		return types.ReconcileResult{}, err
	}
	api, err := d.apiForAccount(ctx, state.Desired.Account)
	if err != nil {
		return types.ReconcileResult{}, restate.TerminalError(err, 400)
	}

	state.ReconcileScheduled = false
	if state.Status != types.StatusReady && state.Status != types.StatusError {
		restate.Set(ctx, drivers.StateKey, state)
		return types.ReconcileResult{}, nil
	}

	name := state.Outputs.RoleName
	if name == "" {
		name = state.Desired.RoleName
	}
	if name == "" {
		restate.Set(ctx, drivers.StateKey, state)
		return types.ReconcileResult{}, nil
	}

	now, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
		return time.Now().UTC().Format(time.RFC3339), nil
	})
	if err != nil {
		return types.ReconcileResult{}, err
	}

	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeRole(rc, name)
		if runErr != nil {
			if IsNotFound(runErr) {
				return ObservedState{}, restate.TerminalError(runErr, 404)
			}
			return ObservedState{}, runErr
		}
		return obs, nil
	})
	if err != nil {
		if IsNotFound(err) {
			state.Status = types.StatusError
			state.Error = fmt.Sprintf("role %s was deleted externally", name)
			state.LastReconcile = now
			restate.Set(ctx, drivers.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventExternalDelete, state.Error)
			return types.ReconcileResult{Error: state.Error}, nil
		}
		state.LastReconcile = now
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Error: err.Error()}, nil
	}

	state.Observed = observed
	state.LastReconcile = now
	drift := HasDrift(state.Desired, observed)

	if state.Status == types.StatusError {
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Drift: drift, Correcting: false}, nil
	}

	if drift && state.Mode == types.ModeManaged {
		ctx.Log().Info("drift detected, correcting iam role", "roleName", name)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
		correctionErr := d.correctDrift(ctx, api, name, state.Desired, observed)
		if correctionErr != nil {
			restate.Set(ctx, drivers.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			return types.ReconcileResult{Drift: true, Correcting: true, Error: correctionErr.Error()}, nil
		}
		refreshed, refreshErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			return api.DescribeRole(rc, name)
		})
		if refreshErr == nil {
			state.Observed = refreshed
			state.Outputs = outputsFromObserved(refreshed)
		}
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventCorrected, "")
		return types.ReconcileResult{Drift: true, Correcting: true}, nil
	}

	if drift && state.Mode == types.ModeObserved {
		ctx.Log().Info("drift detected (observed mode, not correcting)", "roleName", name)
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
		return types.ReconcileResult{Drift: true, Correcting: false}, nil
	}

	state.Outputs = outputsFromObserved(observed)
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return types.ReconcileResult{}, nil
}

// GetStatus returns the current lifecycle status, mode, generation, and error for read-only queries.
// Uses ObjectSharedContext for concurrent-safe access without exclusive locking.
func (d *IAMRoleDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[IAMRoleState](ctx, drivers.StateKey)
	if err != nil {
		return types.StatusResponse{}, err
	}
	return types.StatusResponse{Status: state.Status, Mode: state.Mode, Generation: state.Generation, Error: state.Error}, nil
}

// GetOutputs returns the computed outputs (ARN, RoleId, RoleName) for cross-resource references.
func (d *IAMRoleDriver) GetOutputs(ctx restate.ObjectSharedContext) (IAMRoleOutputs, error) {
	state, err := restate.Get[IAMRoleState](ctx, drivers.StateKey)
	if err != nil {
		return IAMRoleOutputs{}, err
	}
	return state.Outputs, nil
}

// GetInputs is a shared (read-only) handler that returns the desired input spec.
func (d *IAMRoleDriver) GetInputs(ctx restate.ObjectSharedContext) (IAMRoleSpec, error) {
	state, err := restate.Get[IAMRoleState](ctx, drivers.StateKey)
	if err != nil {
		return IAMRoleSpec{}, err
	}
	return state.Desired, nil
}

// correctDrift converges all mutable IAM role fields from observed toward desired state.
// Each field update is wrapped in restate.Run for durable journaling. The sequence:
// 1. Path change is rejected as terminal (immutable field).
// 2. Assume role policy document is updated if JSON-normalized content differs.
// 3. Description and max session duration are updated together.
// 4. Permissions boundary is set or removed.
// 5. Inline policies are put (create/update) or deleted to match desired set.
// 6. Managed policies are attached or detached to match desired set.
// 7. Tags are updated via diff-based add/remove.
func (d *IAMRoleDriver) correctDrift(ctx restate.ObjectContext, api IAMRoleAPI, roleName string, desired IAMRoleSpec, observed ObservedState) error {
	if desired.Path != "" && observed.Path != "" && desired.Path != observed.Path {
		return restate.TerminalError(fmt.Errorf("path is immutable; delete and recreate the role to change the path"), 409)
	}

	if !policyDocumentsEqual(desired.AssumeRolePolicyDocument, observed.AssumeRolePolicyDocument) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			if runErr := api.UpdateAssumeRolePolicy(rc, roleName, desired.AssumeRolePolicyDocument); runErr != nil {
				if IsMalformedPolicy(runErr) {
					return restate.Void{}, restate.TerminalError(runErr, 400)
				}
				return restate.Void{}, runErr
			}
			return restate.Void{}, nil
		})
		if err != nil {
			return fmt.Errorf("update assume role policy: %w", err)
		}
	}

	if desired.Description != observed.Description || desired.MaxSessionDuration != observed.MaxSessionDuration {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateRole(rc, roleName, desired.Description, desired.MaxSessionDuration)
		})
		if err != nil {
			return fmt.Errorf("update role settings: %w", err)
		}
	}

	if desired.PermissionsBoundary != observed.PermissionsBoundary {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			if desired.PermissionsBoundary == "" {
				if runErr := api.DeletePermissionsBoundary(rc, roleName); runErr != nil && !IsNotFound(runErr) {
					return restate.Void{}, runErr
				}
				return restate.Void{}, nil
			}
			return restate.Void{}, api.PutPermissionsBoundary(rc, roleName, desired.PermissionsBoundary)
		})
		if err != nil {
			return fmt.Errorf("update permissions boundary: %w", err)
		}
	}

	observedInline := normalizePolicyMap(observed.InlinePolicies)
	for policyName, document := range normalizePolicyMap(desired.InlinePolicies) {
		if current, ok := observedInline[policyName]; ok && current == document {
			continue
		}
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			if runErr := api.PutInlinePolicy(rc, roleName, policyName, document); runErr != nil {
				if IsMalformedPolicy(runErr) {
					return restate.Void{}, restate.TerminalError(runErr, 400)
				}
				return restate.Void{}, runErr
			}
			return restate.Void{}, nil
		})
		if err != nil {
			return fmt.Errorf("put inline policy %s: %w", policyName, err)
		}
	}
	for policyName := range observed.InlinePolicies {
		if _, ok := desired.InlinePolicies[policyName]; ok {
			continue
		}
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			if runErr := api.DeleteInlinePolicy(rc, roleName, policyName); runErr != nil && !IsNotFound(runErr) {
				return restate.Void{}, runErr
			}
			return restate.Void{}, nil
		})
		if err != nil {
			return fmt.Errorf("delete inline policy %s: %w", policyName, err)
		}
	}

	managedToAdd, managedToRemove := diffStringSets(desired.ManagedPolicyArns, observed.ManagedPolicyArns)
	for _, policyArn := range managedToAdd {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.AttachManagedPolicy(rc, roleName, policyArn)
		})
		if err != nil {
			return fmt.Errorf("attach managed policy %s: %w", policyArn, err)
		}
	}
	for _, policyArn := range managedToRemove {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			if runErr := api.DetachManagedPolicy(rc, roleName, policyArn); runErr != nil && !IsNotFound(runErr) {
				return restate.Void{}, runErr
			}
			return restate.Void{}, nil
		})
		if err != nil {
			return fmt.Errorf("detach managed policy %s: %w", policyArn, err)
		}
	}

	if !drivers.TagsMatch(desired.Tags, observed.Tags) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, roleName, desired.Tags)
		})
		if err != nil {
			return fmt.Errorf("update role tags: %w", err)
		}
	}

	return nil
}

// scheduleReconcile sends a delayed self-invocation to the Reconcile handler using Restate's
// durable timer. The ReconcileScheduled flag prevents duplicate timers from being queued.
func (d *IAMRoleDriver) scheduleReconcile(ctx restate.ObjectContext, state *IAMRoleState) {
	if state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").
		Send(restate.Void{}, restate.WithDelay(drivers.ReconcileIntervalForKind(ServiceName)))
}

// apiForAccount resolves AWS credentials for the given account alias via the auth service
// and constructs an IAMRoleAPI instance using the driver's factory.
func (d *IAMRoleDriver) apiForAccount(ctx restate.ObjectContext, account string) (IAMRoleAPI, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, fmt.Errorf("iam role driver is not configured")
	}
	awsCfg, err := d.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve IAM account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), nil
}

// applyDefaults fills in zero-value fields with IAM defaults: path="/", maxSessionDuration=3600,
// and initializes nil maps/slices to empty collections to simplify comparison logic.
func applyDefaults(spec IAMRoleSpec) IAMRoleSpec {
	if spec.Path == "" {
		spec.Path = "/"
	}
	if spec.MaxSessionDuration == 0 {
		spec.MaxSessionDuration = 3600
	}
	if spec.InlinePolicies == nil {
		spec.InlinePolicies = map[string]string{}
	}
	if spec.ManagedPolicyArns == nil {
		spec.ManagedPolicyArns = []string{}
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	return spec
}

// specFromObserved constructs an IAMRoleSpec from the observed AWS state, used during import
// to set the desired spec to match whatever currently exists in AWS.
func specFromObserved(obs ObservedState) IAMRoleSpec {
	inlinePolicies := make(map[string]string, len(obs.InlinePolicies))
	for key, value := range obs.InlinePolicies {
		inlinePolicies[key] = normalizePolicyDocument(value)
	}
	return IAMRoleSpec{
		Path:                     obs.Path,
		RoleName:                 obs.RoleName,
		AssumeRolePolicyDocument: normalizePolicyDocument(obs.AssumeRolePolicyDocument),
		Description:              obs.Description,
		MaxSessionDuration:       obs.MaxSessionDuration,
		PermissionsBoundary:      obs.PermissionsBoundary,
		InlinePolicies:           inlinePolicies,
		ManagedPolicyArns:        sortedStrings(obs.ManagedPolicyArns),
		Tags:                     drivers.FilterPraxisTags(obs.Tags),
	}
}

// outputsFromObserved extracts the output fields (ARN, RoleId, RoleName) from the observed state.
func outputsFromObserved(obs ObservedState) IAMRoleOutputs {
	return IAMRoleOutputs{Arn: obs.Arn, RoleId: obs.RoleId, RoleName: obs.RoleName}
}

// diffStringSets computes the set differences between desired and observed string slices,
// returning elements to add (in desired but not observed) and to remove (in observed but not desired).
func diffStringSets(desired, observed []string) ([]string, []string) {
	desiredSet := make(map[string]struct{}, len(desired))
	observedSet := make(map[string]struct{}, len(observed))
	for _, value := range desired {
		desiredSet[value] = struct{}{}
	}
	for _, value := range observed {
		observedSet[value] = struct{}{}
	}
	var add []string
	for _, value := range desired {
		if _, ok := observedSet[value]; !ok {
			add = append(add, value)
		}
	}
	var remove []string
	for _, value := range observed {
		if _, ok := desiredSet[value]; !ok {
			remove = append(remove, value)
		}
	}
	return sortedStrings(add), sortedStrings(remove)
}

// PreDelete detaches all managed policies, deletes inline policies,
// removes the role from instance profiles, and deletes the permissions
// boundary. This runs before Delete so the role can be cleanly removed.
func (d *IAMRoleDriver) PreDelete(ctx restate.ObjectContext) error {
	state, err := restate.Get[IAMRoleState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	name := state.Desired.RoleName
	if name == "" {
		name = state.Outputs.RoleName
	}
	if name == "" {
		return nil
	}
	api, err := d.apiForAccount(ctx, state.Desired.Account)
	if err != nil {
		return restate.TerminalError(err, 400)
	}
	_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		observed, descErr := api.DescribeRole(rc, name)
		if descErr != nil {
			if IsNotFound(descErr) {
				return restate.Void{}, nil
			}
			return restate.Void{}, descErr
		}
		profiles, listErr := api.ListInstanceProfilesForRole(rc, name)
		if listErr != nil && !IsNotFound(listErr) {
			return restate.Void{}, listErr
		}
		for _, profileName := range profiles {
			if runErr := api.RemoveRoleFromInstanceProfile(rc, name, profileName); runErr != nil && !IsNotFound(runErr) {
				return restate.Void{}, runErr
			}
		}
		for _, policyArn := range observed.ManagedPolicyArns {
			if runErr := api.DetachManagedPolicy(rc, name, policyArn); runErr != nil && !IsNotFound(runErr) {
				return restate.Void{}, runErr
			}
		}
		for policyName := range observed.InlinePolicies {
			if runErr := api.DeleteInlinePolicy(rc, name, policyName); runErr != nil && !IsNotFound(runErr) {
				return restate.Void{}, runErr
			}
		}
		if observed.PermissionsBoundary != "" {
			if runErr := api.DeletePermissionsBoundary(rc, name); runErr != nil && !IsNotFound(runErr) {
				return restate.Void{}, runErr
			}
		}
		return restate.Void{}, nil
	})
	return err
}

// ClearState clears all Virtual Object state for this resource.
// Used by the Orphan deletion policy to release a resource from management.
func (d *IAMRoleDriver) ClearState(ctx restate.ObjectContext) error {
	drivers.ClearAllState(ctx)
	return nil
}
