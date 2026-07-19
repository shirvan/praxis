package kernel

import (
	"encoding/json"
	"fmt"
	"time"

	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/drivers"
	"github.com/shirvan/praxis/internal/eventing"
	"github.com/shirvan/praxis/pkg/types"
)

// Driver exposes the eight standard Praxis resource handlers while delegating
// provider behavior to a typed descriptor.
type Driver[S, O, Obs any] struct {
	descriptor Descriptor[S, O, Obs]
}

func (d *Driver[S, O, Obs]) ServiceName() string { return d.descriptor.ServiceName }

// GenericLifecycle is a compile-time marker used by production bindings and
// conformance tests. It has no Restate-compatible signature and is therefore
// not exposed as a handler.
func (d *Driver[S, O, Obs]) GenericLifecycle() {}

func selectProvisionResponse[O any](committed O, createOnly *O) O {
	if createOnly != nil {
		return *createOnly
	}
	return committed
}

func (d *Driver[S, O, Obs]) readiness(observed Obs) (ReadinessResult, error) {
	if !d.descriptor.Capabilities.Readiness {
		return ReadinessResult{Phase: ReadinessReady}, nil
	}
	result := d.descriptor.CheckReadiness(observed)
	switch result.Phase {
	case ReadinessReady, ReadinessPending, ReadinessFailed:
		return result, nil
	default:
		return ReadinessResult{}, restate.TerminalError(fmt.Errorf(
			"kernel descriptor %s returned invalid readiness phase %q", d.ServiceName(), result.Phase,
		), 500)
	}
}

func (d *Driver[S, O, Obs]) readinessFailure(result ReadinessResult) error {
	message := result.Message
	if message == "" {
		message = "provider reported a failed asynchronous state"
	}
	return restate.TerminalError(fmt.Errorf("provider readiness failed for %s: %s", d.ServiceName(), message), 409)
}

// correctionFailure preserves terminal provider feedback as durable lifecycle
// visibility, but returns transient failures from the handler. Returning the
// latter lets Restate retry the invocation from its journal, replaying any
// successful provider write instead of executing it a second time.
func (d *Driver[S, O, Obs]) correctionFailure(
	ctx restate.ObjectContext,
	state *State[S, O, Obs],
	err error,
	now time.Time,
	result types.ReconcileResult,
) (types.ReconcileResult, error) {
	result.Error = err.Error()
	if !restate.IsTerminalError(err) {
		return result, err
	}
	markError(state, err, types.ReasonProvisionFailed, now)
	restate.Set(ctx, drivers.StateKey, *state)
	d.scheduleReconcile(ctx, state)
	result.Conditions = state.Conditions
	return result, nil
}

func restoreDesiredOnProvisionConflict[S, O, Obs any](state *State[S, O, Obs], previousDesired S, previousReconcile types.ReconcileMode, previousIgnoreChanges []string, hasPreviousDesired bool, err error) {
	if hasPreviousDesired && restate.IsTerminalError(err) && restate.ErrorCode(err) == 409 {
		state.Desired = previousDesired
		state.Reconcile = previousReconcile
		state.IgnoreChanges = append([]string(nil), previousIgnoreChanges...)
	}
}

func (d *Driver[S, O, Obs]) Provision(ctx restate.ObjectContext, request types.ProvisionRequest) (O, error) {
	var zero O
	if err := validateLifecyclePolicy(request.Lifecycle); err != nil {
		return zero, restate.TerminalError(err, 400)
	}
	var desired S
	if err := json.Unmarshal(request.Spec, &desired); err != nil {
		return zero, restate.TerminalError(fmt.Errorf("decode provision spec: %w", err), 400)
	}
	state, err := loadState[S, O, Obs](ctx)
	if err != nil {
		return zero, err
	}
	previousDesired := state.Desired
	previousReconcile := state.Reconcile
	previousIgnoreChanges := append([]string(nil), state.IgnoreChanges...)
	hasPreviousDesired := state.Generation > 0
	now, err := drivers.CurrentTime(ctx)
	if err != nil {
		return zero, err
	}
	desired, err = d.descriptor.Prepare(ctx, desired)
	if err != nil {
		return zero, err
	}
	if err := d.descriptor.Validate(desired); err != nil {
		return zero, restate.TerminalError(err, 400)
	}

	markProvisioning(&state, desired, request.Lifecycle, now)
	restate.Set(ctx, drivers.StateKey, state)

	observation, err := d.descriptor.Operations.Observe(ctx, desired, state.Outputs)
	if err != nil {
		markError(&state, err, types.ReasonProvisionFailed, now)
		restate.Set(ctx, drivers.StateKey, state)
		return zero, err
	}
	existedBeforeCreate := observation.Exists
	seed := state.Outputs
	var createOnlyResponse *O
	if !observation.Exists {
		created, createErr := d.descriptor.Operations.Create(ctx, desired)
		if createErr != nil {
			markError(&state, createErr, types.ReasonProvisionFailed, now)
			restate.Set(ctx, drivers.StateKey, state)
			return zero, createErr
		}
		seed = created.SeedOutputs
		createOnlyResponse = created.CreateOnlyResponse
		observation, err = d.descriptor.Operations.Observe(ctx, desired, seed)
	}
	if err == nil && observation.Exists && d.descriptor.Capabilities.LateInitialization {
		var changed bool
		desired, changed = d.descriptor.LateInitialize(desired, observation.Value)
		state.Desired = desired
		message := "provider defaults were checked; desired state was already explicit"
		if changed {
			message = "provider-selected defaults were adopted into desired state"
		}
		setCondition(&state, types.ConditionInitialized, types.ConditionTrue, types.ReasonLateInitialized, message, now)
	}
	if err == nil && observation.Exists && existedBeforeCreate && hasPreviousDesired {
		if changeOperations, ok := d.descriptor.Operations.(ProvisionChangeOperations[S, O, Obs]); ok {
			var committed O
			committed, err = changeOperations.ConvergeProvisionChange(ctx, previousDesired, desired, observation.Value, seed)
			if err == nil {
				seed = committed
				observation, err = d.descriptor.Operations.Observe(ctx, desired, seed)
			} else {
				// A failed previous/current comparison rejects the new contract.
				// Keep GetInputs anchored to the last accepted generation instead
				// of persisting an immutable or write-only change that never
				// reached the provider.
				state.Desired = previousDesired
			}
		}
	}
	if err != nil {
		markError(&state, err, types.ReasonProvisionFailed, now)
		restate.Set(ctx, drivers.StateKey, state)
		return zero, err
	}
	if !observation.Exists {
		err = fmt.Errorf("%s resource disappeared during provisioning", d.ServiceName())
		markError(&state, err, types.ReasonProvisionFailed, now)
		restate.Set(ctx, drivers.StateKey, state)
		return zero, err
	}

	readiness, err := d.readiness(observation.Value)
	if err != nil {
		markError(&state, err, types.ReasonProvisionFailed, now)
		restate.Set(ctx, drivers.StateKey, state)
		return zero, err
	}
	if readiness.Phase == ReadinessFailed {
		err = d.readinessFailure(readiness)
		markError(&state, err, types.ReasonProvisionFailed, now)
		state.Observed = observation.Value
		state.Outputs = d.descriptor.OutputsFromObserved(observation.Value, seed)
		restate.Set(ctx, drivers.StateKey, state)
		return zero, err
	}
	if readiness.Phase == ReadinessPending && d.descriptor.Capabilities.ConvergeWhilePending {
		var committed O
		committed, err = d.descriptor.Operations.Converge(ctx, desired, observation.Value, seed)
		if err == nil {
			seed = committed
			observation, err = d.descriptor.Operations.Observe(ctx, desired, seed)
		}
		if err != nil {
			restoreDesiredOnProvisionConflict(&state, previousDesired, previousReconcile, previousIgnoreChanges, hasPreviousDesired, err)
			markError(&state, err, types.ReasonProvisionFailed, now)
			restate.Set(ctx, drivers.StateKey, state)
			return zero, err
		}
		if !observation.Exists {
			err = fmt.Errorf("%s resource disappeared while converging pending state", d.ServiceName())
			markError(&state, err, types.ReasonProvisionFailed, now)
			restate.Set(ctx, drivers.StateKey, state)
			return zero, err
		}
		readiness, err = d.readiness(observation.Value)
		if err != nil {
			markError(&state, err, types.ReasonProvisionFailed, now)
			restate.Set(ctx, drivers.StateKey, state)
			return zero, err
		}
		if readiness.Phase == ReadinessFailed {
			err = d.readinessFailure(readiness)
			markError(&state, err, types.ReasonProvisionFailed, now)
			state.Observed = observation.Value
			state.Outputs = d.descriptor.OutputsFromObserved(observation.Value, seed)
			restate.Set(ctx, drivers.StateKey, state)
			return zero, err
		}
	}
	if readiness.Phase == ReadinessPending {
		state.Observed = observation.Value
		state.Outputs = d.descriptor.OutputsFromObserved(observation.Value, seed)
		markAwaitingReadiness(&state, readiness.Message, now)
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return selectProvisionResponse(state.Outputs, createOnlyResponse), nil
	}
	if d.descriptor.HasDrift(desired, observation.Value) {
		var committed O
		committed, err = d.descriptor.Operations.Converge(ctx, desired, observation.Value, seed)
		if err == nil {
			seed = committed
			observation, err = d.descriptor.Operations.Observe(ctx, desired, seed)
		}
	}
	if err != nil {
		restoreDesiredOnProvisionConflict(&state, previousDesired, previousReconcile, previousIgnoreChanges, hasPreviousDesired, err)
		markError(&state, err, types.ReasonProvisionFailed, now)
		restate.Set(ctx, drivers.StateKey, state)
		return zero, err
	}
	if !observation.Exists {
		err = fmt.Errorf("%s resource disappeared during provisioning", d.ServiceName())
		markError(&state, err, types.ReasonProvisionFailed, now)
		restate.Set(ctx, drivers.StateKey, state)
		return zero, err
	}
	readiness, err = d.readiness(observation.Value)
	if err != nil {
		markError(&state, err, types.ReasonProvisionFailed, now)
		restate.Set(ctx, drivers.StateKey, state)
		return zero, err
	}
	if readiness.Phase == ReadinessFailed {
		err = d.readinessFailure(readiness)
		markError(&state, err, types.ReasonProvisionFailed, now)
		state.Observed = observation.Value
		state.Outputs = d.descriptor.OutputsFromObserved(observation.Value, seed)
		restate.Set(ctx, drivers.StateKey, state)
		return zero, err
	}
	if readiness.Phase == ReadinessPending {
		state.Observed = observation.Value
		state.Outputs = d.descriptor.OutputsFromObserved(observation.Value, seed)
		markAwaitingReadiness(&state, readiness.Message, now)
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return selectProvisionResponse(state.Outputs, createOnlyResponse), nil
	}

	state.Observed = observation.Value
	state.Outputs = d.descriptor.OutputsFromObserved(observation.Value, seed)
	markReady(&state, now)
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return selectProvisionResponse(state.Outputs, createOnlyResponse), nil
}

func (d *Driver[S, O, Obs]) Import(ctx restate.ObjectContext, ref types.ImportRef) (O, error) {
	var zero O
	if !d.descriptor.Capabilities.Import {
		return zero, restate.TerminalError(fmt.Errorf("%s does not support import", d.ServiceName()), 400)
	}
	if ref.ResourceID == "" {
		return zero, restate.TerminalError(fmt.Errorf("resourceId is required"), 400)
	}
	mode := ref.Mode
	if mode == "" {
		mode = types.ModeObserved
	}
	if mode != types.ModeManaged && mode != types.ModeObserved {
		return zero, restate.TerminalError(fmt.Errorf("invalid import mode %q", mode), 400)
	}
	if mode == types.ModeObserved && !d.descriptor.Capabilities.ObservedMode {
		return zero, restate.TerminalError(fmt.Errorf("%s does not support observed mode", d.ServiceName()), 400)
	}

	state, err := loadState[S, O, Obs](ctx)
	if err != nil {
		return zero, err
	}
	now, err := drivers.CurrentTime(ctx)
	if err != nil {
		return zero, err
	}
	state.Version = StateVersion
	state.Status = types.StatusProvisioning
	state.Mode = mode
	if mode == types.ModeObserved {
		state.Reconcile = types.ReconcileModeObserve
	} else {
		state.Reconcile = types.ReconcileModeAuto
	}
	state.IgnoreChanges = nil
	state.Error = ""
	state.Generation++
	setCondition(&state, types.ConditionReady, types.ConditionFalse, types.ReasonDispatched, "import in progress", now)
	restate.Set(ctx, drivers.StateKey, state)

	observation, err := d.descriptor.Operations.Import(ctx, ref)
	if err != nil {
		markError(&state, err, types.ReasonProvisionFailed, now)
		restate.Set(ctx, drivers.StateKey, state)
		return zero, err
	}
	if !observation.Exists {
		err = restate.TerminalError(fmt.Errorf("import failed: %s resource %s does not exist", d.ServiceName(), ref.ResourceID), 404)
		markError(&state, err, types.ReasonNotFound, now)
		restate.Set(ctx, drivers.StateKey, state)
		return zero, err
	}
	desired := d.descriptor.DesiredFromObserved(ref, observation.Value)
	desired, err = d.descriptor.Prepare(ctx, desired)
	if err != nil {
		markError(&state, err, types.ReasonProvisionFailed, now)
		restate.Set(ctx, drivers.StateKey, state)
		return zero, err
	}
	validateImport := d.descriptor.Validate
	if d.descriptor.ValidateImport != nil {
		validateImport = d.descriptor.ValidateImport
	}
	if err := validateImport(desired); err != nil {
		err = restate.TerminalError(err, 400)
		markError(&state, err, types.ReasonProvisionFailed, now)
		restate.Set(ctx, drivers.StateKey, state)
		return zero, err
	}

	state.Desired = desired
	state.Observed = observation.Value
	state.Outputs = d.descriptor.OutputsFromObserved(observation.Value, state.Outputs)
	if d.descriptor.Capabilities.LateInitialization {
		setCondition(&state, types.ConditionInitialized, types.ConditionTrue, types.ReasonLateInitialized, "imported provider values were adopted as desired state", now)
	}
	readiness, readinessErr := d.readiness(observation.Value)
	if readinessErr != nil {
		markError(&state, readinessErr, types.ReasonProvisionFailed, now)
		restate.Set(ctx, drivers.StateKey, state)
		return zero, readinessErr
	}
	if readiness.Phase == ReadinessFailed {
		readinessErr = d.readinessFailure(readiness)
		markError(&state, readinessErr, types.ReasonProvisionFailed, now)
		restate.Set(ctx, drivers.StateKey, state)
		return zero, readinessErr
	}
	if readiness.Phase == ReadinessPending {
		markAwaitingReadiness(&state, readiness.Message, now)
	} else {
		markReady(&state, now)
	}
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return state.Outputs, nil
}

func (d *Driver[S, O, Obs]) Delete(ctx restate.ObjectContext) error {
	state, err := loadState[S, O, Obs](ctx)
	if err != nil {
		return err
	}
	if state.Status == types.StatusDeleted {
		return nil
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(fmt.Errorf("cannot delete %s resource in Observed mode; import it as managed first", d.ServiceName()), 409)
	}
	if !d.descriptor.Capabilities.Delete {
		return restate.TerminalError(fmt.Errorf("%s does not support deletion", d.ServiceName()), 409)
	}
	now, err := drivers.CurrentTime(ctx)
	if err != nil {
		return err
	}
	if state.Status == types.StatusPending && !d.descriptor.Capabilities.Readiness {
		restate.Set(ctx, drivers.StateKey, tombstone[S, O, Obs](state.Generation, now))
		return nil
	}

	state.Status = types.StatusDeleting
	state.Error = ""
	state.ReconcileScheduled = false
	setCondition(&state, types.ConditionReady, types.ConditionFalse, types.ReasonDeleting, "deletion in progress", now)
	restate.Set(ctx, drivers.StateKey, state)
	if err := d.descriptor.Operations.Delete(ctx, state.Desired, state.Outputs); err != nil {
		markError(&state, err, types.ReasonDeleteFailed, now)
		restate.Set(ctx, drivers.StateKey, state)
		return err
	}
	restate.Set(ctx, drivers.StateKey, tombstone[S, O, Obs](state.Generation, now))
	return nil
}

func (d *Driver[S, O, Obs]) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := loadState[S, O, Obs](ctx)
	if err != nil {
		return types.ReconcileResult{}, err
	}
	state.ReconcileScheduled = false
	pendingReadiness := state.Status == types.StatusPending && d.descriptor.Capabilities.Readiness
	if state.Status != types.StatusReady && state.Status != types.StatusError && !pendingReadiness {
		restate.Set(ctx, drivers.StateKey, state)
		return types.ReconcileResult{}, nil
	}
	now, err := drivers.CurrentTime(ctx)
	if err != nil {
		return types.ReconcileResult{}, err
	}
	state.LastReconcile = now.Format(time.RFC3339)

	observation, observeErr := d.descriptor.Operations.Observe(ctx, state.Desired, state.Outputs)
	if observeErr != nil {
		setCondition(&state, types.ConditionHealthy, types.ConditionUnknown, types.ReasonRetrying, observeErr.Error(), now)
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Error: observeErr.Error(), Conditions: state.Conditions}, nil
	}
	if !observation.Exists {
		err = fmt.Errorf("%s resource was deleted externally", d.ServiceName())
		if state.Reconcile == types.ReconcileModeObserve {
			state.Status = types.StatusReady
			state.Error = ""
			setCondition(&state, types.ConditionReady, types.ConditionTrue, types.ReasonSucceeded, "resource is ready under observe-only reconciliation", now)
			setCondition(&state, types.ConditionHealthy, types.ConditionTrue, types.ReasonSucceeded, "provider absence observed; reconciliation writes are disabled", now)
			setCondition(&state, types.ConditionDriftFree, types.ConditionFalse, types.ReasonExternalDelete, err.Error(), now)
			restate.Set(ctx, drivers.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			drivers.ReportDriftEvent(ctx, d.ServiceName(), eventing.DriftEventExternalDelete, err.Error())
			return types.ReconcileResult{Drift: true, Conditions: state.Conditions}, nil
		}
		markError(&state, err, types.ReasonExternalDelete, now)
		setCondition(&state, types.ConditionHealthy, types.ConditionFalse, types.ReasonExternalDelete, err.Error(), now)
		setCondition(&state, types.ConditionDriftFree, types.ConditionFalse, types.ReasonExternalDelete, err.Error(), now)
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		drivers.ReportDriftEvent(ctx, d.ServiceName(), eventing.DriftEventExternalDelete, err.Error())
		return types.ReconcileResult{
			Error: err.Error(), Conditions: state.Conditions,
			ReplacementRequired: state.Mode == types.ModeManaged,
		}, nil
	}

	state.Observed = observation.Value
	observedOutputs := d.descriptor.OutputsFromObserved(observation.Value, state.Outputs)
	readiness, readinessErr := d.readiness(observation.Value)
	if readinessErr != nil {
		setCondition(&state, types.ConditionHealthy, types.ConditionUnknown, types.ReasonRetrying, readinessErr.Error(), now)
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Error: readinessErr.Error(), Conditions: state.Conditions}, nil
	}
	correctingPending := false
	if state.Status != types.StatusError && readiness.Phase == ReadinessFailed {
		state.Outputs = observedOutputs
		readinessErr = d.readinessFailure(readiness)
		markError(&state, readinessErr, types.ReasonProvisionFailed, now)
		setCondition(&state, types.ConditionHealthy, types.ConditionTrue, types.ReasonSucceeded, "provider resource is reachable", now)
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Error: readinessErr.Error(), Conditions: state.Conditions}, nil
	}
	if state.Status != types.StatusError && readiness.Phase == ReadinessPending &&
		state.Mode == types.ModeManaged && state.Reconcile == types.ReconcileModeAuto && d.descriptor.Capabilities.ConvergeWhilePending {
		correctingPending = true
		committed, convergeErr := d.descriptor.Operations.Converge(ctx, state.Desired, observation.Value, state.Outputs)
		if convergeErr != nil {
			return d.correctionFailure(ctx, &state, convergeErr, now, types.ReconcileResult{Correcting: true})
		}
		observation, err = d.descriptor.Operations.Observe(ctx, state.Desired, committed)
		if err != nil || !observation.Exists {
			if err == nil {
				err = restate.TerminalError(fmt.Errorf("%s resource disappeared while converging pending state", d.ServiceName()), 409)
			}
			return d.correctionFailure(ctx, &state, err, now, types.ReconcileResult{Correcting: true})
		}
		state.Observed = observation.Value
		state.Outputs = d.descriptor.OutputsFromObserved(observation.Value, committed)
		observedOutputs = state.Outputs
		readiness, readinessErr = d.readiness(observation.Value)
		if readinessErr != nil {
			return d.correctionFailure(ctx, &state, readinessErr, now, types.ReconcileResult{Correcting: true})
		}
		if readiness.Phase == ReadinessFailed {
			readinessErr = d.readinessFailure(readiness)
			return d.correctionFailure(ctx, &state, readinessErr, now, types.ReconcileResult{Correcting: true})
		}
	}
	if state.Status != types.StatusError && readiness.Phase == ReadinessPending {
		state.Outputs = observedOutputs
		markAwaitingReadiness(&state, readiness.Message, now)
		setCondition(&state, types.ConditionHealthy, types.ConditionTrue, types.ReasonSucceeded, "provider resource is reachable", now)
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Correcting: correctingPending, Conditions: state.Conditions}, nil
	}
	if pendingReadiness && readiness.Phase == ReadinessReady {
		markReady(&state, now)
	}
	drift, driftErr := actionableDrift(d.descriptor, state.Desired, observation.Value, state.IgnoreChanges)
	if driftErr != nil {
		setCondition(&state, types.ConditionHealthy, types.ConditionUnknown, types.ReasonRetrying, driftErr.Error(), now)
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Error: driftErr.Error(), Conditions: state.Conditions}, nil
	}
	setCondition(&state, types.ConditionHealthy, types.ConditionTrue, types.ReasonSucceeded, "provider resource is reachable", now)
	if drift {
		setCondition(&state, types.ConditionDriftFree, types.ConditionFalse, types.ReasonDriftDetected, "provider state differs from desired state", now)
	} else {
		setCondition(&state, types.ConditionDriftFree, types.ConditionTrue, types.ReasonSucceeded, "provider state matches desired state", now)
	}

	// Error state reconciliation remains visibility-only. Provision is the
	// explicit recovery operation and is the only path back to active writes.
	if state.Status == types.StatusError || !drift || state.Mode == types.ModeObserved || state.Reconcile == types.ReconcileModeObserve {
		// Outputs represent the last provider identity Praxis successfully
		// committed. Preserve them while drift is merely being reported so a
		// later visibility-only pass cannot silently adopt the external identity.
		if !drift {
			state.Outputs = observedOutputs
		}
		if drift {
			drivers.ReportDriftEvent(ctx, d.ServiceName(), eventing.DriftEventDetected, "")
		}
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Drift: drift, Correcting: correctingPending, Conditions: state.Conditions}, nil
	}
	drivers.ReportDriftEvent(ctx, d.ServiceName(), eventing.DriftEventDetected, "")
	committed, err := d.descriptor.Operations.Converge(ctx, state.Desired, observation.Value, state.Outputs)
	if err != nil {
		return d.correctionFailure(ctx, &state, err, now, types.ReconcileResult{Drift: true, Correcting: true})
	}
	corrected, err := d.descriptor.Operations.Observe(ctx, state.Desired, committed)
	if err != nil || !corrected.Exists {
		if err == nil {
			err = restate.TerminalError(fmt.Errorf("%s resource disappeared after drift correction", d.ServiceName()), 409)
		}
		return d.correctionFailure(ctx, &state, err, now, types.ReconcileResult{Drift: true, Correcting: true})
	}
	state.Observed = corrected.Value
	state.Outputs = d.descriptor.OutputsFromObserved(corrected.Value, committed)
	correctedReadiness, readinessErr := d.readiness(corrected.Value)
	if readinessErr != nil {
		return d.correctionFailure(ctx, &state, readinessErr, now, types.ReconcileResult{Drift: true, Correcting: true})
	}
	if correctedReadiness.Phase == ReadinessFailed {
		return d.correctionFailure(ctx, &state, d.readinessFailure(correctedReadiness), now, types.ReconcileResult{Drift: true, Correcting: true})
	}
	setCondition(&state, types.ConditionDriftFree, types.ConditionTrue, types.ReasonDriftCorrected, "provider drift was corrected", now)
	if correctedReadiness.Phase == ReadinessPending {
		markAwaitingReadiness(&state, correctedReadiness.Message, now)
	}
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	drivers.ReportDriftEvent(ctx, d.ServiceName(), eventing.DriftEventCorrected, "")
	return types.ReconcileResult{Drift: true, Correcting: true, Conditions: state.Conditions}, nil
}

func (d *Driver[S, O, Obs]) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := loadState[S, O, Obs](ctx)
	if err != nil {
		return types.StatusResponse{}, err
	}
	return types.StatusResponse{
		Status: state.Status, Mode: state.Mode, Reconcile: state.Reconcile,
		IgnoreChanges: append([]string(nil), state.IgnoreChanges...), Generation: state.Generation,
		Error: state.Error, Conditions: state.Conditions,
	}, nil
}

func (d *Driver[S, O, Obs]) GetOutputs(ctx restate.ObjectSharedContext) (O, error) {
	state, err := loadState[S, O, Obs](ctx)
	return state.Outputs, err
}

func (d *Driver[S, O, Obs]) GetInputs(ctx restate.ObjectSharedContext) (S, error) {
	state, err := loadState[S, O, Obs](ctx)
	return state.Desired, err
}

func (d *Driver[S, O, Obs]) ClearState(ctx restate.ObjectContext) error {
	drivers.ClearAllState(ctx)
	return nil
}

func (d *Driver[S, O, Obs]) scheduleReconcile(ctx restate.ObjectContext, state *State[S, O, Obs]) {
	if state.ReconcileScheduled || state.Status == types.StatusDeleted {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, d.ServiceName(), restate.Key(ctx), "Reconcile").Send(
		restate.Void{},
		restate.WithDelay(drivers.ReconcileDelayFor(d.ServiceName(), restate.Key(ctx))),
	)
}
