package provider

import "github.com/shirvan/praxis/pkg/types"

// TimeoutDefaultsProvider optionally supplies per-kind default resource
// operation timeouts.
type TimeoutDefaultsProvider interface {
	DefaultTimeouts() types.ResourceTimeouts
}