package types

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPolicyJSONRoundTrip(t *testing.T) {
	createdAt := time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC)
	policy := Policy{
		Name:         "require-encryption",
		Scope:        PolicyScopeTemplate,
		TemplateName: "webapp",
		Source:       "resources: [_]: spec: encryption: enabled: true",
		Digest:       "abc123",
		Description:  "Require encryption",
		CreatedAt:    createdAt,
	}

	encoded, err := json.Marshal(policy)
	require.NoError(t, err)

	var decoded Policy
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	assert.Equal(t, policy, decoded)
}

func TestPolicyRecordOmitsTemplateNameWhenUnset(t *testing.T) {
	encoded, err := json.Marshal(Policy{
		Name:      "global-policy",
		Scope:     PolicyScopeGlobal,
		Source:    "resources: [_]: spec: {}",
		Digest:    "digest",
		CreatedAt: time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "templateName")
}
