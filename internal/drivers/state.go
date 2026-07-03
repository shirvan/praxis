package drivers

import (
	"hash/fnv"
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

// ReconcileDelayFor returns the delay before the next reconcile for a given
// kind and Virtual Object key, adding deterministic per-key jitter of 0–25% of
// the interval. Without jitter, all resources provisioned in one apply re-fire
// in near-lockstep every interval forever, producing a synchronized fleet-wide
// spike (a "reconcile herd") that hammers the auth service and AWS rate limits.
//
// The jitter is derived from an FNV hash of the key, so it is identical on
// journal replay — no restate.Run and no wall-clock or randomness needed, which
// would otherwise break Restate's deterministic replay.
//
// The hash is SCALED into the [0, interval/4) band rather than taken modulo it:
// a uint32 interpreted as nanoseconds tops out at ~4.3s, so a modulo against
// any band wider than that would never wrap and the jitter would be capped at
// ~4.3s regardless of interval — leaving the herd effectively synchronized.
// Per-mille scaling keeps the arithmetic in integers with no overflow for any
// realistic interval; 1000 distinct slots is ample spread.
func ReconcileDelayFor(kind, key string) time.Duration {
	interval := ReconcileIntervalForKind(kind)
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	// permille < 1000, so the widening uint32→int64 conversion is safe and the
	// product stays far below int64 max for any realistic interval.
	permille := int64(h.Sum32() % 1000)
	jitter := time.Duration(int64(interval/4) * permille / 1000)
	return interval + jitter
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
