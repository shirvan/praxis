package ratelimit

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew_CreatesLimiter(t *testing.T) {
	l := New("test-service", 10, 5)
	require.NotNil(t, l)
	assert.Equal(t, "test-service", l.name)
}

func TestWait_AllowsBurst(t *testing.T) {
	l := New("burst-test", 1, 3) // 1 rps, burst of 3
	ctx := context.Background()

	// Should allow 3 immediate requests (burst size).
	for i := 0; i < 3; i++ {
		err := l.Wait(ctx)
		require.NoError(t, err)
	}
}

func TestWait_ContextCancellation(t *testing.T) {
	l := New("cancel-test", 0.1, 1) // very slow refill

	ctx := context.Background()
	// Consume the burst token.
	require.NoError(t, l.Wait(ctx))

	// Next request should block. Cancel the context to unblock.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	err := l.Wait(ctx)
	assert.Error(t, err, "should fail when context is cancelled")
}
