package types

import (
	"errors"
	"fmt"
	"time"
)

// RetryableError marks a resource operation failure as transient so the
// orchestrator can retry the specific resource with backoff instead of failing
// the whole dependency branch immediately.
type RetryableError struct {
	Err error `json:"-"`

	// RetryAfter is an optional driver hint for the minimum delay before retrying.
	// When empty, the orchestrator falls back to its configured backoff policy.
	RetryAfter time.Duration `json:"retryAfter,omitempty"`
}

func (e *RetryableError) Error() string {
	if e == nil || e.Err == nil {
		return "retryable error"
	}
	return fmt.Sprintf("retryable: %s", e.Err.Error())
}

func (e *RetryableError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func NewRetryableError(err error) *RetryableError {
	if err == nil {
		return nil
	}
	return &RetryableError{Err: err}
}

func NewRetryableErrorWithDelay(err error, retryAfter time.Duration) *RetryableError {
	if err == nil {
		return nil
	}
	return &RetryableError{Err: err, RetryAfter: retryAfter}
}

func IsRetryable(err error) (*RetryableError, bool) {
	var retryable *RetryableError
	if errors.As(err, &retryable) {
		return retryable, true
	}
	return nil, false
}