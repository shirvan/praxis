package command

import (
	"testing"

	"github.com/praxiscloud/praxis/internal/core/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveRequestAccount_UsesRequestAccount(t *testing.T) {
	t.Setenv("PRAXIS_ACCOUNT_NAME", "local")
	service := &PraxisCommandService{auth: auth.LoadFromEnv()}

	account, err := service.resolveRequestAccount("local", nil)
	require.NoError(t, err)
	assert.Equal(t, "local", account.Name)
}

func TestResolveRequestAccount_VariableOverridesRequestAccount(t *testing.T) {
	t.Setenv("PRAXIS_ACCOUNT_NAME", "local")
	service := &PraxisCommandService{auth: auth.LoadFromEnv()}

	account, err := service.resolveRequestAccount("ignored", map[string]any{"account": "local"})
	require.NoError(t, err)
	assert.Equal(t, "local", account.Name)
}

func TestResolveRequestAccount_RejectsNonStringVariable(t *testing.T) {
	t.Setenv("PRAXIS_ACCOUNT_NAME", "local")
	service := &PraxisCommandService{auth: auth.LoadFromEnv()}

	_, err := service.resolveRequestAccount("local", map[string]any{"account": 42})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "variables.account must be a string")
}
