package route53healthcheck

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

// HealthCheckDriver is the Restate virtual object that manages a single Route53 health check.
type HealthCheckDriver struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) HealthCheckAPI
}

func NewHealthCheckDriver(auth authservice.AuthClient) *HealthCheckDriver {
	return NewHealthCheckDriverWithFactory(auth, func(cfg aws.Config) HealthCheckAPI {
		return NewHealthCheckAPI(awsclient.NewRoute53Client(cfg))
	})
}

func NewHealthCheckDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) HealthCheckAPI) *HealthCheckDriver {
	if factory == nil {
		factory = func(cfg aws.Config) HealthCheckAPI { return NewHealthCheckAPI(awsclient.NewRoute53Client(cfg)) }
	}
	return &HealthCheckDriver{auth: auth, apiFactory: factory}
}

func (d *HealthCheckDriver) ServiceName() string {
	return ServiceName
}

// Provision implements the idempotent create-or-converge pattern for Route53 health checks.
// Creates the check if not found, then converges all mutable fields (type and requestInterval
// are immutable). Uses version-based optimistic concurrency for updates.
func (d *HealthCheckDriver) Provision(ctx restate.ObjectContext, spec HealthCheckSpec) (HealthCheckOutputs, error) {
	ctx.Log().Info("provisioning route53 health check", "key", restate.Key(ctx))
	api, err := d.apiForAccount(ctx, spec.Account)
	if err != nil {
		return HealthCheckOutputs{}, restate.TerminalError(err, 400)
	}
	spec.ManagedKey = restate.Key(ctx)
	spec, err = normalizeHealthCheckSpec(spec)
	if err != nil {
		return HealthCheckOutputs{}, restate.TerminalError(err, 400)
	}

	state, err := restate.Get[HealthCheckState](ctx, drivers.StateKey)
	if err != nil {
		return HealthCheckOutputs{}, err
	}
	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++

	healthCheckID := state.Outputs.HealthCheckId
	observed := state.Observed
	if healthCheckID != "" {
		described, descErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			return api.DescribeHealthCheck(rc, healthCheckID)
		})
		if descErr == nil {
			observed = described
		} else {
			healthCheckID = ""
		}
	}

	if healthCheckID == "" {
		createdID, createErr := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
			id, runErr := api.CreateHealthCheck(rc, spec)
			if runErr != nil {
				if IsAlreadyExists(runErr) || IsConflict(runErr) {
					return "", restate.TerminalError(runErr, 409)
				}
				if IsInvalidInput(runErr) {
					return "", restate.TerminalError(runErr, 400)
				}
				return "", runErr
			}
			return id, nil
		})
		if createErr != nil {
			state.Status = types.StatusError
			state.Error = createErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return HealthCheckOutputs{}, createErr
		}
		healthCheckID = createdID
	}

	if observed.HealthCheckId != "" && observed.Type != spec.Type {
		err := fmt.Errorf("health check %s already exists with type %s; requested type %s cannot be changed", healthCheckID, observed.Type, spec.Type)
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return HealthCheckOutputs{}, restate.TerminalError(err, 409)
	}
	if observed.HealthCheckId != "" && observed.RequestInterval != 0 && observed.RequestInterval != spec.RequestInterval {
		err := fmt.Errorf("health check %s already exists with requestInterval %d; requested requestInterval %d cannot be changed", healthCheckID, observed.RequestInterval, spec.RequestInterval)
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return HealthCheckOutputs{}, restate.TerminalError(err, 409)
	}

	if correctionErr := d.correctDrift(ctx, api, healthCheckID, spec, observed); correctionErr != nil {
		state.Status = types.StatusError
		state.Error = correctionErr.Error()
		state.Outputs = HealthCheckOutputs{HealthCheckId: healthCheckID}
		restate.Set(ctx, drivers.StateKey, state)
		return HealthCheckOutputs{}, correctionErr
	}

	observed, err = restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		return api.DescribeHealthCheck(rc, healthCheckID)
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		state.Outputs = HealthCheckOutputs{HealthCheckId: healthCheckID}
		restate.Set(ctx, drivers.StateKey, state)
		return HealthCheckOutputs{}, err
	}
	outputs := outputsFromObserved(observed)
	state.Observed = observed
	state.Outputs = outputs
	state.Status = types.StatusReady
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return outputs, nil
}

func (d *HealthCheckDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (HealthCheckOutputs, error) {
	ctx.Log().Info("importing route53 health check", "resourceId", ref.ResourceID, "mode", ref.Mode)
	api, err := d.apiForAccount(ctx, ref.Account)
	if err != nil {
		return HealthCheckOutputs{}, restate.TerminalError(err, 400)
	}
	mode := defaultHealthCheckImportMode(ref.Mode)
	state, err := restate.Get[HealthCheckState](ctx, drivers.StateKey)
	if err != nil {
		return HealthCheckOutputs{}, err
	}
	state.Generation++
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribeHealthCheck(rc, ref.ResourceID)
		if runErr != nil {
			if IsNotFound(runErr) {
				return ObservedState{}, restate.TerminalError(fmt.Errorf("import failed: health check %s does not exist", ref.ResourceID), 404)
			}
			return ObservedState{}, runErr
		}
		return obs, nil
	})
	if err != nil {
		return HealthCheckOutputs{}, err
	}
	spec := specFromObserved(observed)
	spec.Account = ref.Account
	spec.ManagedKey = restate.Key(ctx)
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

// Delete removes the health check from AWS. Refuses deletion in Observed mode.
func (d *HealthCheckDriver) Delete(ctx restate.ObjectContext) error {
	ctx.Log().Info("deleting route53 health check", "key", restate.Key(ctx))
	state, err := restate.Get[HealthCheckState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	if state.Status == types.StatusDeleted {
		return nil
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(fmt.Errorf("cannot delete health check %s: resource is in Observed mode; re-import with --mode managed to allow deletion", state.Outputs.HealthCheckId), 409)
	}
	healthCheckID := state.Outputs.HealthCheckId
	if healthCheckID == "" {
		restate.Set(ctx, drivers.StateKey, HealthCheckState{Status: types.StatusDeleted})
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
		runErr := api.DeleteHealthCheck(rc, healthCheckID)
		if runErr != nil {
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
	restate.Set(ctx, drivers.StateKey, HealthCheckState{Status: types.StatusDeleted})
	return nil
}

func (d *HealthCheckDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[HealthCheckState](ctx, drivers.StateKey)
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
	healthCheckID := state.Outputs.HealthCheckId
	if healthCheckID == "" {
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
		obs, runErr := api.DescribeHealthCheck(rc, healthCheckID)
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
			state.Error = fmt.Sprintf("health check %s was deleted externally", healthCheckID)
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
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
		if correctionErr := d.correctDrift(ctx, api, healthCheckID, state.Desired, observed); correctionErr != nil {
			state.Status = types.StatusError
			state.Error = correctionErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			return types.ReconcileResult{Drift: true, Correcting: true, Error: correctionErr.Error()}, nil
		}
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventCorrected, "")
		return types.ReconcileResult{Drift: true, Correcting: true}, nil
	}
	if drift && state.Mode == types.ModeObserved {
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
	}
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return types.ReconcileResult{Drift: drift, Correcting: false}, nil
}

func (d *HealthCheckDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[HealthCheckState](ctx, drivers.StateKey)
	if err != nil {
		return types.StatusResponse{}, err
	}
	return types.StatusResponse{Status: state.Status, Mode: state.Mode, Generation: state.Generation, Error: state.Error}, nil
}

func (d *HealthCheckDriver) GetOutputs(ctx restate.ObjectSharedContext) (HealthCheckOutputs, error) {
	state, err := restate.Get[HealthCheckState](ctx, drivers.StateKey)
	if err != nil {
		return HealthCheckOutputs{}, err
	}
	return state.Outputs, nil
}

// GetInputs is a shared (read-only) handler that returns the desired input spec.
func (d *HealthCheckDriver) GetInputs(ctx restate.ObjectSharedContext) (HealthCheckSpec, error) {
	state, err := restate.Get[HealthCheckState](ctx, drivers.StateKey)
	if err != nil {
		return HealthCheckSpec{}, err
	}
	return state.Desired, nil
}

// correctDrift converges health check configuration and tags from observed toward desired state.
// Uses version-based optimistic concurrency via UpdateHealthCheck, then updates tags separately.
func (d *HealthCheckDriver) correctDrift(ctx restate.ObjectContext, api HealthCheckAPI, healthCheckID string, desired HealthCheckSpec, observed ObservedState) error {
	if observed.HealthCheckId != "" {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateHealthCheck(rc, healthCheckID, observed, desired)
		})
		if err != nil {
			return fmt.Errorf("update health check configuration: %w", err)
		}
	}
	if !tagsMatch(desired.Tags, observed.Tags) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, healthCheckID, desired.Tags)
		})
		if err != nil {
			return fmt.Errorf("update health check tags: %w", err)
		}
	}
	return nil
}

func (d *HealthCheckDriver) scheduleReconcile(ctx restate.ObjectContext, state *HealthCheckState) {
	if state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").Send(restate.Void{}, restate.WithDelay(drivers.ReconcileInterval))
}

func (d *HealthCheckDriver) apiForAccount(ctx restate.ObjectContext, account string) (HealthCheckAPI, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, fmt.Errorf("HealthCheckDriver is not configured with an auth registry")
	}
	awsCfg, err := d.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve Route53 health check account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), nil
}

func specFromObserved(observed ObservedState) HealthCheckSpec {
	return HealthCheckSpec{
		Type:                         observed.Type,
		IPAddress:                    observed.IPAddress,
		Port:                         observed.Port,
		ResourcePath:                 observed.ResourcePath,
		FQDN:                         observed.FQDN,
		SearchString:                 observed.SearchString,
		RequestInterval:              observed.RequestInterval,
		FailureThreshold:             observed.FailureThreshold,
		ChildHealthChecks:            observed.ChildHealthChecks,
		HealthThreshold:              observed.HealthThreshold,
		CloudWatchAlarmName:          observed.CloudWatchAlarmName,
		CloudWatchAlarmRegion:        observed.CloudWatchAlarmRegion,
		InsufficientDataHealthStatus: observed.InsufficientDataHealthStatus,
		Disabled:                     observed.Disabled,
		InvertHealthCheck:            observed.InvertHealthCheck,
		EnableSNI:                    observed.EnableSNI,
		Regions:                      observed.Regions,
		Tags:                         filterPraxisTags(observed.Tags),
	}
}

func outputsFromObserved(observed ObservedState) HealthCheckOutputs {
	return HealthCheckOutputs{HealthCheckId: observed.HealthCheckId}
}

func defaultHealthCheckImportMode(mode types.Mode) types.Mode {
	if mode == "" {
		return types.ModeObserved
	}
	return mode
}
