package kernel

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/pkg/types"
)

func TestNormalizeLoadedStateVersionGate(t *testing.T) {
	t.Run("new", func(t *testing.T) {
		state, err := normalizeLoadedState[string, string, string](nil)
		require.NoError(t, err)
		assert.Equal(t, StateVersion, state.Version)
		assert.Equal(t, types.StatusPending, state.Status)
	})

	t.Run("unversioned persisted state rejected", func(t *testing.T) {
		_, err := normalizeLoadedState(&State[string, string, string]{Status: types.StatusReady, Generation: 1})
		require.ErrorContains(t, err, "missing driver state version")
	})

	t.Run("future version rejected", func(t *testing.T) {
		_, err := normalizeLoadedState(&State[string, string, string]{Version: "beta", Status: types.StatusReady})
		require.ErrorContains(t, err, "unsupported driver state version")
	})

	t.Run("corrupt enum rejected", func(t *testing.T) {
		_, err := normalizeLoadedState(&State[string, string, string]{Version: StateVersion, Status: "Warming"})
		require.ErrorContains(t, err, "unsupported resource status")
	})
}
