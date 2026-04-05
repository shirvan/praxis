package iaminstanceprofile

import (
	"fmt"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers"
	"github.com/shirvan/praxis/internal/eventing"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// IAMInstanceProfileDriver is the Restate virtual object that manages a single IAM instance profile.
type IAMInstanceProfileDriver struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) IAMInstanceProfileAPI
}

func NewIAMInstanceProfileDriver(auth authservice.AuthClient) *IAMInstanceProfileDriver {
	return NewIAMInstanceProfileDriverWithFactory(auth, func(cfg aws.Config) IAMInstanceProfileAPI {
		return NewIAMInstanceProfileAPI(awsclient.NewIAMClient(cfg))
	})
}

func NewIAMInstanceProfileDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) IAMInstanceProfileAPI) *IAMInstanceProfileDriver {
	if factory == nil {
		factory = func(cfg aws.Config) IAMInstanceProfileAPI {
			return NewIAMInstanceProfileAPI(awsclient.NewIAMClient(cfg))
		}
	}
	return &IAMInstanceProfileDriver{auth: auth, apiFactory: factory}
}

func (d *IAMInstanceProfileDriver) ServiceName() string {
	return ServiceName
}

// Provision implements the idempotent create-or-converge pattern for IAM instance profiles.
// Creates the profile and attaches the role if not found; converges role and tags if it exists.
// Path is immutable—returns a terminal error if an existing profile has a different path.
func (d *IAMInstanceProfileDriver) Provision(ctx restate.ObjectContext, spec IAMInstanceProfileSpec) (IAMInstanceProfileOutputs, error) {
	ctx.Log().Info("provisioning iam instance profile", "key", restate.Key(ctx), "instanceProfileName", spec.InstanceProfileName)
	api, err := d.apiForAccount(ctx, spec.Account)
	if err != nil {
		return IAMInstanceProfileOutputs{}, restate.TerminalError(err, 400)
	}

	spec = applyDefaults(spec)
	if spec.InstanceProfileName == "" {
		return IAMInstanceProfileOutputs{}, restate.TerminalError(fmt.Errorf("instanceProfileName is required"), 400)
	}
	if spec.RoleName == "" {
		return IAMInstanceProfileOutputs{}, restate.TerminalError(fmt.Errorf("roleName is required"), 400)
	}

	state, err := restate.Get[IAMInstanceProfileState](ctx, drivers.StateKey)
	if err != nil {
		return IAMInstanceProfileOutputs{}, err
	}
	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++

	outputs := state.Outputs
	profileExists := outputs.Arn != ""
	if profileExists {
		descResult, descErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			obs, runErr := api.DescribeInstanceProfile(rc, spec.InstanceProfileName)
			if runErr != nil {
				if IsNotFound(runErr) {
					return ObservedState{}, restate.TerminalError(runErr, 404)
				}
				return ObservedState{}, runErr
			}
			return obs, nil
		})
		if descErr != nil || descResult.Arn == "" {
			profileExists = false
		} else {
			state.Observed = descResult
		}
	}

	if profileExists && state.Observed.Path != "" && state.Observed.Path != spec.Path {
		return IAMInstanceProfileOutputs{}, restate.TerminalError(fmt.Errorf("path is immutable; delete and recreate the instance profile to change the path"), 409)
	}

	if !profileExists {
		created, runErr := restate.Run(ctx, func(rc restate.RunContext) (IAMInstanceProfileOutputs, error) {
			arn, profileID, createErr := api.CreateInstanceProfile(rc, spec)
			if createErr != nil {
				if IsAlreadyExists(createErr) {
					return IAMInstanceProfileOutputs{}, restate.TerminalError(createErr, 409)
				}
				return IAMInstanceProfileOutputs{}, createErr
			}
			if addErr := api.AddRoleToInstanceProfile(rc, spec.InstanceProfileName, spec.RoleName); addErr != nil {
				if IsLimitExceeded(addErr) {
					return IAMInstanceProfileOutputs{}, restate.TerminalError(fmt.Errorf("instance profile can only have one role: %w", addErr), 409)
				}
				return IAMInstanceProfileOutputs{}, addErr
			}
			return IAMInstanceProfileOutputs{Arn: arn, InstanceProfileId: profileID, InstanceProfileName: spec.InstanceProfileName}, nil
		})
		if runErr != nil {
			state.Status = types.StatusError
			state.Error = runErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return IAMInstanceProfileOutputs{}, runErr
		}
		outputs = created
	} else {
		if correctionErr := d.correctDrift(ctx, api, spec.InstanceProfileName, spec, state.Observed); correctionErr != nil {
			state.Status = types.StatusError
			state.Error = correctionErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return IAMInstanceProfileOutputs{}, correctionErr
		}
	}

	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeInstanceProfile(rc, spec.InstanceProfileName)
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
		return IAMInstanceProfileOutputs{}, err
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

func (d *IAMInstanceProfileDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (IAMInstanceProfileOutputs, error) {
	ctx.Log().Info("importing iam instance profile", "resourceId", ref.ResourceID, "mode", ref.Mode)
	api, err := d.apiForAccount(ctx, ref.Account)
	if err != nil {
		return IAMInstanceProfileOutputs{}, restate.TerminalError(err, 400)
	}

	mode := ref.Mode
	if mode == "" {
		mode = types.ModeObserved
	}
	state, err := restate.Get[IAMInstanceProfileState](ctx, drivers.StateKey)
	if err != nil {
		return IAMInstanceProfileOutputs{}, err
	}
	state.Generation++

	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeInstanceProfile(rc, ref.ResourceID)
		if runErr != nil {
			if IsNotFound(runErr) {
				return ObservedState{}, restate.TerminalError(fmt.Errorf("import failed: instance profile %s does not exist", ref.ResourceID), 404)
			}
			return ObservedState{}, runErr
		}
		return obs, nil
	})
	if err != nil {
		return IAMInstanceProfileOutputs{}, err
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

// Delete removes the instance profile from AWS after detaching its role.
// Handles the case where the role is still attached by re-describing and retrying.
func (d *IAMInstanceProfileDriver) Delete(ctx restate.ObjectContext) error {
	ctx.Log().Info("deleting iam instance profile", "key", restate.Key(ctx))
	state, err := restate.Get[IAMInstanceProfileState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	if state.Status == types.StatusDeleted {
		return nil
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(fmt.Errorf("cannot delete iam instance profile in Observed mode; re-import with --mode managed to allow deletion"), 409)
	}

	name := state.Desired.InstanceProfileName
	if name == "" {
		name = state.Outputs.InstanceProfileName
	}
	if name == "" {
		restate.Set(ctx, drivers.StateKey, IAMInstanceProfileState{Status: types.StatusDeleted})
		return nil
	}

	api, err := d.apiForAccount(ctx, state.Desired.Account)
	if err != nil {
		return restate.TerminalError(err, 400)
	}

	state.Status = types.StatusDeleting
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)

	removeRoleName := state.Observed.RoleName
	if removeRoleName == "" {
		removeRoleName = state.Desired.RoleName
	}

	_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		if removeRoleName != "" {
			if runErr := api.RemoveRoleFromInstanceProfile(rc, name, removeRoleName); runErr != nil && !IsNotFound(runErr) {
				return restate.Void{}, runErr
			}
		}
		if runErr := api.DeleteInstanceProfile(rc, name); runErr != nil {
			if IsDeleteConflict(runErr) {
				obs, descErr := api.DescribeInstanceProfile(rc, name)
				if descErr == nil && obs.RoleName != "" {
					if retryErr := api.RemoveRoleFromInstanceProfile(rc, name, obs.RoleName); retryErr != nil && !IsNotFound(retryErr) {
						return restate.Void{}, retryErr
					}
					if deleteErr := api.DeleteInstanceProfile(rc, name); deleteErr != nil && !IsNotFound(deleteErr) {
						return restate.Void{}, deleteErr
					}
					return restate.Void{}, nil
				}
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

	restate.Set(ctx, drivers.StateKey, IAMInstanceProfileState{Status: types.StatusDeleted})
	return nil
}

func (d *IAMInstanceProfileDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[IAMInstanceProfileState](ctx, drivers.StateKey)
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

	name := state.Outputs.InstanceProfileName
	if name == "" {
		name = state.Desired.InstanceProfileName
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
		obs, runErr := api.DescribeInstanceProfile(rc, name)
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
			state.Error = fmt.Sprintf("instance profile %s was deleted externally", name)
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
		ctx.Log().Info("drift detected, correcting iam instance profile", "instanceProfileName", name)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
		correctionErr := d.correctDrift(ctx, api, name, state.Desired, observed)
		if correctionErr != nil {
			restate.Set(ctx, drivers.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			return types.ReconcileResult{Drift: true, Correcting: true, Error: correctionErr.Error()}, nil
		}
		refreshed, refreshErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			return api.DescribeInstanceProfile(rc, name)
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
		ctx.Log().Info("drift detected (observed mode, not correcting)", "instanceProfileName", name)
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

func (d *IAMInstanceProfileDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[IAMInstanceProfileState](ctx, drivers.StateKey)
	if err != nil {
		return types.StatusResponse{}, err
	}
	return types.StatusResponse{Status: state.Status, Mode: state.Mode, Generation: state.Generation, Error: state.Error}, nil
}

func (d *IAMInstanceProfileDriver) GetOutputs(ctx restate.ObjectSharedContext) (IAMInstanceProfileOutputs, error) {
	state, err := restate.Get[IAMInstanceProfileState](ctx, drivers.StateKey)
	if err != nil {
		return IAMInstanceProfileOutputs{}, err
	}
	return state.Outputs, nil
}

// GetInputs is a shared (read-only) handler that returns the desired input spec.
func (d *IAMInstanceProfileDriver) GetInputs(ctx restate.ObjectSharedContext) (IAMInstanceProfileSpec, error) {
	state, err := restate.Get[IAMInstanceProfileState](ctx, drivers.StateKey)
	if err != nil {
		return IAMInstanceProfileSpec{}, err
	}
	return state.Desired, nil
}

// correctDrift converges role association and tags from observed toward desired state.
// Returns a terminal error if path differs (immutable field).
func (d *IAMInstanceProfileDriver) correctDrift(ctx restate.ObjectContext, api IAMInstanceProfileAPI, name string, desired IAMInstanceProfileSpec, observed ObservedState) error {
	if desired.Path != "" && observed.Path != "" && desired.Path != observed.Path {
		return restate.TerminalError(fmt.Errorf("path is immutable; delete and recreate the instance profile to change the path"), 409)
	}
	if desired.RoleName != observed.RoleName {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			if observed.RoleName != "" {
				if runErr := api.RemoveRoleFromInstanceProfile(rc, name, observed.RoleName); runErr != nil && !IsNotFound(runErr) {
					return restate.Void{}, runErr
				}
			}
			if desired.RoleName != "" {
				if runErr := api.AddRoleToInstanceProfile(rc, name, desired.RoleName); runErr != nil {
					if IsLimitExceeded(runErr) {
						return restate.Void{}, restate.TerminalError(fmt.Errorf("instance profile can only have one role: %w", runErr), 409)
					}
					return restate.Void{}, runErr
				}
			}
			return restate.Void{}, nil
		})
		if err != nil {
			return fmt.Errorf("update role association: %w", err)
		}
	}

	addTags, removeKeys := diffTags(desired.Tags, observed.Tags)
	if len(addTags) > 0 {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.TagInstanceProfile(rc, name, addTags)
		})
		if err != nil {
			return fmt.Errorf("tag instance profile: %w", err)
		}
	}
	if len(removeKeys) > 0 {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UntagInstanceProfile(rc, name, removeKeys)
		})
		if err != nil {
			return fmt.Errorf("untag instance profile: %w", err)
		}
	}
	return nil
}

func (d *IAMInstanceProfileDriver) scheduleReconcile(ctx restate.ObjectContext, state *IAMInstanceProfileState) {
	if state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").
		Send(restate.Void{}, restate.WithDelay(drivers.ReconcileInterval))
}

func (d *IAMInstanceProfileDriver) apiForAccount(ctx restate.ObjectContext, account string) (IAMInstanceProfileAPI, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, fmt.Errorf("iam instance profile driver is not configured")
	}
	awsCfg, err := d.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve IAM account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), nil
}

func applyDefaults(spec IAMInstanceProfileSpec) IAMInstanceProfileSpec {
	if spec.Path == "" {
		spec.Path = "/"
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	return spec
}

func specFromObserved(obs ObservedState) IAMInstanceProfileSpec {
	return IAMInstanceProfileSpec{
		Path:                obs.Path,
		InstanceProfileName: obs.InstanceProfileName,
		RoleName:            obs.RoleName,
		Tags:                drivers.FilterPraxisTags(obs.Tags),
	}
}

func outputsFromObserved(obs ObservedState) IAMInstanceProfileOutputs {
	return IAMInstanceProfileOutputs{
		Arn:                 obs.Arn,
		InstanceProfileId:   obs.InstanceProfileId,
		InstanceProfileName: obs.InstanceProfileName,
	}
}

// diffTags computes the set of tags to add/update and keys to remove by comparing
// desired vs observed tags, after filtering out "praxis:"-prefixed internal tags.
func diffTags(desired, observed map[string]string) (map[string]string, []string) {
	filteredDesired := drivers.FilterPraxisTags(desired)
	filteredObserved := drivers.FilterPraxisTags(observed)

	add := make(map[string]string)
	for key, value := range filteredDesired {
		if observedValue, ok := filteredObserved[key]; !ok || observedValue != value {
			add[key] = value
		}
	}
	var remove []string
	for key := range filteredObserved {
		if _, ok := filteredDesired[key]; !ok {
			remove = append(remove, key)
		}
	}
	sort.Strings(remove)
	return add, remove
}
