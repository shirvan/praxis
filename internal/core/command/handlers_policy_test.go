package command

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/praxiscloud/praxis/pkg/types"
)

func TestPolicyScopeKeyFromRequest_Global(t *testing.T) {
	key, err := policyScopeKeyFromRequest(types.PolicyScopeGlobal, "")
	require.NoError(t, err)
	assert.Equal(t, "global", key)
}

func TestPolicyScopeKeyFromRequest_Template(t *testing.T) {
	key, err := policyScopeKeyFromRequest(types.PolicyScopeTemplate, "webapp")
	require.NoError(t, err)
	assert.Equal(t, "template:webapp", key)
}

func TestPolicyScopeKeyFromRequest_RejectsMissingTemplate(t *testing.T) {
	_, err := policyScopeKeyFromRequest(types.PolicyScopeTemplate, "")
	require.Error(t, err)
}

func TestPolicySummaries(t *testing.T) {
	now := time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC)
	summaries := policySummaries(types.PolicyRecord{
		Scope: types.PolicyScopeTemplate,
		Policies: []types.Policy{{
			Name:         "require-encryption",
			Scope:        types.PolicyScopeTemplate,
			TemplateName: "webapp",
			Description:  "Require encryption",
			CreatedAt:    now,
		}},
	})
	require.Len(t, summaries, 1)
	assert.Equal(t, "require-encryption", summaries[0].Name)
	assert.Equal(t, "webapp", summaries[0].TemplateName)
	assert.Equal(t, now, summaries[0].UpdatedAt)
}
