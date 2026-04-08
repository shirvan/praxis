package drivers

import (
	"time"

	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/drivers/awserr"
	"github.com/shirvan/praxis/pkg/types"
)

// StateKey is the single Restate K/V key used by all drivers.
//
// All driver state is stored as one atomic JSON object per Virtual Object
// instance. This prevents torn state: if the handler crashes between two
// separate Set calls (e.g., status updated but observed not), replay would
// see an inconsistent snapshot. A single key guarantees all-or-nothing
// state transitions via one restate.Set() call.
const StateKey = "state"

// DefaultReconcileInterval is the baseline delay between reconciliation cycles.
const DefaultReconcileInterval = 5 * time.Minute

// MinReconcileInterval prevents overly aggressive reconcile schedules.
const MinReconcileInterval = 30 * time.Second

// ReconcileInterval is the current global reconcile cadence. Drivers still use
// this directly today, while newer code can call ReconcileIntervalForKind.
var ReconcileInterval = DefaultReconcileInterval

// ReconcileIntervalForKind returns the reconcile interval for the given kind.
// The current implementation uses the global interval for all resource kinds.
func ReconcileIntervalForKind(string) time.Duration {
	if ReconcileInterval < MinReconcileInterval {
		return MinReconcileInterval
	}
	return ReconcileInterval
}

// DefaultMode returns ModeManaged if the provided mode is empty.
// This centralizes the default-mode logic so drivers don't duplicate it.
func DefaultMode(m types.Mode) types.Mode {
	if m == "" {
		return types.ModeManaged
	}
	return m
}

// IsAccessDenied returns true for AWS authorization failure codes.
// Exported at the drivers package level so individual driver packages can use
// it without importing awserr directly.
func IsAccessDenied(err error) bool {
	return awserr.IsAccessDenied(err)
}

// ClearAllState clears all Virtual Object state.
// Used by the Orphan deletion policy to release a resource from management.
func ClearAllState(ctx restate.ObjectContext) {
	restate.ClearAll(ctx)
}
