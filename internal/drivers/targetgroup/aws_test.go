package targetgroup

import (
	"errors"
	"fmt"
	"testing"

	"github.com/shirvan/praxis/internal/drivers"

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

func TestIsNotFound_APIError(t *testing.T) {
	assert.True(t, IsNotFound(&mockAPIError{code: "TargetGroupNotFound"}))
}

func TestIsNotFound_Nil(t *testing.T) {
	assert.False(t, IsNotFound(nil))
}

func TestIsNotFound_Unrelated(t *testing.T) {
	assert.False(t, IsNotFound(errors.New("network timeout")))
	assert.False(t, IsNotFound(&mockAPIError{code: "ValidationError"}))
}

func TestIsDuplicate_APIError(t *testing.T) {
	assert.True(t, IsDuplicate(&mockAPIError{code: "DuplicateTargetGroupName"}))
}

func TestIsDuplicate_Nil(t *testing.T) {
	assert.False(t, IsDuplicate(nil))
}

func TestIsDuplicate_Unrelated(t *testing.T) {
	assert.False(t, IsDuplicate(errors.New("timeout")))
	assert.False(t, IsDuplicate(&mockAPIError{code: "TargetGroupNotFound"}))
}

func TestIsResourceInUse_APIError(t *testing.T) {
	assert.True(t, IsResourceInUse(&mockAPIError{code: "ResourceInUse"}))
}

func TestIsResourceInUse_Nil(t *testing.T) {
	assert.False(t, IsResourceInUse(nil))
}

func TestIsResourceInUse_Unrelated(t *testing.T) {
	assert.False(t, IsResourceInUse(errors.New("timeout")))
}

func TestIsTooMany_APIError(t *testing.T) {
	assert.True(t, IsTooMany(&mockAPIError{code: "TooManyTargetGroups"}))
}

func TestIsTooMany_Nil(t *testing.T) {
	assert.False(t, IsTooMany(nil))
}

func TestIsTooMany_Unrelated(t *testing.T) {
	assert.False(t, IsTooMany(errors.New("timeout")))
}

func TestIsInvalidConfiguration_APIError(t *testing.T) {
	assert.True(t, IsInvalidConfiguration(&mockAPIError{code: "InvalidTarget"}))
	assert.True(t, IsInvalidConfiguration(&mockAPIError{code: "ValidationError"}))
	assert.True(t, IsInvalidConfiguration(&mockAPIError{code: "InvalidConfigurationRequest"}))
}

func TestIsInvalidConfiguration_Nil(t *testing.T) {
	assert.False(t, IsInvalidConfiguration(nil))
}

func TestIsInvalidConfiguration_Unrelated(t *testing.T) {
	assert.False(t, IsInvalidConfiguration(errors.New("timeout")))
	assert.False(t, IsInvalidConfiguration(&mockAPIError{code: "TargetGroupNotFound"}))
}

func TestDiffTargets(t *testing.T) {
	desired := []Target{
		{ID: "i-1", Port: 80},
		{ID: "i-2", Port: 80},
		{ID: "i-3", Port: 8080},
	}
	observed := []Target{
		{ID: "i-1", Port: 80},
		{ID: "i-4", Port: 80},
	}
	add, remove := diffTargets(desired, observed)
	assert.Len(t, add, 2)
	assert.Len(t, remove, 1)
	assert.Equal(t, "i-4", remove[0].ID)
}

func TestDiffTargets_Empty(t *testing.T) {
	add, remove := diffTargets(nil, nil)
	assert.Empty(t, add)
	assert.Empty(t, remove)
}

func TestEncodeTargets(t *testing.T) {
	targets := []Target{
		{ID: "i-1", Port: 80},
		{ID: "i-2", Port: 0, AvailabilityZone: "us-east-1a"},
	}
	encoded := encodeTargets(targets)
	assert.Len(t, encoded, 2)
	assert.Equal(t, "i-1", *encoded[0].Id)
	assert.Equal(t, int32(80), *encoded[0].Port)
	assert.Nil(t, encoded[0].AvailabilityZone)
	assert.Equal(t, "i-2", *encoded[1].Id)
	assert.Nil(t, encoded[1].Port)
	assert.Equal(t, "us-east-1a", *encoded[1].AvailabilityZone)
}

func TestHealthCheckWithDefaults(t *testing.T) {
	hc := healthCheckWithDefaults(HealthCheck{})
	assert.Equal(t, "HTTP", hc.Protocol)
	assert.Equal(t, "traffic-port", hc.Port)
	assert.Equal(t, "/", hc.Path)
	assert.Equal(t, int32(5), hc.HealthyThreshold)
	assert.Equal(t, int32(2), hc.UnhealthyThreshold)
	assert.Equal(t, int32(30), hc.Interval)
	assert.Equal(t, int32(5), hc.Timeout)
	assert.Equal(t, "200", hc.Matcher)
}

func TestHealthCheckWithDefaults_TCPNoPath(t *testing.T) {
	hc := healthCheckWithDefaults(HealthCheck{Protocol: "TCP"})
	assert.Equal(t, "TCP", hc.Protocol)
	assert.Equal(t, "", hc.Path, "TCP health checks should not have a path")
	assert.Equal(t, "", hc.Matcher, "TCP health checks should not have a matcher")
}

func TestHealthCheckWithDefaults_PreservesExisting(t *testing.T) {
	hc := healthCheckWithDefaults(HealthCheck{
		Protocol:           "HTTPS",
		Path:               "/status",
		Port:               "8443",
		HealthyThreshold:   3,
		UnhealthyThreshold: 5,
		Interval:           10,
		Timeout:            8,
	})
	assert.Equal(t, "HTTPS", hc.Protocol)
	assert.Equal(t, "/status", hc.Path)
	assert.Equal(t, "8443", hc.Port)
	assert.Equal(t, int32(3), hc.HealthyThreshold)
	assert.Equal(t, int32(5), hc.UnhealthyThreshold)
	assert.Equal(t, int32(10), hc.Interval)
	assert.Equal(t, int32(8), hc.Timeout)
	assert.Equal(t, "200", hc.Matcher, "HTTPS health check should get default matcher")
}

func TestFilterPraxisTags(t *testing.T) {
	tags := map[string]string{
		"env":                "dev",
		"team":               "platform",
		"praxis:managed-key": "us-east-1~api-tg",
		"praxis:version":     "1.0",
	}
	filtered := drivers.FilterPraxisTags(tags)
	assert.Equal(t, map[string]string{"env": "dev", "team": "platform"}, filtered)
}

func TestFilterPraxisTags_Nil(t *testing.T) {
	filtered := drivers.FilterPraxisTags(nil)
	assert.NotNil(t, filtered)
	assert.Empty(t, filtered)
}

func TestIsNLBProtocol(t *testing.T) {
	assert.True(t, isNLBProtocol("TCP"))
	assert.True(t, isNLBProtocol("UDP"))
	assert.True(t, isNLBProtocol("TLS"))
	assert.True(t, isNLBProtocol("TCP_UDP"))
	assert.True(t, isNLBProtocol("tcp"))
	assert.False(t, isNLBProtocol("HTTP"))
	assert.False(t, isNLBProtocol("HTTPS"))
	assert.False(t, isNLBProtocol(""))
}

func TestDefaultStickinessType(t *testing.T) {
	assert.Equal(t, "source_ip", defaultStickinessType("TCP"))
	assert.Equal(t, "source_ip", defaultStickinessType("UDP"))
	assert.Equal(t, "source_ip", defaultStickinessType("TLS"))
	assert.Equal(t, "source_ip", defaultStickinessType("TCP_UDP"))
	assert.Equal(t, "lb_cookie", defaultStickinessType("HTTP"))
	assert.Equal(t, "lb_cookie", defaultStickinessType("HTTPS"))
	assert.Equal(t, "lb_cookie", defaultStickinessType(""))
}
