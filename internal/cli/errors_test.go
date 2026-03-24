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
