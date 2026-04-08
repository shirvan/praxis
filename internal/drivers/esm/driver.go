package esm

import (
	"encoding/base64"
	"fmt"
	"slices"
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

// EventSourceMappingDriver implements the Praxis driver for Lambda Event Source Mappings.
type EventSourceMappingDriver struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) ESMAPI
}

// NewEventSourceMappingDriver creates a production driver with default Lambda client factory.
func NewEventSourceMappingDriver(auth authservice.AuthClient) *EventSourceMappingDriver {
	return NewEventSourceMappingDriverWithFactory(auth, func(cfg aws.Config) ESMAPI {
		return NewESMAPI(awsclient.NewLambdaClient(cfg))
	})
}

// NewEventSourceMappingDriverWithFactory creates a driver with a custom ESMAPI factory (for testing).
func NewEventSourceMappingDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) ESMAPI) *EventSourceMappingDriver {
	if factory == nil {
		factory = func(cfg aws.Config) ESMAPI { return NewESMAPI(awsclient.NewLambdaClient(cfg)) }
	}
	return &EventSourceMappingDriver{auth: auth, apiFactory: factory}
}

func (d *EventSourceMappingDriver) ServiceName() string { return ServiceName }

// Provision creates or updates an Event Source Mapping.
//
// Flow:
//  1. Validate required fields, apply defaults.
//  2. If no UUID exists: search for existing mapping (FindEventSourceMapping for
//     idempotency), then create if not found, wait for stable state.
//  3. If UUID exists: check immutable field violations (startingPosition, eventSourceArn),
//     then update the mapping and wait for stable state.
//  4. Final GetEventSourceMapping to capture outputs. Set status=Ready.
func (d *EventSourceMappingDriver) Provision(ctx restate.ObjectContext, spec EventSourceMappingSpec) (EventSourceMappingOutputs, error) {
	api, region, err := d.apiForAccount(ctx, spec.Account)
	if err != nil {
		return EventSourceMappingOutputs{}, restate.TerminalError(err, 400)
	}
	spec = applyDefaults(spec)
	if spec.Region == "" {
		spec.Region = region
	}
	if err := validateProvisionSpec(spec); err != nil {
		return EventSourceMappingOutputs{}, restate.TerminalError(err, 400)
	}
	state, err := restate.Get[EventSourceMappingState](ctx, drivers.StateKey)
	if err != nil {
		return EventSourceMappingOutputs{}, err
	}
	previousDesired := state.Desired
	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++
	if state.Outputs.UUID == "" {
		outputs, createErr := d.createMapping(ctx, api, spec)
		if createErr != nil {
			state.Status = types.StatusError
			state.Error = createErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			if IsInvalidParameter(createErr) {
				return EventSourceMappingOutputs{}, restate.TerminalError(createErr, 400)
			}
			return EventSourceMappingOutputs{}, createErr
		}
		state.Outputs = outputs
		state.Observed, err = restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			return api.GetEventSourceMapping(rc, outputs.UUID)
		})
		if err != nil {
			state.Status = types.StatusError
			state.Error = err.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return EventSourceMappingOutputs{}, err
		}
		state.Outputs = outputsFromObserved(state.Observed)
		state.Status = types.StatusReady
		state.Error = ""
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return state.Outputs, nil
	}
	if startingPositionChanged(previousDesired, spec) || (previousDesired.EventSourceArn != "" && previousDesired.EventSourceArn != spec.EventSourceArn) {
		return EventSourceMappingOutputs{}, restate.TerminalError(fmt.Errorf("startingPosition and eventSourceArn are immutable for event source mappings"), 409)
	}
	_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		return restate.Void{}, api.UpdateEventSourceMapping(rc, state.Outputs.UUID, spec)
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return EventSourceMappingOutputs{}, err
	}
	_, err = restate.Run(ctx, func(rc restate.RunContext) (string, error) { return api.WaitForStableState(rc, state.Outputs.UUID) })
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return EventSourceMappingOutputs{}, err
	}
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		return api.GetEventSourceMapping(rc, state.Outputs.UUID)
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return EventSourceMappingOutputs{}, err
	}
	state.Observed = observed
	state.Outputs = outputsFromObserved(observed)
	state.Status = types.StatusReady
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return state.Outputs, nil
}

// Import adopts an existing Event Source Mapping by UUID into Praxis management.
func (d *EventSourceMappingDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (EventSourceMappingOutputs, error) {
	api, region, err := d.apiForAccount(ctx, ref.Account)
	if err != nil {
		return EventSourceMappingOutputs{}, restate.TerminalError(err, 400)
	}
	state, err := restate.Get[EventSourceMappingState](ctx, drivers.StateKey)
	if err != nil {
		return EventSourceMappingOutputs{}, err
	}
	state.Generation++
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		return api.GetEventSourceMapping(rc, ref.ResourceID)
	})
	if err != nil {
		if IsNotFound(err) {
			return EventSourceMappingOutputs{}, restate.TerminalError(fmt.Errorf("import failed: event source mapping %s does not exist", ref.ResourceID), 404)
		}
		return EventSourceMappingOutputs{}, err
	}
	spec := specFromObserved(observed)
	spec.Account = ref.Account
	spec.Region = region
	state.Desired = spec
	state.Observed = observed
	state.Outputs = outputsFromObserved(observed)
	state.Status = types.StatusReady
	state.Mode = defaultImportMode(ref.Mode)
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return state.Outputs, nil
}

// Delete removes the Event Source Mapping and waits for deletion to complete.
// Observed-mode resources cannot be deleted (409).
func (d *EventSourceMappingDriver) Delete(ctx restate.ObjectContext) error {
	state, err := restate.Get[EventSourceMappingState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	if state.Status == types.StatusDeleted {
		return nil
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(fmt.Errorf("cannot delete EventSourceMapping %s: resource is in Observed mode; re-import with --mode managed to allow deletion", state.Outputs.UUID), 409)
	}
	if state.Outputs.UUID == "" {
		restate.Set(ctx, drivers.StateKey, EventSourceMappingState{Status: types.StatusDeleted})
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
		runErr := api.DeleteEventSourceMapping(rc, state.Outputs.UUID)
		if runErr != nil {
			if IsNotFound(runErr) {
				return restate.Void{}, nil
			}
			if IsConflict(runErr) {
				return restate.Void{}, restate.TerminalError(runErr, 409)
			}
			if IsInvalidParameter(runErr) {
				return restate.Void{}, restate.TerminalError(runErr, 400)
			}
			if drivers.IsAccessDenied(runErr) {
				return restate.Void{}, restate.TerminalError(runErr, 403)
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
	if _, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) { return api.WaitForStableState(rc, state.Outputs.UUID) }); err != nil && !IsNotFound(err) {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return err
	}
	restate.Set(ctx, drivers.StateKey, EventSourceMappingState{Status: types.StatusDeleted})
	return nil
}

// Reconcile is the periodic drift-detection loop for Event Source Mappings.
// Detects external deletion and configuration drift. No auto-correction is
// performed — drift is reported only.
func (d *EventSourceMappingDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[EventSourceMappingState](ctx, drivers.StateKey)
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
	if state.Outputs.UUID == "" {
		restate.Set(ctx, drivers.StateKey, state)
		return types.ReconcileResult{}, nil
	}
	now, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) { return time.Now().UTC().Format(time.RFC3339), nil })
	if err != nil {
		return types.ReconcileResult{}, err
	}
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		return api.GetEventSourceMapping(rc, state.Outputs.UUID)
	})
	if err != nil {
		if IsNotFound(err) {
			state.Status = types.StatusError
			state.Error = fmt.Sprintf("event source mapping %s was deleted externally", state.Outputs.UUID)
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
	state.Outputs = outputsFromObserved(observed)
	state.LastReconcile = now
	drift := HasDrift(state.Desired, observed)
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	if drift {
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
	}
	return types.ReconcileResult{Drift: drift}, nil
}

// GetStatus returns the current lifecycle status (shared/concurrent handler).
func (d *EventSourceMappingDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[EventSourceMappingState](ctx, drivers.StateKey)
	if err != nil {
		return types.StatusResponse{}, err
	}
	return types.StatusResponse{Status: state.Status, Mode: state.Mode, Generation: state.Generation, Error: state.Error}, nil
}

// GetOutputs returns the provisioned outputs (shared/concurrent handler).
func (d *EventSourceMappingDriver) GetOutputs(ctx restate.ObjectSharedContext) (EventSourceMappingOutputs, error) {
	state, err := restate.Get[EventSourceMappingState](ctx, drivers.StateKey)
	if err != nil {
		return EventSourceMappingOutputs{}, err
	}
	return state.Outputs, nil
}

// GetInputs is a shared (read-only) handler that returns the desired input spec.
func (d *EventSourceMappingDriver) GetInputs(ctx restate.ObjectSharedContext) (EventSourceMappingSpec, error) {
	state, err := restate.Get[EventSourceMappingState](ctx, drivers.StateKey)
	if err != nil {
		return EventSourceMappingSpec{}, err
	}
	return state.Desired, nil
}

// scheduleReconcile enqueues a delayed Reconcile message with dedup guard.
func (d *EventSourceMappingDriver) scheduleReconcile(ctx restate.ObjectContext, state *EventSourceMappingState) {
	if state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").Send(restate.Void{}, restate.WithDelay(drivers.ReconcileIntervalForKind(ServiceName)))
}

// apiForAccount resolves AWS credentials and creates an ESMAPI for the given Praxis account.
func (d *EventSourceMappingDriver) apiForAccount(ctx restate.ObjectContext, account string) (ESMAPI, string, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, "", fmt.Errorf("EventSourceMappingDriver is not configured with an auth registry")
	}
	awsCfg, err := d.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve event source mapping account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), awsCfg.Region, nil
}

// applyDefaults normalizes FunctionResponseTypes (nil → empty, sorted).
func applyDefaults(spec EventSourceMappingSpec) EventSourceMappingSpec {
	if spec.FunctionResponseTypes == nil {
		spec.FunctionResponseTypes = []string{}
	} else {
		spec.FunctionResponseTypes = append([]string(nil), spec.FunctionResponseTypes...)
		slices.Sort(spec.FunctionResponseTypes)
	}
	return spec
}

// validateProvisionSpec checks that region, functionName, and eventSourceArn are set.
func validateProvisionSpec(spec EventSourceMappingSpec) error {
	if strings.TrimSpace(spec.Region) == "" {
		return fmt.Errorf("region is required")
	}
	if strings.TrimSpace(spec.FunctionName) == "" {
		return fmt.Errorf("functionName is required")
	}
	if strings.TrimSpace(spec.EventSourceArn) == "" {
		return fmt.Errorf("eventSourceArn is required")
	}
	return nil
}

// createMapping handles first-time creation with idempotency check.
// Uses FindEventSourceMapping to detect pre-existing mappings for the same
// function+eventSource pair, avoiding duplicate creation.
func (d *EventSourceMappingDriver) createMapping(ctx restate.ObjectContext, api ESMAPI, spec EventSourceMappingSpec) (EventSourceMappingOutputs, error) {
	foundUUID, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
		return api.FindEventSourceMapping(rc, spec.FunctionName, spec.EventSourceArn)
	})
	if err != nil {
		return EventSourceMappingOutputs{}, err
	}
	if foundUUID != "" {
		observed, getErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) { return api.GetEventSourceMapping(rc, foundUUID) })
		if getErr != nil {
			return EventSourceMappingOutputs{}, getErr
		}
		return outputsFromObserved(observed), nil
	}
	outputs, err := restate.Run(ctx, func(rc restate.RunContext) (EventSourceMappingOutputs, error) {
		return api.CreateEventSourceMapping(rc, spec)
	})
	if err != nil {
		return EventSourceMappingOutputs{}, err
	}
	_, err = restate.Run(ctx, func(rc restate.RunContext) (string, error) { return api.WaitForStableState(rc, outputs.UUID) })
	if err != nil {
		return EventSourceMappingOutputs{}, err
	}
	return outputs, nil
}

// startingPositionChanged returns true if startingPosition or its timestamp changed.
func startingPositionChanged(oldSpec, newSpec EventSourceMappingSpec) bool {
	if oldSpec.StartingPosition != newSpec.StartingPosition {
		return true
	}
	if oldSpec.StartingPositionTimestamp == nil && newSpec.StartingPositionTimestamp == nil {
		return false
	}
	if oldSpec.StartingPositionTimestamp == nil || newSpec.StartingPositionTimestamp == nil {
		return true
	}
	return *oldSpec.StartingPositionTimestamp != *newSpec.StartingPositionTimestamp
}

// specFromObserved reconstructs an EventSourceMappingSpec from observed AWS state for Import.
func specFromObserved(observed ObservedState) EventSourceMappingSpec {
	enabled := observed.State != "Disabled"
	return applyDefaults(EventSourceMappingSpec{FunctionName: observed.FunctionArn, EventSourceArn: observed.EventSourceArn, Enabled: enabled, BatchSize: int32Ptr(observed.BatchSize), MaximumBatchingWindowInSeconds: int32Ptr(observed.MaximumBatchingWindowInSeconds), StartingPosition: observed.StartingPosition, FilterCriteria: observed.FilterCriteria, BisectBatchOnFunctionError: boolPtr(observed.BisectBatchOnFunctionError), MaximumRetryAttempts: observed.MaximumRetryAttempts, MaximumRecordAgeInSeconds: observed.MaximumRecordAgeInSeconds, ParallelizationFactor: int32Ptr(observed.ParallelizationFactor), TumblingWindowInSeconds: int32Ptr(observed.TumblingWindowInSeconds), DestinationConfig: observed.DestinationConfig, ScalingConfig: observed.ScalingConfig, FunctionResponseTypes: append([]string(nil), observed.FunctionResponseTypes...)})
}

// defaultImportMode returns Observed if no mode was explicitly specified.
func defaultImportMode(mode types.Mode) types.Mode {
	if mode == "" {
		return types.ModeObserved
	}
	return mode
}

func int32Ptr(value int32) *int32 { return &value }
func boolPtr(value bool) *bool    { return &value }

// EncodedEventSourceKey produces a URL-safe base64 encoding of the ARN for use as a Restate key.
func EncodedEventSourceKey(eventSourceArn string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(eventSourceArn))
}

// ClearState clears all Virtual Object state for this resource.
// Used by the Orphan deletion policy to release a resource from management.
func (d *EventSourceMappingDriver) ClearState(ctx restate.ObjectContext) error {
	drivers.ClearAllState(ctx)
	return nil
}
