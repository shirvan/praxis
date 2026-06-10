package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsAuthErrorMessage(t *testing.T) {
	assert.True(t, IsAuthErrorMessage("[AUTH_UNKNOWN_ACCOUNT] unknown account"))
	assert.True(t, IsAuthErrorMessage("error: [AUTH_ACCESS_DENIED] denied"))
	assert.False(t, IsAuthErrorMessage("some normal error"))
	assert.False(t, IsAuthErrorMessage(""))
}

// --- ExitCodeForError tests ---
// The exit-code contract (1 general, 3 not found, 4 validation, 5 conflict,
// 6 auth) is documented in docs/CLI.md and relied on by scripts and agents.

func TestExitCodeForError_Auth(t *testing.T) {
	assert.Equal(t, ExitAuth, ExitCodeForError("[AUTH_EXPIRED] credentials expired"))
}

func TestExitCodeForError_NotFound(t *testing.T) {
	assert.Equal(t, ExitNotFound, ExitCodeForError("NOT_FOUND: deployment missing"))
	assert.Equal(t, ExitNotFound, ExitCodeForError(`deployment "x" not found`))
	assert.Equal(t, ExitNotFound, ExitCodeForError(`workspace "stage" is not configured`))
}

func TestExitCodeForError_Validation(t *testing.T) {
	assert.Equal(t, ExitValidation, ExitCodeForError("VALIDATION_ERROR: bad spec"))
	assert.Equal(t, ExitValidation, ExitCodeForError("TEMPLATE_INVALID: parse error"))
	assert.Equal(t, ExitValidation, ExitCodeForError("event data validation failed for x"))
	assert.Equal(t, ExitValidation, ExitCodeForError(`invalid duration "notaduration"`))
}

func TestExitCodeForError_Conflict(t *testing.T) {
	assert.Equal(t, ExitConflict, ExitCodeForError("CONFLICT: key in use"))
	assert.Equal(t, ExitConflict, ExitCodeForError(`template "webapp" already exists`))
	assert.Equal(t, ExitConflict, ExitCodeForError("delete already in progress"))
}

func TestExitCodeForError_General(t *testing.T) {
	assert.Equal(t, ExitGeneral, ExitCodeForError("connection refused"))
	assert.Equal(t, ExitGeneral, ExitCodeForError("INTERNAL_ERROR: boom"))
}
