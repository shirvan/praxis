package registry

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/praxiscloud/praxis/pkg/types"
)

func TestUpsertTemplateEntries_CreatesAndUpdates(t *testing.T) {
	now := time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC)
	entries := upsertTemplateEntries(nil, types.TemplateSummary{Name: "webapp", UpdatedAt: now})
	require.Len(t, entries, 1)

	updated := upsertTemplateEntries(entries, types.TemplateSummary{Name: "webapp", Description: "shared", UpdatedAt: now.Add(time.Minute)})
	require.Len(t, updated, 1)
	assert.Equal(t, "shared", updated["webapp"].Description)
}

func TestRemoveTemplateEntry_NoOpForMissing(t *testing.T) {
	entries := map[string]types.TemplateSummary{"webapp": {Name: "webapp"}}
	updated := removeTemplateEntry(entries, "missing")
	require.Len(t, updated, 1)
	assert.Contains(t, updated, "webapp")
}

func TestListTemplateEntries_SortsByName(t *testing.T) {
	entries := map[string]types.TemplateSummary{
		"zeta":  {Name: "zeta"},
		"alpha": {Name: "alpha"},
	}
	listed := listTemplateEntries(entries)
	require.Len(t, listed, 2)
	assert.Equal(t, "alpha", listed[0].Name)
	assert.Equal(t, "zeta", listed[1].Name)
}
