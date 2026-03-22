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
	assert.True(t, IsNotFound(errors.New("api error InvalidRouteTableID.NotFound: missing")))
}

func TestIsRouteNotFound_True(t *testing.T) {
	assert.True(t, IsRouteNotFound(&mockAPIError{code: "InvalidRoute.NotFound"}))
	assert.True(t, IsRouteNotFound(errors.New("api error InvalidRoute.NotFound: missing route")))
}

func TestIsRouteAlreadyExists_True(t *testing.T) {
	assert.True(t, IsRouteAlreadyExists(&mockAPIError{code: "RouteAlreadyExists"}))
	assert.True(t, IsRouteAlreadyExists(errors.New("api error RouteAlreadyExists: duplicate")))
}

func TestIsMainRouteTable_True(t *testing.T) {
	assert.True(t, IsMainRouteTable(errors.New("cannot delete main route table")))
	assert.True(t, IsMainRouteTable(errors.New("The routeTable is the main route table for the VPC")))
}

func TestIsInvalidRoute_True(t *testing.T) {
	assert.True(t, IsInvalidRoute(&mockAPIError{code: "InvalidRoute.InvalidState"}))
	assert.True(t, IsInvalidRoute(errors.New("invalid route target")))
}
