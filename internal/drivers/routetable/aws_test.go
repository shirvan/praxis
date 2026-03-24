package routetable

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

func TestIsNotFound_True(t *testing.T) {
	assert.True(t, IsNotFound(&mockAPIError{code: "InvalidRouteTableID.NotFound"}))
}

func TestIsRouteNotFound_True(t *testing.T) {
	assert.True(t, IsRouteNotFound(&mockAPIError{code: "InvalidRoute.NotFound"}))
}

func TestIsRouteAlreadyExists_True(t *testing.T) {
	assert.True(t, IsRouteAlreadyExists(&mockAPIError{code: "RouteAlreadyExists"}))
}

func TestIsMainRouteTable_True(t *testing.T) {
	assert.True(t, IsMainRouteTable(errors.New("The routeTable is the main route table for the VPC")))
}

func TestIsInvalidRoute_True(t *testing.T) {
	assert.True(t, IsInvalidRoute(&mockAPIError{code: "InvalidRoute.InvalidState"}))
}
