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

func TestClassifyLookupProbeError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		terminal bool
		status   uint16
	}{
		{name: "ambiguous", err: errors.New("multiple resources matched the filter"), terminal: true, status: 409},
		{name: "access denied", err: &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "denied"}, terminal: true, status: 403},
		{name: "validation", err: &smithy.GenericAPIError{Code: "ValidationException", Message: "invalid"}, terminal: true, status: 400},
		{name: "throttling retries", err: &smithy.GenericAPIError{Code: "ThrottlingException", Message: "slow down"}},
		{name: "transport and unknown errors retry", err: errors.New("connection reset")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyLookupProbeError(tt.err)
			assert.Equal(t, tt.terminal, restate.IsTerminalError(got))
			if tt.terminal {
				assert.Equal(t, tt.status, uint16(restate.ErrorCode(got)))
			} else {
				assert.True(t, errors.Is(got, tt.err))
			}
		})
	}
}

func TestValidateLookupFilter(t *testing.T) {
	assert.Error(t, validateLookupFilter(LookupFilter{}))
	assert.Error(t, validateLookupFilter(LookupFilter{Region: "us-west-2"}))
	assert.NoError(t, validateLookupFilter(LookupFilter{ID: "vpc-123"}))
	assert.NoError(t, validateLookupFilter(LookupFilter{Name: "production"}))
	assert.NoError(t, validateLookupFilter(LookupFilter{Tag: map[string]string{"env": "prod"}}))
}
