package resolver

import (
	"encoding/json"
	"fmt"

	restate "github.com/restatedev/sdk-go"
)

// RestateSSMResolver wraps SSMResolver so parameter fetches are journaled via
// restate.Run. This makes SSM lookups durable and replay-safe inside Restate
// handlers.
//
// During normal execution, restate.Run calls the inner SSMResolver's
// batchFetchMap to actually hit the AWS SSM API. The result (the resolved
// parameter map) is recorded in Restate's invocation journal. On replay
// (e.g. after a process restart), restate.Run returns the journaled result
// without making the AWS call again. This guarantees that SSM resolution
// happens exactly once per invocation, even if the handler is retried.
type RestateSSMResolver struct {
	inner *SSMResolver
}

// NewRestateSSMResolver constructs a Restate-aware resolver that wraps the
// given SSMResolver. The inner resolver provides the actual AWS SSM client;
// the outer RestateSSMResolver adds durable journaling.
func NewRestateSSMResolver(inner *SSMResolver) *RestateSSMResolver {
	return &RestateSSMResolver{inner: inner}
}

// Resolve resolves SSM URIs from within a Restate handler context.
//
// The fetch function is wrapped in restate.Run so the AWS API call is
// journaled. On replay, the journaled result is returned without re-calling
// AWS. Returns both the hydrated documents and the set of sensitive paths
// that should be masked in user-facing output.
func (r *RestateSSMResolver) Resolve(
	ctx restate.Context,
	rawSpecs map[string]json.RawMessage,
) (map[string]json.RawMessage, *SensitiveParams, error) {
	if r == nil || r.inner == nil {
		return nil, nil, fmt.Errorf("RestateSSMResolver requires a non-nil inner resolver")
	}

	return r.resolveWithFetcher(rawSpecs, func(paths []string) (map[string]string, error) {
		return restate.Run(ctx, func(runCtx restate.RunContext) (map[string]string, error) {
			result, err := r.inner.batchFetchMap(runCtx, paths)
			if err != nil {
				return nil, restate.TerminalError(err, 500)
			}
			return result, nil
		})
	})
}

// resolveWithFetcher delegates to the shared resolveSSMReferences function,
// passing the provided fetch callback. This method exists to allow testing
// with a mock fetcher without requiring a Restate context.
func (r *RestateSSMResolver) resolveWithFetcher(
	rawSpecs map[string]json.RawMessage,
	fetch func(paths []string) (map[string]string, error),
) (map[string]json.RawMessage, *SensitiveParams, error) {
	if r == nil || r.inner == nil {
		return nil, nil, fmt.Errorf("RestateSSMResolver requires a non-nil inner resolver")
	}
	return resolveSSMReferences(rawSpecs, fetch)
}
