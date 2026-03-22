package vpcpeering

import (
	"errors"
	"testing"

	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
)

func TestIsNotFound(t *testing.T) {
	assert.True(t, IsNotFound(&smithy.GenericAPIError{Code: "InvalidVpcPeeringConnectionID.NotFound"}))
	assert.True(t, IsNotFound(errors.New("InvalidVpcPeeringConnectionId.NotFound: missing")))
	assert.False(t, IsNotFound(nil))
}

func TestIsVpcNotFound(t *testing.T) {
	assert.True(t, IsVpcNotFound(&smithy.GenericAPIError{Code: "InvalidVpcID.NotFound"}))
	assert.True(t, IsVpcNotFound(errors.New("InvalidVpcID.Malformed: bad")))
}

func TestIsAlreadyExists(t *testing.T) {
	assert.True(t, IsAlreadyExists(&smithy.GenericAPIError{Code: "VpcPeeringConnectionAlreadyExists"}))
	assert.True(t, IsAlreadyExists(errors.New("duplicate peering request already exists")))
}

func TestIsCidrOverlap(t *testing.T) {
	assert.True(t, IsCidrOverlap(&smithy.GenericAPIError{Code: "OverlappingCidrBlock"}))
	assert.True(t, IsCidrOverlap(errors.New("The CIDR blocks overlap")))
}

func TestIsPeeringLimitExceeded(t *testing.T) {
	assert.True(t, IsPeeringLimitExceeded(&smithy.GenericAPIError{Code: "VpcPeeringConnectionLimitExceeded"}))
	assert.True(t, IsPeeringLimitExceeded(errors.New("VpcLimitExceeded: quota reached")))
}