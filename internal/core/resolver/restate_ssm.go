package resolver

import (
	"encoding/json"
	"fmt"

	restate "github.com/restatedev/sdk-go"
)

// RestateSSMResolver wraps SSMResolver so parameter fetches are journaled via
// restate.Run. This makes SSM lookups durable and replay-safe inside Restate
// handlers.
type RestateSSMResolver struct {
	inner *SSMResolver
}

// NewRestateSSMResolver constructs a Restate-aware resolver on top of the
// existing AWS-backed SSM resolver.
func NewRestateSSMResolver(inner *SSMResolver) *RestateSSMResolver {
	return &RestateSSMResolver{inner: inner}
}

// Resolve resolves SSM URIs exactly once from a Restate handler context and
// returns both the hydrated documents and the set of sensitive paths that should
// be masked in user-facing output.
func (r *RestateSSMResolver) Resolve(
	ctx restate.Context,
	rawSpecs map[string]json.RawMessage,
) (map[string]json.RawMessage, *SensitiveParams, error) {
	if r == nil || r.inner == nil {
		return nil, nil, fmt.Errorf("RestateSSMResolver requires a non-nil inner resolver")
	}

	return r.resolveWithFetcher(rawSpecs, func(paths []string) (map[string]string, error) {
		return restate.Run(ctx, func(runCtx restate.RunContext) (map[string]string, error) {
			return r.inner.batchFetchMap(runCtx, paths)
		})
	})
}

func (r *RestateSSMResolver) resolveWithFetcher(
	rawSpecs map[string]json.RawMessage,
	fetch func(paths []string) (map[string]string, error),
) (map[string]json.RawMessage, *SensitiveParams, error) {
	if r == nil || r.inner == nil {
		return nil, nil, fmt.Errorf("RestateSSMResolver requires a non-nil inner resolver")
	}
	return resolveSSMReferences(rawSpecs, fetch)
}
