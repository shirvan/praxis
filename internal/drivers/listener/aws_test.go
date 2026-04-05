package listener

import (
	"errors"
	"fmt"
	"github.com/shirvan/praxis/internal/drivers"
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
	assert.True(t, IsNotFound(&mockAPIError{code: "ListenerNotFound"}))
}

func TestIsNotFound_Nil(t *testing.T) {
	assert.False(t, IsNotFound(nil))
}

func TestIsNotFound_Unrelated(t *testing.T) {
	assert.False(t, IsNotFound(errors.New("timeout")))
	assert.False(t, IsNotFound(&mockAPIError{code: "ValidationError"}))
}

func TestIsDuplicate_APIError(t *testing.T) {
	assert.True(t, IsDuplicate(&mockAPIError{code: "DuplicateListener"}))
}

func TestIsDuplicate_Nil(t *testing.T) {
	assert.False(t, IsDuplicate(nil))
}

func TestIsTooMany_APIError(t *testing.T) {
	assert.True(t, IsTooMany(&mockAPIError{code: "TooManyListeners"}))
}

func TestIsTooMany_Nil(t *testing.T) {
	assert.False(t, IsTooMany(nil))
}

func TestIsTargetGroupNotFound_APIError(t *testing.T) {
	assert.True(t, IsTargetGroupNotFound(&mockAPIError{code: "TargetGroupNotFound"}))
}

func TestIsTargetGroupNotFound_Nil(t *testing.T) {
	assert.False(t, IsTargetGroupNotFound(nil))
}

func TestIsInvalidConfig_APIError(t *testing.T) {
	assert.True(t, IsInvalidConfig(&mockAPIError{code: "InvalidConfigurationRequest"}))
}

func TestIsInvalidConfig_Nil(t *testing.T) {
	assert.False(t, IsInvalidConfig(nil))
}

func TestIsCertificateNotFound_APIError(t *testing.T) {
	assert.True(t, IsCertificateNotFound(&mockAPIError{code: "CertificateNotFound"}))
}

func TestIsCertificateNotFound_Nil(t *testing.T) {
	assert.False(t, IsCertificateNotFound(nil))
}

func TestFilterPraxisTags(t *testing.T) {
	tags := map[string]string{"env": "dev", "praxis:listener-name": "val", "Name": "my-listener"}
	filtered := drivers.FilterPraxisTags(tags)
	assert.Equal(t, map[string]string{"env": "dev", "Name": "my-listener"}, filtered)
}

func TestFilterPraxisTags_Empty(t *testing.T) {
	filtered := drivers.FilterPraxisTags(nil)
	assert.Equal(t, map[string]string{}, filtered)
}

func TestToAWSActionsForward(t *testing.T) {
	actions := toAWSActions([]ListenerAction{{Type: "forward", TargetGroupArn: "arn:tg"}})
	assert.Len(t, actions, 1)
	assert.Equal(t, "forward", string(actions[0].Type))
	assert.Equal(t, "arn:tg", *actions[0].TargetGroupArn)
}

func TestFromAWSActionsRoundtrip(t *testing.T) {
	original := []ListenerAction{
		{Type: "forward", TargetGroupArn: "arn:tg"},
		{Type: "redirect", RedirectConfig: &RedirectConfig{Protocol: "HTTPS", Host: "example.com", Port: "443", Path: "/new", Query: "", StatusCode: "HTTP_301"}},
		{Type: "fixed-response", FixedResponseConfig: &FixedResponseConfig{StatusCode: "200", ContentType: "text/plain", MessageBody: "OK"}},
	}
	awsActions := toAWSActions(original)
	roundtripped := fromAWSActions(awsActions)
	assert.Equal(t, len(original), len(roundtripped))
	assert.Equal(t, original[0].Type, roundtripped[0].Type)
	assert.Equal(t, original[0].TargetGroupArn, roundtripped[0].TargetGroupArn)
	assert.Equal(t, original[1].RedirectConfig.Protocol, roundtripped[1].RedirectConfig.Protocol)
	assert.Equal(t, original[1].RedirectConfig.StatusCode, roundtripped[1].RedirectConfig.StatusCode)
	assert.Equal(t, original[2].FixedResponseConfig.StatusCode, roundtripped[2].FixedResponseConfig.StatusCode)
}
