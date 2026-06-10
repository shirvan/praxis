// Package ratelimit provides a thin wrapper around golang.org/x/time/rate
// (token-bucket algorithm) with structured logging for backpressure visibility.
//
// Praxis drivers share a single Limiter per AWS service to stay within API
// rate limits. Each driver calls limiter.Wait(ctx) before issuing an AWS API
// call; if the bucket is empty, the call blocks until a token is available.
// A warning log at 100ms gives operators early signal that rate limits are
// being hit before requests start failing.
package ratelimit

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Limiter wraps golang.org/x/time/rate.Limiter with a service label for logging.
// The name field is included in warning logs so operators can identify which
// AWS service is experiencing rate pressure (e.g., "ec2", "s3", "iam").
type Limiter struct {
	inner *rate.Limiter // underlying token-bucket limiter
	name  string        // human-readable service label for logs
}

// New creates a Limiter with the given requests-per-second rate and burst size.
// The burst size determines the maximum number of tokens that can accumulate,
// allowing short traffic spikes up to that count before throttling kicks in.
// For example, New("ec2", 10, 20) allows 10 sustained req/s with bursts up to 20.
func New(name string, rps float64, burst int) *Limiter {
	return &Limiter{
		inner: rate.NewLimiter(rate.Limit(rps), burst),
		name:  name,
	}
}

var (
	sharedMu sync.Mutex
	shared   = map[string]*Limiter{}
)

// Shared returns the process-wide Limiter for the given service name, creating
// it on first use. All API clients constructed for the same service share one
// token bucket, so the rate limit holds across concurrent handler invocations.
// The rps/burst of the first caller win; subsequent calls reuse the limiter.
func Shared(name string, rps float64, burst int) *Limiter {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if l, ok := shared[name]; ok {
		return l
	}
	l := New(name, rps, burst)
	shared[name] = l
	return l
}

// Wait blocks until a token is available or ctx is cancelled.
// Logs a warning if it waits longer than 100ms (visible rate pressure indicator).
func (l *Limiter) Wait(ctx context.Context) error {
	start := time.Now()
	if err := l.inner.Wait(ctx); err != nil {
		return err
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		slog.Warn("rate limiter backpressure", "service", l.name, "waited", d.String())
	}
	return nil
}
