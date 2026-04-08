package provider

import restate "github.com/restatedev/sdk-go"

// WaitReadyResult reports whether a provisioned resource is actually ready for
// downstream consumers.
type WaitReadyResult struct {
	Ready   bool           `json:"ready"`
	Message string         `json:"message,omitempty"`
	Outputs map[string]any `json:"outputs,omitempty"`
}

// ReadyWaiter is an optional adapter hook for post-provision readiness checks.
type ReadyWaiter interface {
	WaitReady(ctx restate.Context, key string) (WaitReadyResult, error)
}