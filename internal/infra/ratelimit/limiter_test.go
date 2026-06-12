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
	for range 3 {
		err := l.Wait(ctx)
		require.NoError(t, err)
	}
}

func TestShared_ReturnsSameInstancePerName(t *testing.T) {
	a := Shared("shared-identity-test", 10, 5)
	b := Shared("shared-identity-test", 10, 5)
	assert.Same(t, a, b, "same name must share one token bucket")

	other := Shared("shared-identity-test-other", 10, 5)
	assert.NotSame(t, a, other, "different names must not share a bucket")
}

func TestShared_FirstCallerConfigurationWins(t *testing.T) {
	first := Shared("shared-config-test", 1, 2)
	// A later caller asking for a much higher rate must get the original
	// limiter unchanged — drivers rely on the first registration's limits.
	second := Shared("shared-config-test", 1000, 1000)
	require.Same(t, first, second)

	ctx := context.Background()
	require.NoError(t, second.Wait(ctx)) // burst token 1
	require.NoError(t, second.Wait(ctx)) // burst token 2

	// Bucket is now empty under the FIRST caller's config (1 rps, burst 2).
	// If the second caller's 1000 rps had won, this would not block.
	blocked, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	assert.Error(t, second.Wait(blocked), "first caller's rate must win")
}

func TestShared_ConcurrentFirstUseIsSafe(t *testing.T) {
	const goroutines = 16
	results := make(chan *Limiter, goroutines)
	start := make(chan struct{})
	for range goroutines {
		go func() {
			<-start
			results <- Shared("shared-concurrent-test", 10, 5)
		}()
	}
	close(start)
	first := <-results
	for range goroutines - 1 {
		assert.Same(t, first, <-results)
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
