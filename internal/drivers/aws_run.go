package drivers

import (
	"fmt"

	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/awserr"
)

// ErrorClassifier maps a provider error to its Restate retry contract. A
// classifier returns a TerminalError for failures that retrying cannot fix and
// leaves transient failures bare so Restate can retry them.
type ErrorClassifier func(error) error

// PassThroughError is the explicit classifier for operations with no
// resource-specific error codes. RunAWS still applies the shared AWS policy.
func PassThroughError(err error) error { return err }

// ClassifyCredentialError preserves the Auth Service's retry and HTTP-status
// contract at driver boundaries. Credential resolution happens before an AWS
// SDK call, so it cannot be classified by RunAWS itself.
func ClassifyCredentialError(err error) error {
	if err == nil || restate.IsTerminalError(err) {
		return err
	}
	authErr, ok := authservice.AsAuthError(err)
	if !ok {
		// Missing driver wiring and other local account-resolution failures are
		// caller/configuration errors, matching the existing alpha contract.
		return restate.TerminalError(err, 400)
	}
	if authErr.IsRetryable() {
		return err
	}
	return restate.TerminalError(err, restate.Code(authErr.HTTPCode()))
}

// ClassifyAWS applies the policy shared by every AWS driver around a
// resource-specific classifier. Resource classifiers remain responsible for
// semantic codes such as not-found and conflict because those meanings depend
// on the operation being performed.
func ClassifyAWS(err error, resourceClassifier ErrorClassifier) error {
	if err == nil || restate.IsTerminalError(err) {
		return err
	}

	// Throttling is always retryable, even if a broad resource-specific
	// classifier would otherwise mistake it for a quota or validation failure.
	if awserr.IsThrottled(err) {
		return err
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	if awserr.IsExpiredToken(err) {
		return restate.TerminalError(err, 401)
	}
	if awserr.IsQuotaExceeded(err) {
		return restate.TerminalError(err, 503)
	}

	classified := resourceClassifier(err)
	if classified == nil || restate.IsTerminalError(classified) {
		return classified
	}

	// These provider request-shape failures are safe to classify uniformly.
	// Not-found and conflicts deliberately stay resource-specific.
	if awserr.IsValidation(classified) {
		return restate.TerminalError(classified, 400)
	}
	return classified
}

// RunAWS journals one provider operation and classifies its error before the
// restate.Run callback returns. Keeping classification inside the callback is
// required: Restate decides whether to retry from the error returned by that
// callback, so classifying it afterwards can turn permanent provider failures
// into infinite retries.
func RunAWS[T any](
	ctx restate.Context,
	operation func(restate.RunContext) (T, error),
	classify ErrorClassifier,
) (T, error) {
	if classify == nil {
		var zero T
		return zero, restate.TerminalError(fmt.Errorf("RunAWS requires an explicit error classifier"), 500)
	}
	return restate.Run(ctx, func(runCtx restate.RunContext) (T, error) {
		result, err := operation(runCtx)
		return result, ClassifyAWS(err, classify)
	})
}
