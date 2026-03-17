package ratelimit

import (
	"context"
	"log/slog"
	"time"

	"golang.org/x/time/rate"
)

// Limiter wraps golang.org/x/time/rate.Limiter with a service label for logging.
type Limiter struct {
	inner *rate.Limiter
	name  string
}

// New creates a Limiter with the given requests-per-second rate and burst size.
func New(name string, rps float64, burst int) *Limiter {
	return &Limiter{
		inner: rate.NewLimiter(rate.Limit(rps), burst),
		name:  name,
	}
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
