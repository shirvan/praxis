package orchestrator

import (
	"sort"

	restate "github.com/restatedev/sdk-go"

	"github.com/praxiscloud/praxis/pkg/types"
)

// DeploymentIndex stores lightweight deployment summaries behind a fixed,
// globally known key. Restate virtual objects cannot enumerate keys, so list
// semantics need one aggregate object rather than one object per deployment.
type DeploymentIndex struct{}

func (DeploymentIndex) ServiceName() string {
	return DeploymentIndexServiceName
}

// Upsert inserts or replaces a deployment summary.
func (DeploymentIndex) Upsert(ctx restate.ObjectContext, summary types.DeploymentSummary) error {
	entries, err := restate.Get[map[string]types.DeploymentSummary](ctx, "entries")
	if err != nil {
		return err
	}
	if entries == nil {
		entries = make(map[string]types.DeploymentSummary)
	}
	entries[summary.Key] = summary
	restate.Set(ctx, "entries", entries)
	return nil
}

// Remove deletes a summary from the global listing.
func (DeploymentIndex) Remove(ctx restate.ObjectContext, key string) error {
	entries, err := restate.Get[map[string]types.DeploymentSummary](ctx, "entries")
	if err != nil {
		return err
	}
	if entries == nil {
		return nil
	}
	delete(entries, key)
	restate.Set(ctx, "entries", entries)
	return nil
}

// List returns all summaries in deterministic key order.
func (DeploymentIndex) List(ctx restate.ObjectSharedContext, _ restate.Void) ([]types.DeploymentSummary, error) {
	entries, err := restate.Get[map[string]types.DeploymentSummary](ctx, "entries")
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, nil
	}

	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	out := make([]types.DeploymentSummary, 0, len(keys))
	for _, key := range keys {
		out = append(out, entries[key])
	}
	return out, nil
}
