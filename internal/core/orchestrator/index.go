// index.go implements the DeploymentIndex Restate Virtual Object.
//
// Restate virtual objects are key-addressed, but the framework does not provide
// a built-in way to enumerate all keys. The DeploymentIndex solves this by
// storing all deployment summaries under a single well-known key ("global"),
// enabling list/filter operations from the CLI and API.
package orchestrator

import (
	"sort"

	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/pkg/types"
)

// DeploymentIndex stores lightweight deployment summaries behind a fixed,
// globally known key. Restate virtual objects cannot enumerate keys, so list
// semantics require one aggregate object rather than one object per deployment.
//
// The index is eventually consistent with DeploymentStateObj: the workflow
// calls Upsert after each status transition, and Remove when a deployment is
// fully deleted.
type DeploymentIndex struct{}

// ServiceName returns the stable Restate service name for the index object.
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

// ListFilter is the optional input to DeploymentIndex.List.
// When Workspace is non-empty, only deployments tagged with that workspace are returned.
type ListFilter struct {
	Workspace string `json:"workspace,omitempty"`
}

// List returns all summaries in deterministic key order.
// When filter.Workspace is non-empty, only matching deployments are returned.
func (DeploymentIndex) List(ctx restate.ObjectSharedContext, filter ListFilter) ([]types.DeploymentSummary, error) {
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
		summary := entries[key]
		if filter.Workspace != "" && summary.Workspace != filter.Workspace {
			continue
		}
		out = append(out, summary)
	}
	return out, nil
}
