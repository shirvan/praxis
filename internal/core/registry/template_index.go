package registry

import (
	"sort"

	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/pkg/types"
)

// TemplateIndex is a Restate Virtual Object that maintains a global
// name → TemplateSummary index behind a single fixed key ("global").
//
// This design avoids a full scan of all TemplateRegistry objects when the
// CLI needs to list available templates. The TemplateRegistry sends
// one-way messages (ObjectSend) to this index on every register/delete,
// so the index is eventually consistent with the individual records.
//
// Because there is only one key ("global"), all mutations are serialized
// by Restate, preventing lost updates from concurrent template registrations.
type TemplateIndex struct{}

// ServiceName returns the Restate service name for this Virtual Object.
func (TemplateIndex) ServiceName() string {
	return TemplateIndexServiceName
}

// Upsert adds or updates a template summary in the index. Called via one-way
// message from TemplateRegistry.Register.
func (TemplateIndex) Upsert(ctx restate.ObjectContext, summary types.TemplateSummary) error {
	entries, err := restate.Get[map[string]types.TemplateSummary](ctx, "entries")
	if err != nil {
		return err
	}
	entries = upsertTemplateEntries(entries, summary)
	restate.Set(ctx, "entries", entries)
	return nil
}

// Remove deletes a template summary from the index. Called via one-way
// message from TemplateRegistry.Delete.
func (TemplateIndex) Remove(ctx restate.ObjectContext, name string) error {
	entries, err := restate.Get[map[string]types.TemplateSummary](ctx, "entries")
	if err != nil {
		return err
	}
	entries = removeTemplateEntry(entries, name)
	if entries != nil {
		restate.Set(ctx, "entries", entries)
	}
	return nil
}

// List returns all template summaries sorted alphabetically by name.
// This is a shared (read-only) handler, so it does not block writes.
func (TemplateIndex) List(ctx restate.ObjectSharedContext, _ restate.Void) ([]types.TemplateSummary, error) {
	entries, err := restate.Get[map[string]types.TemplateSummary](ctx, "entries")
	if err != nil {
		return nil, err
	}
	return listTemplateEntries(entries), nil
}

// upsertTemplateEntries inserts or replaces a summary in the map, lazily
// initializing the map on first use.
func upsertTemplateEntries(entries map[string]types.TemplateSummary, summary types.TemplateSummary) map[string]types.TemplateSummary {
	if entries == nil {
		entries = make(map[string]types.TemplateSummary)
	}
	entries[summary.Name] = summary
	return entries
}

// removeTemplateEntry deletes a summary by name. Returns nil if the map was
// already nil (no state to persist).
func removeTemplateEntry(entries map[string]types.TemplateSummary, name string) map[string]types.TemplateSummary {
	if entries == nil {
		return nil
	}
	delete(entries, name)
	return entries
}

// listTemplateEntries converts the map into a sorted slice for deterministic
// API responses.
func listTemplateEntries(entries map[string]types.TemplateSummary) []types.TemplateSummary {
	if len(entries) == 0 {
		return nil
	}
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]types.TemplateSummary, 0, len(keys))
	for _, key := range keys {
		out = append(out, entries[key])
	}
	return out
}
