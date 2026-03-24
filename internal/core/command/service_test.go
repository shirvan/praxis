package command

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveRequestAccount_UsesRequestAccount(t *testing.T) {
	service := &PraxisCommandService{}

	account, err := service.resolveRequestAccount("local", nil)
	require.NoError(t, err)
	assert.Equal(t, "local", account)
}

func TestResolveRequestAccount_VariableOverridesRequestAccount(t *testing.T) {
	service := &PraxisCommandService{}

	account, err := service.resolveRequestAccount("ignored", map[string]any{"account": "local"})
	require.NoError(t, err)
	assert.Equal(t, "local", account)
}

func TestResolveRequestAccount_RejectsNonStringVariable(t *testing.T) {
	service := &PraxisCommandService{}

	_, err := service.resolveRequestAccount("local", map[string]any{"account": 42})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "variables.account must be a string")
}
