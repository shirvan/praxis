package registry

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/praxiscloud/praxis/pkg/types"
)

func TestRegisterTemplateRecord_NewTemplate(t *testing.T) {
	now := time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC)
	record, summary, resp, err := registerTemplateRecord("webapp", nil, types.RegisterTemplateRequest{
		Name:        "webapp",
		Source:      "resources: {}",
		Description: "shared template",
		Labels:      map[string]string{"team": "platform"},
	}, now)
	require.NoError(t, err)
	assert.Equal(t, "webapp", record.Metadata.Name)
	assert.Equal(t, "shared template", record.Metadata.Description)
	assert.Equal(t, map[string]string{"team": "platform"}, record.Metadata.Labels)
	assert.Equal(t, now, record.Metadata.CreatedAt)
	assert.Equal(t, now, record.Metadata.UpdatedAt)
	assert.Equal(t, record.Digest, resp.Digest)
	assert.Equal(t, "webapp", summary.Name)
	assert.Empty(t, record.PreviousSource)
}

func TestRegisterTemplateRecord_UpdateShiftsRollbackBuffer(t *testing.T) {
	now := time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC)
	existing := &types.TemplateRecord{
		Metadata: types.TemplateMetadata{
			Name:        "webapp",
			Description: "existing",
			Labels:      map[string]string{"team": "platform"},
			CreatedAt:   now.Add(-time.Hour),
			UpdatedAt:   now.Add(-time.Hour),
		},
		Source: "resources: { bucket: {} }",
		Digest: templateDigest("resources: { bucket: {} }"),
	}

	record, summary, _, err := registerTemplateRecord("webapp", existing, types.RegisterTemplateRequest{
		Name:   "webapp",
		Source: "resources: {}",
	}, now)
	require.NoError(t, err)
	assert.Equal(t, existing.Source, record.PreviousSource)
	assert.Equal(t, existing.Digest, record.PreviousDigest)
	assert.Equal(t, existing.Metadata.CreatedAt, record.Metadata.CreatedAt)
	assert.Equal(t, "existing", summary.Description)
	assert.Equal(t, map[string]string{"team": "platform"}, record.Metadata.Labels)
}

func TestRegisterTemplateRecord_RejectsInvalidInput(t *testing.T) {
	now := time.Now().UTC()
	_, _, _, err := registerTemplateRecord("webapp", nil, types.RegisterTemplateRequest{Name: "other", Source: "resources: {}"}, now)
	require.Error(t, err)

	_, _, _, err = registerTemplateRecord("webapp", nil, types.RegisterTemplateRequest{Name: "webapp", Source: "not: { cue"}, now)
	require.Error(t, err)

	_, _, _, err = registerTemplateRecord("webapp", nil, types.RegisterTemplateRequest{Name: "webapp"}, now)
	require.Error(t, err)
}

func TestDeleteTemplateRecord(t *testing.T) {
	name, err := deleteTemplateRecord(&types.TemplateRecord{}, types.DeleteTemplateRequest{Name: "webapp"})
	require.NoError(t, err)
	assert.Equal(t, "webapp", name)

	_, err = deleteTemplateRecord(nil, types.DeleteTemplateRequest{Name: "webapp"})
	require.Error(t, err)
}
