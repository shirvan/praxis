package vpcpeering

import (
	"testing"

	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
)

func TestIsNotFound(t *testing.T) {
	assert.True(t, IsNotFound(&smithy.GenericAPIError{Code: "InvalidVpcPeeringConnectionID.NotFound"}))
	assert.False(t, IsNotFound(nil))
}

func TestIsVpcNotFound(t *testing.T) {
	assert.True(t, IsVpcNotFound(&smithy.GenericAPIError{Code: "InvalidVpcID.NotFound"}))
}

func TestIsAlreadyExists(t *testing.T) {
	assert.True(t, IsAlreadyExists(&smithy.GenericAPIError{Code: "VpcPeeringConnectionAlreadyExists"}))
}

func TestIsCidrOverlap(t *testing.T) {
	assert.True(t, IsCidrOverlap(&smithy.GenericAPIError{Code: "OverlappingCidrBlock"}))
}

func TestIsPeeringLimitExceeded(t *testing.T) {
	assert.True(t, IsPeeringLimitExceeded(&smithy.GenericAPIError{Code: "VpcPeeringConnectionLimitExceeded"}))
}
