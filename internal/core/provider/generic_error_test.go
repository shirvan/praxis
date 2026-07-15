package provider

import (
	"errors"
	"testing"

	"github.com/aws/smithy-go"
	restate "github.com/restatedev/sdk-go"
	"github.com/stretchr/testify/assert"
)

func TestClassifyPlanProbeError(t *testing.T) {
	tests := []struct {
		name     string
		code     string
		terminal bool
		status   uint16
	}{
		{name: "expired credentials", code: "ExpiredTokenException", terminal: true, status: 401},
		{name: "access denied", code: "AccessDeniedException", terminal: true, status: 403},
		{name: "validation", code: "ValidationException", terminal: true, status: 400},
		{name: "invalid parameter", code: "InvalidParameterValue", terminal: true, status: 400},
		{name: "malformed query", code: "MalformedQueryString", terminal: true, status: 400},
		{name: "throttling", code: "ThrottlingException"},
		{name: "not found is resource-specific", code: "InvalidVpcID.NotFound"},
		{name: "conflict is resource-specific", code: "ResourceInUseException"},
		{name: "unknown", code: "ServiceUnavailableException"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := &smithy.GenericAPIError{Code: tt.code, Message: "test"}
			got := classifyPlanProbeError(original)
			assert.Equal(t, tt.terminal, restate.IsTerminalError(got))
			if tt.terminal {
				assert.Equal(t, tt.status, uint16(restate.ErrorCode(got)))
			} else {
				assert.True(t, errors.Is(got, original))
			}
		})
	}
}
