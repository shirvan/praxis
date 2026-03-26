package dashboard

import (
	"errors"
	"fmt"
	"testing"

	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
)

type mockAPIError struct {
	code    string
	message string
}

func (e *mockAPIError) Error() string                 { return fmt.Sprintf("%s: %s", e.code, e.message) }
func (e *mockAPIError) ErrorCode() string             { return e.code }
func (e *mockAPIError) ErrorMessage() string          { return e.message }
func (e *mockAPIError) ErrorFault() smithy.ErrorFault { return smithy.FaultUnknown }

func TestIsNotFound(t *testing.T) {
	assert.True(t, IsNotFound(&mockAPIError{code: "ResourceNotFound"}))
	assert.True(t, IsNotFound(&mockAPIError{code: "DashboardNotFoundError"}))
	assert.False(t, IsNotFound(errors.New("timeout")))
}

func TestIsDashboardInvalidInput(t *testing.T) {
	assert.True(t, IsDashboardInvalidInput(&mockAPIError{code: "DashboardInvalidInputError"}))
	assert.False(t, IsDashboardInvalidInput(&mockAPIError{code: "InvalidParameterValue"}))
}
