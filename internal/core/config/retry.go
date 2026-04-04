// Package config provides environment-based configuration for all Praxis services.
package config

import (
	"time"

	restate "github.com/restatedev/sdk-go"
)

// DefaultRetryPolicy returns the standard invocation retry policy applied to
// every Praxis service binding. It prevents infinite retry loops by capping
// the total number of attempts and pausing the invocation when the limit is
// reached so operators can inspect and resume it.
//
// Policy summary:
//   - Initial interval:       100ms
//   - Exponentiation factor:  2.0 (100ms → 200ms → 400ms → …)
//   - Max interval:           60s  (cap between retries)
//   - Max attempts:           50   (including the initial call)
//   - On exhaustion:          Pause (preserves state for manual inspection)
func DefaultRetryPolicy() restate.ServiceDefinitionOption {
	return restate.WithInvocationRetryPolicy(
		restate.WithInitialInterval(100*time.Millisecond),
		restate.WithExponentiationFactor(2.0),
		restate.WithMaxInterval(60*time.Second),
		restate.WithMaxAttempts(50),
		restate.PauseOnMaxAttempts(),
	)
}
