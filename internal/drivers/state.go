package drivers

import (
	"time"

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

// ReconcileInterval is the default delay between reconciliation cycles.
// Each driver schedules its own reconciliation via a Restate delayed message
// (ObjectSend with WithDelay). Durable timers survive restarts because
// Restate persists the scheduled invocation in its journal.
const ReconcileInterval = 5 * time.Minute

// DefaultMode returns ModeManaged if the provided mode is empty.
// This centralizes the default-mode logic so drivers don't duplicate it.
func DefaultMode(m types.Mode) types.Mode {
	if m == "" {
		return types.ModeManaged
	}
	return m
}
