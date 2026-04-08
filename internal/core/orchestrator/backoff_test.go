package orchestrator

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/pkg/types"
)

func TestResolveRetryConfig_Defaults(t *testing.T) {
	resolved, err := resolveRetryConfig(DeploymentPlan{}, nil)
	require.NoError(t, err)
	assert.Equal(t, defaultRetryMaxRetries, resolved.MaxRetries)
	assert.Equal(t, defaultRetryBaseDelay, resolved.BaseDelay)
	assert.Equal(t, defaultRetryMaxDelay, resolved.MaxDelay)
}

func TestResolveRetryConfig_LifecycleOverrides(t *testing.T) {
	maxRetries := 5
	resolved, err := resolveRetryConfig(DeploymentPlan{}, &types.LifecyclePolicy{
		Retry: &types.RetryPolicy{
			MaxRetries: &maxRetries,
			BaseDelay:  "7s",
			MaxDelay:   "45s",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, 5, resolved.MaxRetries)
	assert.Equal(t, 7*time.Second, resolved.BaseDelay)
	assert.Equal(t, 45*time.Second, resolved.MaxDelay)
}

func TestNextRetryDelay_UsesDeterministicBoundedJitter(t *testing.T) {
	config := RetryConfig{MaxRetries: 3, BaseDelay: 5 * time.Second, MaxDelay: 20 * time.Second}
	delay := nextRetryDelay("bucket", 2, config, 0)
	assert.GreaterOrEqual(t, delay, time.Duration(0))
	assert.LessOrEqual(t, delay, 10*time.Second)
	assert.Equal(t, delay, nextRetryDelay("bucket", 2, config, 0))
	assert.NotEqual(t, delay, nextRetryDelay("bucket", 3, config, 0))
}

func TestNextRetryDelay_RespectsRetryAfterHint(t *testing.T) {
	config := RetryConfig{MaxRetries: 3, BaseDelay: 5 * time.Second, MaxDelay: 20 * time.Second}
	delay := nextRetryDelay("bucket", 1, config, 9*time.Second)
	assert.GreaterOrEqual(t, delay, 9*time.Second)
}