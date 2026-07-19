package kernel

import (
	"fmt"
	"time"

	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/drivers"
	"github.com/shirvan/praxis/pkg/types"
)

func loadState[S, O, Obs any](ctx restate.ObjectSharedContext) (State[S, O, Obs], error) {
	stored, err := restate.Get[*State[S, O, Obs]](ctx, drivers.StateKey)
	if err != nil {
		return State[S, O, Obs]{}, err
	}
	state, err := normalizeLoadedState(stored)
	if err != nil {
		return state, restate.TerminalError(err, 409)
	}
	return state, nil
}

func normalizeLoadedState[S, O, Obs any](stored *State[S, O, Obs]) (State[S, O, Obs], error) {
	if stored == nil {
		return State[S, O, Obs]{Version: StateVersion, Status: types.StatusPending}, nil
	}
	state := *stored
	if state.Version == "" {
		return state, fmt.Errorf("missing driver state version; expected %q", StateVersion)
	}
	if state.Version != StateVersion {
		return state, fmt.Errorf("unsupported driver state version %q; expected %q", state.Version, StateVersion)
	}
	if !knownStatus(state.Status) {
		return state, fmt.Errorf("unsupported resource status %q in driver state", state.Status)
	}
	if state.Mode != "" && state.Mode != types.ModeManaged && state.Mode != types.ModeObserved {
		return state, fmt.Errorf("unsupported resource mode %q in driver state", state.Mode)
	}
	if state.Reconcile != types.ReconcileModeAuto && state.Reconcile != types.ReconcileModeObserve {
		return state, fmt.Errorf("unsupported reconcile mode %q in driver state", state.Reconcile)
	}
	return state, nil
}

func knownStatus(status types.ResourceStatus) bool {
	switch status {
	case types.StatusPending, types.StatusProvisioning, types.StatusReady, types.StatusError, types.StatusDeleting, types.StatusDeleted:
		return true
	default:
		return false
	}
}

func setCondition[S, O, Obs any](state *State[S, O, Obs], conditionType, status, reason, message string, now time.Time) {
	state.Conditions = types.SetCondition(state.Conditions, types.Condition{
		Type: conditionType, Status: status, Reason: reason, Message: message,
	}, now)
}

func markProvisioning[S, O, Obs any](state *State[S, O, Obs], desired S, lifecycle types.LifecyclePolicy, now time.Time) {
	state.Version = StateVersion
	state.Desired = desired
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Reconcile = lifecycle.Reconcile
	state.IgnoreChanges = append([]string(nil), lifecycle.IgnoreChanges...)
	state.Error = ""
	state.Generation++
	setCondition(state, types.ConditionReady, types.ConditionFalse, types.ReasonDispatched, "provisioning in progress", now)
}

func markReady[S, O, Obs any](state *State[S, O, Obs], now time.Time) {
	state.Status = types.StatusReady
	state.Error = ""
	setCondition(state, types.ConditionReady, types.ConditionTrue, types.ReasonSucceeded, "resource is ready", now)
	setCondition(state, types.ConditionProvisioned, types.ConditionTrue, types.ReasonSucceeded, "resource is provisioned", now)
}

func markAwaitingReadiness[S, O, Obs any](state *State[S, O, Obs], message string, now time.Time) {
	if message == "" {
		message = "waiting for provider readiness"
	}
	state.Status = types.StatusPending
	state.Error = ""
	setCondition(state, types.ConditionProvisioned, types.ConditionTrue, types.ReasonSucceeded, "provider resource is provisioned", now)
	setCondition(state, types.ConditionReady, types.ConditionFalse, types.ReasonRetrying, message, now)
}

func markError[S, O, Obs any](state *State[S, O, Obs], err error, reason string, now time.Time) {
	state.Status = types.StatusError
	state.Error = err.Error()
	setCondition(state, types.ConditionReady, types.ConditionFalse, reason, err.Error(), now)
}

func tombstone[S, O, Obs any](generation int64, now time.Time) State[S, O, Obs] {
	state := State[S, O, Obs]{Version: StateVersion, Status: types.StatusDeleted, Reconcile: types.ReconcileModeAuto, Generation: generation}
	setCondition(&state, types.ConditionReady, types.ConditionFalse, types.ReasonSucceeded, "resource is deleted", now)
	return state
}
