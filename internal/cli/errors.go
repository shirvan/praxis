// errors.go provides error detection, formatting, and exit-code mapping for
// CLI output.
//
// Errors from Praxis Core arrive as plain strings after Restate RPC
// serialisation (typed errors are lost), so detection is string-based: stable
// error-code tokens (pkg/types/errorcode.go) are matched first, then common
// phrasing as a fallback.
package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/shirvan/praxis/pkg/types"
)

// CLI exit codes. Stable contract for scripts and AI agents:
//
//	0 — success
//	1 — general error (network failures, internal errors, unclassified)
//	2 — timeout waiting for completion (emitted by --wait flows)
//	3 — not found (deployment, resource, template, or workspace missing)
//	4 — validation error (bad template, bad input, schema violation)
//	5 — conflict (key in use, concurrent operation, preventDestroy)
//	6 — authentication / credential error
const (
	ExitGeneral    = 1
	ExitTimeout    = 2
	ExitNotFound   = 3
	ExitValidation = 4
	ExitConflict   = 5
	ExitAuth       = 6
)

// ExitCodeForError maps an error message to a stable exit code. Matching is
// string-based because Restate RPC serialisation flattens typed errors.
func ExitCodeForError(msg string) int {
	lower := strings.ToLower(msg)
	switch {
	case IsAuthErrorMessage(msg):
		return ExitAuth
	case strings.Contains(msg, string(types.ErrCodeNotFound)),
		strings.Contains(lower, "not found"),
		strings.Contains(lower, "unknown kind"),
		strings.Contains(lower, "is not configured"):
		return ExitNotFound
	case strings.Contains(msg, string(types.ErrCodeValidation)),
		strings.Contains(msg, string(types.ErrCodeTemplateInvalid)),
		strings.Contains(msg, string(types.ErrCodeGraphInvalid)),
		strings.Contains(lower, "validation failed"),
		strings.Contains(lower, "invalid "):
		return ExitValidation
	case strings.Contains(msg, string(types.ErrCodeConflict)),
		strings.Contains(lower, "already exists"),
		strings.Contains(lower, "already in progress"),
		strings.Contains(lower, "conflict"):
		return ExitConflict
	default:
		return ExitGeneral
	}
}

// HandleError renders err for the active output mode and returns the exit
// code the process should terminate with. In JSON output mode, a machine
// readable envelope is written to stderr instead of styled text.
func HandleError(err error) int {
	msg := err.Error()
	code := ExitCodeForError(msg)

	if currentRootFlags != nil && currentRootFlags.outputFormat() == OutputJSON {
		envelope := map[string]any{"error": msg, "exitCode": code}
		enc := json.NewEncoder(os.Stderr)
		enc.SetIndent("", "  ")
		_ = enc.Encode(envelope)
		return code
	}

	if IsAuthErrorMessage(msg) {
		FormatAuthError(msg)
	} else {
		PrintError(msg)
	}
	return code
}

// IsAuthErrorMessage detects auth errors from their serialized string form.
// After Restate RPC serialization, typed *AuthError becomes a string with
// the [AUTH_CODE] prefix.
func IsAuthErrorMessage(msg string) bool {
	return strings.Contains(msg, "[AUTH_")
}

// FormatAuthError renders an auth error with visual emphasis.
// Extracts the hint line (if present) and prints it separately.
func FormatAuthError(msg string) {
	renderer := defaultRenderer()
	parts := strings.SplitN(msg, "\n  hint: ", 2)
	header := "  ✗ Authentication Error"
	if renderer.styles {
		header = renderer.theme.Error.Render(header)
	}
	fmt.Fprintf(os.Stderr, "\n%s\n", header)
	fmt.Fprintf(os.Stderr, "    %s\n", parts[0])
	if len(parts) == 2 {
		hint := "Hint:"
		if renderer.styles {
			hint = renderer.theme.Warning.Render(hint)
		}
		fmt.Fprintf(os.Stderr, "\n    %s %s\n", hint, parts[1])
	}
}

// PrintError prints a generic error message to stderr, prefixed with a
// styled "✗" indicator.
func PrintError(msg string) {
	renderer := defaultRenderer()
	prefix := "✗"
	if renderer.styles {
		prefix = renderer.theme.Error.Render(prefix)
	}
	fmt.Fprintf(os.Stderr, "%s %s\n", prefix, msg)
}
