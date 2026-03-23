package listenerrule

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
	assert.True(t, IsNotFound(&mockAPIError{code: "RuleNotFound"}))
}

func TestIsNotFound_StringMatch(t *testing.T) {
	assert.True(t, IsNotFound(errors.New("RuleNotFound: rule does not exist")))
}

func TestIsNotFound_Nil(t *testing.T) {
	assert.False(t, IsNotFound(nil))
}

func TestIsNotFound_Unrelated(t *testing.T) {
	assert.False(t, IsNotFound(errors.New("timeout")))
	assert.False(t, IsNotFound(&mockAPIError{code: "ValidationError"}))
}

func TestIsPriorityInUse_APIError(t *testing.T) {
	assert.True(t, IsPriorityInUse(&mockAPIError{code: "PriorityInUse"}))
}

func TestIsPriorityInUse_StringMatch(t *testing.T) {
	assert.True(t, IsPriorityInUse(errors.New("PriorityInUse: priority 10 already taken")))
}

func TestIsPriorityInUse_Nil(t *testing.T) {
	assert.False(t, IsPriorityInUse(nil))
}

func TestIsTooMany_APIError(t *testing.T) {
	assert.True(t, IsTooMany(&mockAPIError{code: "TooManyRules"}))
}

func TestIsTooMany_Nil(t *testing.T) {
	assert.False(t, IsTooMany(nil))
}

func TestIsTooManyConditions_APIError(t *testing.T) {
	assert.True(t, IsTooManyConditions(&mockAPIError{code: "TooManyConditionValues"}))
}

func TestIsTooManyConditions_Nil(t *testing.T) {
	assert.False(t, IsTooManyConditions(nil))
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

func TestFilterPraxisTags(t *testing.T) {
	tags := map[string]string{"env": "dev", "praxis:rule-name": "val", "Name": "my-rule"}
	filtered := filterPraxisTags(tags)
	assert.Equal(t, map[string]string{"env": "dev", "Name": "my-rule"}, filtered)
}

func TestFilterPraxisTags_Empty(t *testing.T) {
	filtered := filterPraxisTags(nil)
	assert.Equal(t, map[string]string{}, filtered)
}

func TestToAWSConditions_PathPattern(t *testing.T) {
	conditions := toAWSConditions([]RuleCondition{
		{Field: "path-pattern", Values: []string{"/api/*", "/v2/*"}},
	})
	assert.Len(t, conditions, 1)
	assert.Equal(t, "path-pattern", *conditions[0].Field)
	assert.Equal(t, []string{"/api/*", "/v2/*"}, conditions[0].PathPatternConfig.Values)
}

func TestToAWSConditions_HostHeader(t *testing.T) {
	conditions := toAWSConditions([]RuleCondition{
		{Field: "host-header", Values: []string{"api.example.com"}},
	})
	assert.Len(t, conditions, 1)
	assert.Equal(t, "host-header", *conditions[0].Field)
	assert.Equal(t, []string{"api.example.com"}, conditions[0].HostHeaderConfig.Values)
}

func TestToAWSConditions_HttpHeader(t *testing.T) {
	conditions := toAWSConditions([]RuleCondition{
		{Field: "http-header", HttpHeaderConfig: &HttpHeaderConfig{Name: "X-Custom", Values: []string{"val1"}}},
	})
	assert.Len(t, conditions, 1)
	assert.Equal(t, "X-Custom", *conditions[0].HttpHeaderConfig.HttpHeaderName)
}

func TestToAWSConditions_QueryString(t *testing.T) {
	conditions := toAWSConditions([]RuleCondition{
		{Field: "query-string", QueryStringConfig: &QueryStringConfig{Values: []QueryStringKV{{Key: "page", Value: "1"}}}},
	})
	assert.Len(t, conditions, 1)
	assert.Len(t, conditions[0].QueryStringConfig.Values, 1)
	assert.Equal(t, "page", *conditions[0].QueryStringConfig.Values[0].Key)
}

func TestToAWSConditions_SourceIP(t *testing.T) {
	conditions := toAWSConditions([]RuleCondition{
		{Field: "source-ip", Values: []string{"10.0.0.0/8"}},
	})
	assert.Len(t, conditions, 1)
	assert.Equal(t, []string{"10.0.0.0/8"}, conditions[0].SourceIpConfig.Values)
}

func TestToAWSConditions_HttpRequestMethod(t *testing.T) {
	conditions := toAWSConditions([]RuleCondition{
		{Field: "http-request-method", Values: []string{"GET", "POST"}},
	})
	assert.Len(t, conditions, 1)
	assert.Equal(t, []string{"GET", "POST"}, conditions[0].HttpRequestMethodConfig.Values)
}

func TestFromAWSConditionsRoundtrip(t *testing.T) {
	original := []RuleCondition{
		{Field: "path-pattern", Values: []string{"/api/*"}},
		{Field: "host-header", Values: []string{"example.com"}},
		{Field: "source-ip", Values: []string{"10.0.0.0/8"}},
	}
	aws := toAWSConditions(original)
	roundtripped := fromAWSConditions(aws)
	assert.Equal(t, len(original), len(roundtripped))
	for i := range original {
		assert.Equal(t, original[i].Field, roundtripped[i].Field)
		assert.Equal(t, original[i].Values, roundtripped[i].Values)
	}
}

func TestToAWSActionsForward(t *testing.T) {
	actions := toAWSActions([]RuleAction{{Type: "forward", TargetGroupArn: "arn:tg"}})
	assert.Len(t, actions, 1)
	assert.Equal(t, "forward", string(actions[0].Type))
	assert.Equal(t, "arn:tg", *actions[0].TargetGroupArn)
}

func TestToAWSActionsForwardConfig(t *testing.T) {
	actions := toAWSActions([]RuleAction{
		{Type: "forward", ForwardConfig: &ForwardConfig{
			TargetGroups: []WeightedTargetGroup{
				{TargetGroupArn: "arn:tg-a", Weight: 80},
				{TargetGroupArn: "arn:tg-b", Weight: 20},
			},
		}},
	})
	assert.Len(t, actions, 1)
	assert.NotNil(t, actions[0].ForwardConfig)
	assert.Len(t, actions[0].ForwardConfig.TargetGroups, 2)
}

func TestFromAWSActionsRoundtrip(t *testing.T) {
	original := []RuleAction{
		{Type: "forward", TargetGroupArn: "arn:tg"},
		{Type: "redirect", RedirectConfig: &RedirectConfig{Protocol: "HTTPS", Host: "example.com", Port: "443", Path: "/new", Query: "", StatusCode: "HTTP_301"}},
		{Type: "fixed-response", FixedResponseConfig: &FixedResponseConfig{StatusCode: "200", ContentType: "text/plain", MessageBody: "OK"}},
	}
	awsActions := toAWSActions(original)
	roundtripped := fromAWSActions(awsActions)
	assert.Equal(t, len(original), len(roundtripped))
	assert.Equal(t, original[0].TargetGroupArn, roundtripped[0].TargetGroupArn)
	assert.Equal(t, original[1].RedirectConfig.Protocol, roundtripped[1].RedirectConfig.Protocol)
	assert.Equal(t, original[2].FixedResponseConfig.StatusCode, roundtripped[2].FixedResponseConfig.StatusCode)
}
