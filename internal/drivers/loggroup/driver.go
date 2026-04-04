// Package loggroup – driver.go
//
// This file implements the Restate Virtual Object handler for AWS CloudWatch Log Group.
// The driver exposes five durable handlers:
//   - Provision: create-or-update the resource and persist state
//   - Import:    adopt an existing AWS resource into Praxis management
//   - Delete:    remove the resource from AWS (managed mode only)
//   - Reconcile: periodic drift check + auto-correction (managed mode)
//   - GetStatus / GetOutputs: read-only shared handlers for status queries
//
// All mutating AWS calls are wrapped in restate.Run for durable execution,
// and reconciliation is self-scheduled via delayed Restate messages.
package loggroup

import (
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers"
	"github.com/shirvan/praxis/internal/eventing"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// LogGroupDriver is the Restate Virtual Object handler for AWS CloudWatch Log Group.
// It holds an auth client (for cross-account credential resolution)
// and an API factory (swappable for testing).
type LogGroupDriver struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) LogGroupAPI
}

// NewLogGroupDriver creates a LogGroup driver wired to the given
// auth client. It uses the default AWS SDK client factory.
func NewLogGroupDriver(auth authservice.AuthClient) *LogGroupDriver {
	return NewLogGroupDriverWithFactory(auth, func(cfg aws.Config) LogGroupAPI {
		return NewLogGroupAPI(awsclient.NewCloudWatchLogsClient(cfg))
	})
}

// NewLogGroupDriverWithFactory creates a LogGroup driver with a custom API
// factory, primarily used in tests to inject mock AWS clients.
func NewLogGroupDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) LogGroupAPI) *LogGroupDriver {
	if factory == nil {
		factory = func(cfg aws.Config) LogGroupAPI {
			return NewLogGroupAPI(awsclient.NewCloudWatchLogsClient(cfg))
		}
	}
	return &LogGroupDriver{auth: auth, apiFactory: factory}
}

// ServiceName returns the Restate Virtual Object service name for registration.
func (d *LogGroupDriver) ServiceName() string {
	return ServiceName
}

// Provision creates or updates a AWS CloudWatch Log Group. It validates the spec,
// checks for an existing resource (by ARN or name), detects immutable-field
// conflicts, and either creates a new resource or corrects drift on the
// existing one. State is persisted in Restate K/V after every step.
func (d *LogGroupDriver) Provision(ctx restate.ObjectContext, spec LogGroupSpec) (LogGroupOutputs, error) {
	ctx.Log().Info("provisioning CloudWatch log group", "key", restate.Key(ctx))
	api, region, err := d.apiForAccount(ctx, spec.Account)
	if err != nil {
		return LogGroupOutputs{}, restate.TerminalError(err, 400)
	}
	spec = applyDefaults(spec)
	spec.Region = region
	spec.ManagedKey = restate.Key(ctx)
	if err := validateSpec(spec); err != nil {
		return LogGroupOutputs{}, restate.TerminalError(err, 400)
	}

	state, err := restate.Get[LogGroupState](ctx, drivers.StateKey)
	if err != nil {
		return LogGroupOutputs{}, err
	}
	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++

	observed, found, err := d.observeLogGroup(ctx, api, spec.LogGroupName)
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return LogGroupOutputs{}, err
	}

	if !found {
		_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			runErr := api.CreateLogGroup(rc, spec)
			if runErr != nil {
				if IsAlreadyExists(runErr) {
					return restate.Void{}, restate.TerminalError(runErr, 409)
				}
				if IsInvalidParam(runErr) {
					return restate.Void{}, restate.TerminalError(runErr, 400)
				}
				if IsConflict(runErr) || IsLimitExceeded(runErr) {
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
			return LogGroupOutputs{}, err
		}
	} else if spec.LogGroupClass != observed.LogGroupClass {
		err := fmt.Errorf("logGroupClass is immutable for %s: current=%s desired=%s", spec.LogGroupName, observed.LogGroupClass, spec.LogGroupClass)
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return LogGroupOutputs{}, restate.TerminalError(err, 409)
	}

	if err := d.convergeMutableFields(ctx, api, spec, observed, found); err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return LogGroupOutputs{}, err
	}

	observed, found, err = d.observeLogGroup(ctx, api, spec.LogGroupName)
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return LogGroupOutputs{}, err
	}
	if !found {
		err := fmt.Errorf("log group %s disappeared during provisioning", spec.LogGroupName)
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return LogGroupOutputs{}, err
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

// Import adopts an existing AWS CloudWatch Log Group into Praxis management.
// It reads the current configuration from AWS, synthesizes a spec from
// the observed state, and stores it. Default import mode is Observed
// (read-only); users can re-import with --mode managed to enable writes.
func (d *LogGroupDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (LogGroupOutputs, error) {
	ctx.Log().Info("importing CloudWatch log group", "resourceId", ref.ResourceID, "mode", ref.Mode)
	api, region, err := d.apiForAccount(ctx, ref.Account)
	if err != nil {
		return LogGroupOutputs{}, restate.TerminalError(err, 400)
	}
	state, err := restate.Get[LogGroupState](ctx, drivers.StateKey)
	if err != nil {
		return LogGroupOutputs{}, err
	}
	state.Generation++
	observed, found, err := d.observeLogGroup(ctx, api, ref.ResourceID)
	if err != nil {
		return LogGroupOutputs{}, err
	}
	if !found {
		return LogGroupOutputs{}, restate.TerminalError(fmt.Errorf("import failed: log group %s does not exist", ref.ResourceID), 404)
	}
	spec := specFromObserved(observed)
	spec.Account = ref.Account
	spec.Region = region
	spec.ManagedKey = restate.Key(ctx)
	outputs := outputsFromObserved(observed)
	state.Desired = spec
	state.Observed = observed
	state.Outputs = outputs
	state.Status = types.StatusReady
	state.Mode = defaultImportMode(ref.Mode)
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return outputs, nil
}

// Delete removes the AWS CloudWatch Log Group from AWS. It is blocked for
// resources in Observed mode. The method handles not-found gracefully
// (idempotent delete) and sets the final state to StatusDeleted.
func (d *LogGroupDriver) Delete(ctx restate.ObjectContext) error {
	ctx.Log().Info("deleting CloudWatch log group", "key", restate.Key(ctx))
	state, err := restate.Get[LogGroupState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	if state.Status == types.StatusDeleted {
		return nil
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(fmt.Errorf("cannot delete log group %s: resource is in Observed mode; re-import with --mode managed to allow deletion", state.Outputs.LogGroupName), 409)
	}
	if state.Outputs.LogGroupName == "" {
		restate.Set(ctx, drivers.StateKey, LogGroupState{Status: types.StatusDeleted})
		return nil
	}
	api, _, err := d.apiForAccount(ctx, state.Desired.Account)
	if err != nil {
		return restate.TerminalError(err, 400)
	}
	state.Status = types.StatusDeleting
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)
	_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		runErr := api.DeleteLogGroup(rc, state.Outputs.LogGroupName)
		if runErr != nil {
			if IsNotFound(runErr) {
				return restate.Void{}, nil
			}
			if IsInvalidParam(runErr) {
				return restate.Void{}, restate.TerminalError(runErr, 400)
			}
			if IsConflict(runErr) {
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
	restate.Set(ctx, drivers.StateKey, LogGroupState{Status: types.StatusDeleted})
	return nil
}

// Reconcile is the periodic drift-check handler. It re-reads the
// resource from AWS, compares against desired state, and auto-corrects
// drift when in Managed mode. In Observed mode it only reports drift.
// External deletions are detected and flagged as errors.
// The handler self-schedules via a delayed Restate message.
func (d *LogGroupDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[LogGroupState](ctx, drivers.StateKey)
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
	if state.Outputs.LogGroupName == "" {
		restate.Set(ctx, drivers.StateKey, state)
		return types.ReconcileResult{}, nil
	}
	now, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
		return time.Now().UTC().Format(time.RFC3339), nil
	})
	if err != nil {
		return types.ReconcileResult{}, err
	}
	observed, found, err := d.observeLogGroup(ctx, api, state.Outputs.LogGroupName)
	if err != nil {
		state.LastReconcile = now
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Error: err.Error()}, nil
	}
	if !found {
		state.Status = types.StatusError
		state.Error = fmt.Sprintf("log group %s was deleted externally", state.Outputs.LogGroupName)
		state.LastReconcile = now
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventExternalDelete, state.Error)
		return types.ReconcileResult{Error: state.Error}, nil
	}
	state.Observed = observed
	state.Outputs = outputsFromObserved(observed)
	state.LastReconcile = now
	drift := HasDrift(state.Desired, observed)
	if state.Status == types.StatusError {
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Drift: drift}, nil
	}
	if drift && state.Mode == types.ModeManaged {
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
		if correctionErr := d.correctDrift(ctx, api, state.Desired, observed); correctionErr != nil {
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
	return types.ReconcileResult{Drift: drift}, nil
}

// GetStatus is a shared (read-only) handler that returns the current lifecycle status.
func (d *LogGroupDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[LogGroupState](ctx, drivers.StateKey)
	if err != nil {
		return types.StatusResponse{}, err
	}
	return types.StatusResponse{Status: state.Status, Mode: state.Mode, Generation: state.Generation, Error: state.Error}, nil
}

// GetOutputs is a shared (read-only) handler that returns the provisioned resource outputs.
func (d *LogGroupDriver) GetOutputs(ctx restate.ObjectSharedContext) (LogGroupOutputs, error) {
	state, err := restate.Get[LogGroupState](ctx, drivers.StateKey)
	if err != nil {
		return LogGroupOutputs{}, err
	}
	return state.Outputs, nil
}

// GetInputs is a shared (read-only) handler that returns the desired input spec.
func (d *LogGroupDriver) GetInputs(ctx restate.ObjectSharedContext) (LogGroupSpec, error) {
	state, err := restate.Get[LogGroupState](ctx, drivers.StateKey)
	if err != nil {
		return LogGroupSpec{}, err
	}
	return state.Desired, nil
}

func (d *LogGroupDriver) correctDrift(ctx restate.ObjectContext, api LogGroupAPI, desired LogGroupSpec, observed ObservedState) error {
	if desired.LogGroupClass != "" && observed.LogGroupClass != "" && desired.LogGroupClass != observed.LogGroupClass {
		return fmt.Errorf("logGroupClass is immutable for %s: current=%s desired=%s", desired.LogGroupName, observed.LogGroupClass, desired.LogGroupClass)
	}
	return d.convergeMutableFields(ctx, api, desired, observed, true)
}

func (d *LogGroupDriver) convergeMutableFields(ctx restate.ObjectContext, api LogGroupAPI, spec LogGroupSpec, observed ObservedState, found bool) error {
	if found && !retentionMatch(spec.RetentionInDays, observed.RetentionInDays) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			if spec.RetentionInDays == nil {
				return restate.Void{}, api.DeleteRetentionPolicy(rc, spec.LogGroupName)
			}
			return restate.Void{}, api.PutRetentionPolicy(rc, spec.LogGroupName, *spec.RetentionInDays)
		})
		if err != nil {
			if IsInvalidParam(err) {
				return restate.TerminalError(err, 400)
			}
			return err
		}
	}
	if found && strings.TrimSpace(spec.KmsKeyID) != strings.TrimSpace(observed.KmsKeyID) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			if strings.TrimSpace(spec.KmsKeyID) == "" {
				return restate.Void{}, api.DisassociateKmsKey(rc, spec.LogGroupName)
			}
			return restate.Void{}, api.AssociateKmsKey(rc, spec.LogGroupName, spec.KmsKeyID)
		})
		if err != nil {
			if IsInvalidParam(err) {
				return restate.TerminalError(err, 400)
			}
			return err
		}
	}
	if found {
		toAdd, toRemove := tagDiff(spec.Tags, observed.Tags, spec.ManagedKey)
		if len(toRemove) > 0 {
			_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
				return restate.Void{}, api.UntagResource(rc, observed.ARN, toRemove)
			})
			if err != nil {
				return err
			}
		}
		if len(toAdd) > 0 {
			_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
				return restate.Void{}, api.TagResource(rc, observed.ARN, toAdd)
			})
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *LogGroupDriver) scheduleReconcile(ctx restate.ObjectContext, state *LogGroupState) {
	if state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").
		Send(restate.Void{}, restate.WithDelay(drivers.ReconcileInterval))
}

func (d *LogGroupDriver) apiForAccount(ctx restate.ObjectContext, account string) (LogGroupAPI, string, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, "", fmt.Errorf("LogGroupDriver is not configured with an auth registry")
	}
	awsCfg, err := d.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve LogGroup account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), awsCfg.Region, nil
}

func (d *LogGroupDriver) observeLogGroup(ctx restate.ObjectContext, api LogGroupAPI, logGroupName string) (ObservedState, bool, error) {
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (struct {
		Observed ObservedState
		Found    bool
	}, error) {
		obs, ok, runErr := api.DescribeLogGroup(rc, logGroupName)
		if runErr != nil {
			if IsNotFound(runErr) {
				return struct {
					Observed ObservedState
					Found    bool
				}{}, nil
			}
			return struct {
				Observed ObservedState
				Found    bool
			}{}, runErr
		}
		if !ok {
			return struct {
				Observed ObservedState
				Found    bool
			}{}, nil
		}
		if obs.ARN != "" {
			tags, tagErr := api.ListTagsForResource(rc, obs.ARN)
			if tagErr != nil && !IsNotFound(tagErr) {
				return struct {
					Observed ObservedState
					Found    bool
				}{}, tagErr
			}
			obs.Tags = tags
		}
		return struct {
			Observed ObservedState
			Found    bool
		}{Observed: obs, Found: true}, nil
	})
	if err != nil {
		return ObservedState{}, false, err
	}
	return observed.Observed, observed.Found, nil
}

func specFromObserved(observed ObservedState) LogGroupSpec {
	return LogGroupSpec{
		LogGroupName:    observed.LogGroupName,
		LogGroupClass:   observed.LogGroupClass,
		RetentionInDays: observed.RetentionInDays,
		KmsKeyID:        observed.KmsKeyID,
		Tags:            filterPraxisTags(observed.Tags),
	}
}

func outputsFromObserved(observed ObservedState) LogGroupOutputs {
	retention := int32(0)
	if observed.RetentionInDays != nil {
		retention = *observed.RetentionInDays
	}
	return LogGroupOutputs{
		ARN:             observed.ARN,
		LogGroupName:    observed.LogGroupName,
		LogGroupClass:   observed.LogGroupClass,
		RetentionInDays: retention,
		KmsKeyID:        observed.KmsKeyID,
		CreationTime:    observed.CreationTime,
		StoredBytes:     observed.StoredBytes,
	}
}

func defaultImportMode(mode types.Mode) types.Mode {
	if mode == "" {
		return types.ModeObserved
	}
	return mode
}

func applyDefaults(spec LogGroupSpec) LogGroupSpec {
	spec.Region = strings.TrimSpace(spec.Region)
	spec.LogGroupName = strings.TrimSpace(spec.LogGroupName)
	spec.LogGroupClass = strings.TrimSpace(spec.LogGroupClass)
	if spec.LogGroupClass == "" {
		spec.LogGroupClass = "STANDARD"
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	return spec
}

func validateSpec(spec LogGroupSpec) error {
	if strings.TrimSpace(spec.Region) == "" {
		return fmt.Errorf("region is required")
	}
	if strings.TrimSpace(spec.LogGroupName) == "" {
		return fmt.Errorf("logGroupName is required")
	}
	if spec.LogGroupClass != "STANDARD" && spec.LogGroupClass != "INFREQUENT_ACCESS" {
		return fmt.Errorf("logGroupClass must be STANDARD or INFREQUENT_ACCESS")
	}
	return nil
}
