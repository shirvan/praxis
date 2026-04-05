// resource_index.go implements the ResourceIndex Restate Virtual Object.
//
// The ResourceIndex stores denormalized resource entries across all deployments
// under a single well-known key ("global"). This enables fast queries by
// resource Kind across all deployments without scanning every DeploymentStateObj.
//
// The index is eventually consistent with DeploymentStateObj: workflows upsert
// entries when resources transition to Ready or Error, and remove entries when
// resources are Deleted. The command pipeline seeds initial Pending entries at
// deployment submission time.
package orchestrator

import (
	"sort"
	"time"

	restate "github.com/restatedev/sdk-go"
)

// ResourceIndex stores lightweight resource entries behind a fixed, globally
// known key. Like DeploymentIndex, it works around Restate's inability to
// enumerate Virtual Object keys.
type ResourceIndex struct{}

// ServiceName returns the stable Restate service name for the resource index.
func (ResourceIndex) ServiceName() string {
	return ResourceIndexServiceName
}

// ResourceIndexEntry is a denormalized resource record stored in the index.
type ResourceIndexEntry struct {
	Kind          string    `json:"kind"`
	Key           string    `json:"key"`
	DeploymentKey string    `json:"deploymentKey"`
	ResourceName  string    `json:"resourceName"`
	Workspace     string    `json:"workspace,omitempty"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"createdAt"`
}

// ResourceQuery is the input to ResourceIndex.Query.
type ResourceQuery struct {
	Kind      string `json:"kind,omitempty"`
	Workspace string `json:"workspace,omitempty"`
}

// ResourceIndexRemoveRequest identifies the entry to remove.
type ResourceIndexRemoveRequest struct {
	DeploymentKey string `json:"deploymentKey"`
	ResourceName  string `json:"resourceName"`
}

// resourceEntryKey returns the composite map key for an entry.
func resourceEntryKey(deploymentKey, resourceName string) string {
	return deploymentKey + "~" + resourceName
}

// Upsert inserts or replaces a resource entry.
func (ResourceIndex) Upsert(ctx restate.ObjectContext, entry ResourceIndexEntry) error {
	entries, err := restate.Get[map[string]ResourceIndexEntry](ctx, "entries")
	if err != nil {
		return err
	}
	if entries == nil {
		entries = make(map[string]ResourceIndexEntry)
	}
	key := resourceEntryKey(entry.DeploymentKey, entry.ResourceName)
	entries[key] = entry
	restate.Set(ctx, "entries", entries)
	return nil
}

// Remove deletes a single resource entry by deployment key and resource name.
func (ResourceIndex) Remove(ctx restate.ObjectContext, req ResourceIndexRemoveRequest) error {
	entries, err := restate.Get[map[string]ResourceIndexEntry](ctx, "entries")
	if err != nil {
		return err
	}
	if entries == nil {
		return nil
	}
	key := resourceEntryKey(req.DeploymentKey, req.ResourceName)
	delete(entries, key)
	restate.Set(ctx, "entries", entries)
	return nil
}

// RemoveByDeployment deletes all entries for a given deployment.
func (ResourceIndex) RemoveByDeployment(ctx restate.ObjectContext, deploymentKey string) error {
	entries, err := restate.Get[map[string]ResourceIndexEntry](ctx, "entries")
	if err != nil {
		return err
	}
	if entries == nil {
		return nil
	}
	for key, entry := range entries {
		if entry.DeploymentKey == deploymentKey {
			delete(entries, key)
		}
	}
	restate.Set(ctx, "entries", entries)
	return nil
}

// Query returns resource entries matching the given criteria.
// Results are returned in deterministic order sorted by composite key.
func (ResourceIndex) Query(ctx restate.ObjectSharedContext, query ResourceQuery) ([]ResourceIndexEntry, error) {
	entries, err := restate.Get[map[string]ResourceIndexEntry](ctx, "entries")
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

	out := make([]ResourceIndexEntry, 0, len(keys))
	for _, key := range keys {
		entry := entries[key]
		if query.Kind != "" && entry.Kind != query.Kind {
			continue
		}
		if query.Workspace != "" && entry.Workspace != query.Workspace {
			continue
		}
		out = append(out, entry)
	}
	return out, nil
}
