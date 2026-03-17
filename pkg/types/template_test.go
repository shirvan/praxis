package types

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTemplateRecordJSONRoundTrip(t *testing.T) {
	now := time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC)
	record := TemplateRecord{
		Metadata: TemplateMetadata{
			Name:        "webapp",
			Description: "shared web template",
			Labels:      map[string]string{"team": "platform"},
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		Source:         "resources: {}",
		Digest:         "abc123",
		PreviousSource: "resources: { old: {} }",
		PreviousDigest: "def456",
	}

	encoded, err := json.Marshal(record)
	require.NoError(t, err)

	var decoded TemplateRecord
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	assert.Equal(t, record, decoded)
}

func TestTemplateRefOmitempty(t *testing.T) {
	encoded, err := json.Marshal(ApplyRequest{Template: "resources: {}"})
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "templateRef")
}
