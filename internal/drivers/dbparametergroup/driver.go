package dbparametergroup

import (
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type DBParameterGroupDriver struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) DBParameterGroupAPI
}

func NewDBParameterGroupDriver(auth authservice.AuthClient) *DBParameterGroupDriver {
	return NewDBParameterGroupDriverWithFactory(auth, func(cfg aws.Config) DBParameterGroupAPI {
		return NewDBParameterGroupAPI(awsclient.NewRDSClient(cfg))
	})
}

func NewDBParameterGroupDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) DBParameterGroupAPI) *DBParameterGroupDriver {
	if factory == nil {
		factory = func(cfg aws.Config) DBParameterGroupAPI { return NewDBParameterGroupAPI(awsclient.NewRDSClient(cfg)) }
	}
	return &DBParameterGroupDriver{auth: auth, apiFactory: factory}
}

func (d *DBParameterGroupDriver) ServiceName() string {
	return ServiceName
}

func (d *DBParameterGroupDriver) Provision(ctx restate.ObjectContext, spec DBParameterGroupSpec) (DBParameterGroupOutputs, error) {
	ctx.Log().Info("provisioning db parameter group", "key", restate.Key(ctx))
	api, _, err := d.apiForAccount(ctx, spec.Account)
	if err != nil {
		return DBParameterGroupOutputs{}, restate.TerminalError(err, 400)
	}
	spec = applyDefaults(spec)
	if err := validateSpec(spec); err != nil {
		return DBParameterGroupOutputs{}, restate.TerminalError(err, 400)
	}
	state, err := restate.Get[DBParameterGroupState](ctx, drivers.StateKey)
	if err != nil {
		return DBParameterGroupOutputs{}, err
	}
	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++

	observed := state.Observed
	if state.Outputs.GroupName != "" || spec.GroupName != "" {
		described, descErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			obs, runErr := api.DescribeParameterGroup(rc, spec.GroupName, spec.Type)
			if runErr != nil {
				if IsNotFound(runErr) {
					return ObservedState{}, restate.TerminalError(runErr, 404)
				}
				return ObservedState{}, runErr
			}
			return obs, nil
		})
		if descErr == nil {
			observed = described
		}
	}

	if observed.GroupName == "" {
		createdARN, createErr := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
			arn, runErr := api.CreateParameterGroup(rc, spec)
			if runErr != nil {
				if IsAlreadyExists(runErr) {
					return "", restate.TerminalError(runErr, 409)
				}
				if IsInvalidParam(runErr) {
					return "", restate.TerminalError(runErr, 400)
				}
				return "", runErr
			}
			return arn, nil
		})
		if createErr != nil {
			state.Status = types.StatusError
			state.Error = createErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return DBParameterGroupOutputs{}, createErr
		}
		_ = createdARN
	}

	if correctionErr := d.correctDrift(ctx, api, spec, observed); correctionErr != nil {
		state.Status = types.StatusError
		state.Error = correctionErr.Error()
		state.Outputs = outputsFromObserved(observed)
		restate.Set(ctx, drivers.StateKey, state)
		return DBParameterGroupOutputs{}, correctionErr
	}

	observed, err = restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeParameterGroup(rc, spec.GroupName, spec.Type)
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
		restate.Set(ctx, drivers.StateKey, state)
		return DBParameterGroupOutputs{}, err
	}
	state.Observed = observed
	state.Outputs = outputsFromObserved(observed)
	state.Status = types.StatusReady
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return state.Outputs, nil
}

func (d *DBParameterGroupDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (DBParameterGroupOutputs, error) {
	ctx.Log().Info("importing db parameter group", "resourceId", ref.ResourceID, "mode", ref.Mode)
	api, region, err := d.apiForAccount(ctx, ref.Account)
	if err != nil {
		return DBParameterGroupOutputs{}, restate.TerminalError(err, 400)
	}
	mode := defaultImportMode(ref.Mode)
	groupType := parameterGroupTypeFromKey(restate.Key(ctx))
	state, err := restate.Get[DBParameterGroupState](ctx, drivers.StateKey)
	if err != nil {
		return DBParameterGroupOutputs{}, err
	}
	state.Generation++
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeParameterGroup(rc, ref.ResourceID, groupType)
		if runErr != nil {
			if IsNotFound(runErr) {
				return ObservedState{}, restate.TerminalError(fmt.Errorf("import failed: db parameter group %s does not exist", ref.ResourceID), 404)
			}
			return ObservedState{}, runErr
		}
		return obs, nil
	})
	if err != nil {
		return DBParameterGroupOutputs{}, err
	}
	spec := specFromObserved(observed)
	spec.Account = ref.Account
	spec.Region = region
	state.Desired = spec
	state.Observed = observed
	state.Outputs = outputsFromObserved(observed)
	state.Status = types.StatusReady
	state.Mode = mode
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return state.Outputs, nil
}

func (d *DBParameterGroupDriver) Delete(ctx restate.ObjectContext) error {
	ctx.Log().Info("deleting db parameter group", "key", restate.Key(ctx))
	state, err := restate.Get[DBParameterGroupState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	if state.Status == types.StatusDeleted {
		return nil
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(fmt.Errorf("cannot delete db parameter group %s: resource is in Observed mode; re-import with --mode managed to allow deletion", state.Outputs.GroupName), 409)
	}
	groupName := state.Outputs.GroupName
	if groupName == "" {
		groupName = state.Desired.GroupName
	}
	if groupName == "" {
		restate.Set(ctx, drivers.StateKey, DBParameterGroupState{Status: types.StatusDeleted})
		return nil
	}
	groupType := state.Desired.Type
	api, _, err := d.apiForAccount(ctx, state.Desired.Account)
	if err != nil {
		return restate.TerminalError(err, 400)
	}
	state.Status = types.StatusDeleting
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)
	_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		runErr := api.DeleteParameterGroup(rc, groupName, groupType)
		if runErr != nil {
			if IsNotFound(runErr) {
				return restate.Void{}, nil
			}
			if IsInvalidState(runErr) {
				return restate.Void{}, restate.TerminalError(runErr, 409)
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
	restate.Set(ctx, drivers.StateKey, DBParameterGroupState{Status: types.StatusDeleted})
	return nil
}

func (d *DBParameterGroupDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[DBParameterGroupState](ctx, drivers.StateKey)
	if err != nil {
		return types.ReconcileResult{}, err
	}
	api, _, err := d.apiForAccount(ctx, state.Desired.Account)
	if err != nil {
		return types.ReconcileResult{}, restate.TerminalError(err, 400)
	}
	state.ReconcileScheduled = false
	if state.Status != types.StatusReady && state.Status != types.StatusError {
		restate.Set(ctx, drivers.StateKey, state)
		return types.ReconcileResult{}, nil
	}
	groupName := state.Outputs.GroupName
	if groupName == "" {
		groupName = state.Desired.GroupName
	}
	if groupName == "" {
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
		obs, runErr := api.DescribeParameterGroup(rc, groupName, state.Desired.Type)
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
			state.Error = fmt.Sprintf("db parameter group %s was deleted externally", groupName)
			state.LastReconcile = now
			restate.Set(ctx, drivers.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			return types.ReconcileResult{Error: state.Error}, nil
		}
		state.LastReconcile = now
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Error: err.Error()}, nil
	}
	state.Observed = observed
	state.Outputs = outputsFromObserved(observed)
	state.LastReconcile = now
	drift := HasDrift(state.Desired, observed)
	if state.Status == types.StatusError {
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Drift: drift, Correcting: false}, nil
	}
	if drift && state.Mode == types.ModeManaged {
		if correctionErr := d.correctDrift(ctx, api, state.Desired, observed); correctionErr != nil {
			state.Status = types.StatusError
			state.Error = correctionErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			return types.ReconcileResult{Drift: true, Correcting: true, Error: correctionErr.Error()}, nil
		}
		state.Error = ""
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Drift: true, Correcting: true}, nil
	}
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return types.ReconcileResult{Drift: drift, Correcting: false}, nil
}

func (d *DBParameterGroupDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[DBParameterGroupState](ctx, drivers.StateKey)
	if err != nil {
		return types.StatusResponse{}, err
	}
	return types.StatusResponse{Status: state.Status, Mode: state.Mode, Generation: state.Generation, Error: state.Error}, nil
}

func (d *DBParameterGroupDriver) GetOutputs(ctx restate.ObjectSharedContext) (DBParameterGroupOutputs, error) {
	state, err := restate.Get[DBParameterGroupState](ctx, drivers.StateKey)
	if err != nil {
		return DBParameterGroupOutputs{}, err
	}
	return state.Outputs, nil
}

func (d *DBParameterGroupDriver) correctDrift(ctx restate.ObjectContext, api DBParameterGroupAPI, desired DBParameterGroupSpec, observed ObservedState) error {
	if HasDrift(desired, observed) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			runErr := api.UpdateParameters(rc, desired, observed)
			if runErr != nil {
				if IsInvalidParam(runErr) {
					return restate.Void{}, restate.TerminalError(runErr, 400)
				}
				return restate.Void{}, runErr
			}
			return restate.Void{}, nil
		})
		if err != nil {
			return fmt.Errorf("update db parameter group parameters: %w", err)
		}
	}
	if !tagsMatch(desired.Tags, observed.Tags) && observed.ARN != "" {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, observed.ARN, desired.Tags)
		})
		if err != nil {
			return fmt.Errorf("update db parameter group tags: %w", err)
		}
	}
	return nil
}

func (d *DBParameterGroupDriver) scheduleReconcile(ctx restate.ObjectContext, state *DBParameterGroupState) {
	if state == nil || state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").Send(restate.Void{}, restate.WithDelay(drivers.ReconcileInterval))
}

func (d *DBParameterGroupDriver) apiForAccount(ctx restate.ObjectContext, account string) (DBParameterGroupAPI, string, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, "", fmt.Errorf("db parameter group driver is not configured")
	}
	awsCfg, err := d.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve RDS account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), awsCfg.Region, nil
}

func validateSpec(spec DBParameterGroupSpec) error {
	if strings.TrimSpace(spec.Region) == "" {
		return fmt.Errorf("region is required")
	}
	if strings.TrimSpace(spec.GroupName) == "" {
		return fmt.Errorf("groupName is required")
	}
	if strings.TrimSpace(spec.Type) != TypeDB && strings.TrimSpace(spec.Type) != TypeCluster {
		return fmt.Errorf("type must be %q or %q", TypeDB, TypeCluster)
	}
	if strings.TrimSpace(spec.Family) == "" {
		return fmt.Errorf("family is required")
	}
	if strings.TrimSpace(spec.Description) == "" {
		spec.Description = spec.GroupName
	}
	return nil
}

func specFromObserved(observed ObservedState) DBParameterGroupSpec {
	return DBParameterGroupSpec{GroupName: observed.GroupName, Type: observed.Type, Family: observed.Family, Description: observed.Description, Parameters: observed.Parameters, Tags: filterPraxisTags(observed.Tags)}
}

func outputsFromObserved(observed ObservedState) DBParameterGroupOutputs {
	return DBParameterGroupOutputs{GroupName: observed.GroupName, ARN: observed.ARN, Family: observed.Family, Type: observed.Type}
}

func defaultImportMode(mode types.Mode) types.Mode {
	if mode == "" {
		return types.ModeObserved
	}
	return mode
}

func parameterGroupTypeFromKey(key string) string {
	if strings.Contains(strings.ToLower(key), "cluster") {
		return TypeCluster
	}
	return TypeDB
}
