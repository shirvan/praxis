package vpcpeering

import (
	"errors"
	"testing"

	"github.com/aws/smithy-go"
	restate "github.com/restatedev/sdk-go"
	"github.com/stretchr/testify/assert"
)

func TestClassifyVPCPeeringErrors(t *testing.T) {
	tests := []struct {
		name       string
		classifier func(error) error
		err        error
		code       int
	}{
		{name: "create validation", classifier: classifyVPCPeeringCreate, err: &smithy.GenericAPIError{Code: "InvalidParameterValue"}, code: 400},
		{name: "create VPC not found", classifier: classifyVPCPeeringCreate, err: &smithy.GenericAPIError{Code: "InvalidVpcID.NotFound"}, code: 400},
		{name: "create conflict", classifier: classifyVPCPeeringCreate, err: &smithy.GenericAPIError{Code: "VpcPeeringConnectionAlreadyExists"}, code: 409},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.classifier(tt.err)
			assert.True(t, restate.IsTerminalError(got))
			assert.EqualValues(t, tt.code, restate.ErrorCode(got))
		})
	}

	terminal := restate.TerminalError(errors.New("already terminal"), 418)
	assert.Same(t, terminal, classifyVPCPeeringCreate(terminal))
	assert.Same(t, terminal, classifyVPCPeeringMutation(terminal))

	notFound := &smithy.GenericAPIError{Code: "InvalidVpcPeeringConnectionID.NotFound"}
	assert.Same(t, notFound, classifyVPCPeeringMutation(notFound))
}
