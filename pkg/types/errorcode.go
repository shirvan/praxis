package types

// ErrorCode is a machine-readable classification for API errors returned
// by the command service and orchestrator. These codes enable the CLI to
// choose the correct exit code and display message without parsing free-text
// error strings.
//
// Naming convention: DOMAIN_SPECIFIC_CODE in SCREAMING_SNAKE_CASE.
// Domain prefixes group related codes (e.g., AUTH_, TEMPLATE_, GRAPH_).
//
// These codes appear in DeploymentDetail.ErrorCode and are threaded through
// Restate TerminalError payloads so they survive across service boundaries.
type ErrorCode string

const (
	// ErrCodeValidation indicates a schema or input validation failure.
	// Returned when CUE evaluation detects a constraint violation, a required
	// field is missing, or a field value is outside the allowed range.
	ErrCodeValidation ErrorCode = "VALIDATION_ERROR"

	// ErrCodeNotFound indicates the requested resource, deployment, or
	// template does not exist. Maps to HTTP 404.
	ErrCodeNotFound ErrorCode = "NOT_FOUND"

	// ErrCodeConflict indicates a naming or ownership collision. For example,
	// a deployment key already in use, or an S3 bucket name already taken.
	ErrCodeConflict ErrorCode = "CONFLICT"

	// ErrCodeCapacityExceeded indicates a system limit has been reached,
	// such as too many concurrent deployments or resources per template.
	ErrCodeCapacityExceeded ErrorCode = "CAPACITY_EXCEEDED"

	// ErrCodeTemplateInvalid indicates the CUE template source could not
	// be parsed or unified against the provider schemas.
	ErrCodeTemplateInvalid ErrorCode = "TEMPLATE_INVALID"

	// ErrCodeGraphInvalid indicates the resource dependency graph contains
	// cycles, missing references, or other structural errors that prevent
	// safe orchestration.
	ErrCodeGraphInvalid ErrorCode = "GRAPH_INVALID"

	// ErrCodeProvisionFailed indicates one or more resources failed during
	// the provisioning phase of a deployment.
	ErrCodeProvisionFailed ErrorCode = "PROVISION_FAILED"

	// ErrCodeDeleteFailed indicates one or more resources failed during
	// the deletion phase of a deployment.
	ErrCodeDeleteFailed ErrorCode = "DELETE_FAILED"

	// ErrCodeInternal indicates an unexpected system error. This is the
	// catch-all for bugs, infrastructure failures, and other conditions
	// that do not have a more specific code.
	ErrCodeInternal ErrorCode = "INTERNAL_ERROR"
)
