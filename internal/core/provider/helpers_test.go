package provider

import (
	"errors"
	"testing"

	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/pkg/types"
)

func TestDecodeRetryableInvocationError(t *testing.T) {
	t.Run("nil error stays nil", func(t *testing.T) {
		if got := decodeRetryableInvocationError(nil); got != nil {
			t.Fatalf("expected nil, got %v", got)
		}
	})

	t.Run("already-retryable error is preserved", func(t *testing.T) {
		in := types.NewRetryableError(errors.New("boom"))
		got := decodeRetryableInvocationError(in)
		if _, ok := types.IsRetryable(got); !ok {
			t.Fatalf("expected retryable, got %v", got)
		}
	})

	// Drivers signal throttling/limits as restate.TerminalError(rawAWSErr, 429)
	// with raw AWS messages that never contain the word "retryable". These must
	// still decode to retryable on the strength of the status code alone.
	retryableCases := []struct {
		name string
		err  error
	}{
		{"425 too early", restate.TerminalError(errors.New("resource not ready"), 425)},
		{"429 limit exceeded", restate.TerminalError(errors.New("SubscriptionLimitExceeded: too many subscriptions"), 429)},
		{"503 unavailable", restate.TerminalError(errors.New("ServiceUnavailable"), 503)},
	}
	for _, tc := range retryableCases {
		t.Run(tc.name, func(t *testing.T) {
			got := decodeRetryableInvocationError(tc.err)
			if _, ok := types.IsRetryable(got); !ok {
				t.Fatalf("expected retryable, got %v", got)
			}
		})
	}

	t.Run("non-retryable code is not retried", func(t *testing.T) {
		err := restate.TerminalError(errors.New("ValidationException: bad input"), 409)
		if got := decodeRetryableInvocationError(err); got != nil {
			t.Fatalf("expected nil for 409, got %v", got)
		}
	})

	t.Run("plain error without a code is not retried", func(t *testing.T) {
		if got := decodeRetryableInvocationError(errors.New("dial tcp: connection refused")); got != nil {
			t.Fatalf("expected nil for uncoded error, got %v", got)
		}
	})
}

func TestPlanCreateMasksSensitiveFields(t *testing.T) {
	type secretSpec struct {
		Name         string `json:"name"`
		SecretString string `json:"secretString"`
	}
	op, fields, err := planCreate(secretSpec{Name: "db", SecretString: "hunter2"}, []string{"spec.secretString"})
	if err != nil {
		t.Fatal(err)
	}
	if op != types.OpCreate {
		t.Fatalf("expected OpCreate, got %v", op)
	}
	for _, d := range fields {
		if v, ok := d.NewValue.(string); ok && v == "hunter2" {
			t.Fatalf("secret value leaked into create diff at %s", d.Path)
		}
	}
}
