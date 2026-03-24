package drivers

import (
	"fmt"

	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
)

// TerminalAuthError wraps an auth error as a Restate TerminalError with the
// correct HTTP status code. Transient auth errors are returned as plain errors
// so Restate retries them.
func TerminalAuthError(err error, driverName string) error {
	if err == nil {
		return nil
	}

	authErr, ok := authservice.AsAuthError(err)
	if !ok {
		return restate.TerminalError(
			fmt.Errorf("[AUTH] %s: %w", driverName, err), 400)
	}

	if authErr.IsRetryable() {
		return fmt.Errorf("[AUTH] %s: %w", driverName, err)
	}

	return restate.TerminalError(
		fmt.Errorf("[AUTH] %s: %w", driverName, err), restate.Code(authErr.HTTPCode()))
}

// ClassifyAPIError checks whether an AWS API error is auth-related and wraps
// it appropriately. Use after AWS API calls to catch access denied / expired
// token errors that surface at call time.
func ClassifyAPIError(err error, account, driverName string) error {
	if err == nil {
		return nil
	}
	classified := authservice.ClassifyAWSError(err, account)
	if authservice.IsAuthError(classified) {
		return TerminalAuthError(classified, driverName)
	}
	return err
}
