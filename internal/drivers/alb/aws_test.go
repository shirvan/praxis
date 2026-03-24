package alb

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

func TestIsNotFound_APIError(t *testing.T) {
	assert.True(t, IsNotFound(&mockAPIError{code: "LoadBalancerNotFound"}))
}

func TestIsNotFound_Nil(t *testing.T) {
	assert.False(t, IsNotFound(nil))
}

func TestIsNotFound_Unrelated(t *testing.T) {
	assert.False(t, IsNotFound(errors.New("network timeout")))
	assert.False(t, IsNotFound(&mockAPIError{code: "ValidationError"}))
}

func TestIsDuplicate_APIError(t *testing.T) {
	assert.True(t, IsDuplicate(&mockAPIError{code: "DuplicateLoadBalancerName"}))
}

func TestIsDuplicate_Nil(t *testing.T) {
	assert.False(t, IsDuplicate(nil))
}

func TestIsDuplicate_Unrelated(t *testing.T) {
	assert.False(t, IsDuplicate(errors.New("timeout")))
	assert.False(t, IsDuplicate(&mockAPIError{code: "LoadBalancerNotFound"}))
}

func TestIsResourceInUse_APIError(t *testing.T) {
	assert.True(t, IsResourceInUse(&mockAPIError{code: "ResourceInUse"}))
	assert.True(t, IsResourceInUse(&mockAPIError{code: "OperationNotPermitted"}))
}

func TestIsResourceInUse_Nil(t *testing.T) {
	assert.False(t, IsResourceInUse(nil))
}

func TestIsResourceInUse_Unrelated(t *testing.T) {
	assert.False(t, IsResourceInUse(errors.New("timeout")))
}

func TestIsTooMany_APIError(t *testing.T) {
	assert.True(t, IsTooMany(&mockAPIError{code: "TooManyLoadBalancers"}))
}

func TestIsTooMany_Nil(t *testing.T) {
	assert.False(t, IsTooMany(nil))
}

func TestIsTooMany_Unrelated(t *testing.T) {
	assert.False(t, IsTooMany(errors.New("timeout")))
}

func TestIsInvalidConfig_APIError(t *testing.T) {
	assert.True(t, IsInvalidConfig(&mockAPIError{code: "InvalidConfigurationRequest"}))
}

func TestIsInvalidConfig_Nil(t *testing.T) {
	assert.False(t, IsInvalidConfig(nil))
}

func TestIsInvalidConfig_Unrelated(t *testing.T) {
	assert.False(t, IsInvalidConfig(errors.New("timeout")))
	assert.False(t, IsInvalidConfig(&mockAPIError{code: "LoadBalancerNotFound"}))
}

func TestBuildAttributeMap(t *testing.T) {
	spec := ALBSpec{
		DeletionProtection: true,
		IdleTimeout:        120,
		AccessLogs:         &AccessLogConfig{Enabled: true, Bucket: "my-bucket", Prefix: "logs"},
	}
	attrs := buildAttributeMap(spec)
	assert.Equal(t, "true", attrs["deletion_protection.enabled"])
	assert.Equal(t, "120", attrs["idle_timeout.timeout_seconds"])
	assert.Equal(t, "true", attrs["access_logs.s3.enabled"])
	assert.Equal(t, "my-bucket", attrs["access_logs.s3.bucket"])
	assert.Equal(t, "logs", attrs["access_logs.s3.prefix"])
}

func TestBuildAttributeMap_NoAccessLogs(t *testing.T) {
	spec := ALBSpec{DeletionProtection: false, IdleTimeout: 60}
	attrs := buildAttributeMap(spec)
	assert.Equal(t, "false", attrs["deletion_protection.enabled"])
	assert.Equal(t, "60", attrs["idle_timeout.timeout_seconds"])
	_, hasAccessLogs := attrs["access_logs.s3.enabled"]
	assert.False(t, hasAccessLogs, "access_logs keys should not be set when AccessLogs is nil")
}

func TestNormalizeSubnets(t *testing.T) {
	subnets := normalizeSubnets([]SubnetMapping{{SubnetId: "subnet-c"}, {SubnetId: "subnet-a"}, {SubnetId: "subnet-b"}})
	assert.Equal(t, []string{"subnet-a", "subnet-b", "subnet-c"}, subnets)
}

func TestSubnetsToMappings(t *testing.T) {
	mappings := subnetsToMappings([]string{"subnet-1", "subnet-2"})
	assert.Len(t, mappings, 2)
	assert.Equal(t, "subnet-1", mappings[0].SubnetId)
	assert.Equal(t, "subnet-2", mappings[1].SubnetId)
}

func TestFilterPraxisTags(t *testing.T) {
	tags := map[string]string{"env": "dev", "praxis:managed-key": "val", "Name": "my-alb"}
	filtered := filterPraxisTags(tags)
	assert.Equal(t, map[string]string{"env": "dev", "Name": "my-alb"}, filtered)
}
