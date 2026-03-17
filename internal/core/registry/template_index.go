package registry

import (
	"sort"

	restate "github.com/restatedev/sdk-go"

	"github.com/praxiscloud/praxis/pkg/types"
)

// TemplateIndex stores lightweight template summaries behind a fixed key.
type TemplateIndex struct{}

func (TemplateIndex) ServiceName() string {
	return TemplateIndexServiceName
}

func (TemplateIndex) Upsert(ctx restate.ObjectContext, summary types.TemplateSummary) error {
	entries, err := restate.Get[map[string]types.TemplateSummary](ctx, "entries")
	if err != nil {
		return err
	}
	entries = upsertTemplateEntries(entries, summary)
	restate.Set(ctx, "entries", entries)
	return nil
}

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

func (TemplateIndex) List(ctx restate.ObjectSharedContext, _ restate.Void) ([]types.TemplateSummary, error) {
	entries, err := restate.Get[map[string]types.TemplateSummary](ctx, "entries")
	if err != nil {
		return nil, err
	}
	return listTemplateEntries(entries), nil
}

func upsertTemplateEntries(entries map[string]types.TemplateSummary, summary types.TemplateSummary) map[string]types.TemplateSummary {
	if entries == nil {
		entries = make(map[string]types.TemplateSummary)
	}
	entries[summary.Name] = summary
	return entries
}

func removeTemplateEntry(entries map[string]types.TemplateSummary, name string) map[string]types.TemplateSummary {
	if entries == nil {
		return nil
	}
	delete(entries, name)
	return entries
}

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
