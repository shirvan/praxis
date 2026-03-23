package registry

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/pkg/types"
)

func TestPolicyScopeKeyRoundTrip(t *testing.T) {
	key := PolicyScopeKey(types.PolicyScopeTemplate, "webapp")
	assert.Equal(t, "template:webapp", key)

	scope, templateName, err := ParsePolicyScopeKey(key)
	require.NoError(t, err)
	assert.Equal(t, types.PolicyScopeTemplate, scope)
	assert.Equal(t, "webapp", templateName)
}

func TestAddPolicyRecord_AddsPolicy(t *testing.T) {
	now := time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC)
	record, err := addPolicyRecord("global", nil, types.AddPolicyRequest{
		Name:   "require-encryption",
		Scope:  types.PolicyScopeGlobal,
		Source: `resources: [_]: spec: encryption: enabled: true`,
	}, now)
	require.NoError(t, err)
	require.Len(t, record.Policies, 1)
	assert.Equal(t, types.PolicyScopeGlobal, record.Scope)
	assert.Equal(t, "require-encryption", record.Policies[0].Name)
	assert.NotEmpty(t, record.Policies[0].Digest)
	assert.Equal(t, now, record.Policies[0].CreatedAt)
}

func TestAddPolicyRecord_RejectsDuplicate(t *testing.T) {
	now := time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC)
	_, err := addPolicyRecord("global", &types.PolicyRecord{
		Scope: types.PolicyScopeGlobal,
		Policies: []types.Policy{{
			Name: "require-encryption",
		}},
	}, types.AddPolicyRequest{
		Name:   "require-encryption",
		Scope:  types.PolicyScopeGlobal,
		Source: `resources: [_]: spec: encryption: enabled: true`,
	}, now)
	require.Error(t, err)
}

func TestRemovePolicyRecord_RemovesPolicy(t *testing.T) {
	updated, err := removePolicyRecord(&types.PolicyRecord{
		Scope: types.PolicyScopeGlobal,
		Policies: []types.Policy{{
			Name: "require-encryption",
		}, {
			Name: "require-tags",
		}},
	}, "require-encryption")
	require.NoError(t, err)
	require.Len(t, updated.Policies, 1)
	assert.Equal(t, "require-tags", updated.Policies[0].Name)
}

func TestFindPolicy_ReturnsExisting(t *testing.T) {
	policy, err := findPolicy(&types.PolicyRecord{Policies: []types.Policy{{Name: "require-encryption"}}}, "require-encryption")
	require.NoError(t, err)
	assert.Equal(t, "require-encryption", policy.Name)
}
