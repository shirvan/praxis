package types

// ErrorCode is a machine-readable classification for API errors.
//
// Naming convention: DOMAIN_SPECIFIC_CODE in SCREAMING_SNAKE_CASE.
// Domain prefixes group related codes (e.g., AUTH_, TEMPLATE_, GRAPH_).
type ErrorCode string

const (
	ErrCodeValidation       ErrorCode = "VALIDATION_ERROR"
	ErrCodeNotFound         ErrorCode = "NOT_FOUND"
	ErrCodeConflict         ErrorCode = "CONFLICT"
	ErrCodeCapacityExceeded ErrorCode = "CAPACITY_EXCEEDED"
	ErrCodeTemplateInvalid  ErrorCode = "TEMPLATE_INVALID"
	ErrCodeGraphInvalid     ErrorCode = "GRAPH_INVALID"
	ErrCodeProvisionFailed  ErrorCode = "PROVISION_FAILED"
	ErrCodeDeleteFailed     ErrorCode = "DELETE_FAILED"
	ErrCodeInternal         ErrorCode = "INTERNAL_ERROR"
)
