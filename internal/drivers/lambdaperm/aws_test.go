package lambdaperm

import (
	"fmt"
	"testing"

	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
)

func TestPermissionErrorClassifiers(t *testing.T) {
	assert.True(t, IsConflict(&smithy.GenericAPIError{Code: "ResourceConflictException"}))
	assert.True(t, IsThrottled(&smithy.GenericAPIError{Code: "TooManyRequestsException"}))
	assert.False(t, IsConflict(nil))
	assert.False(t, IsThrottled(nil))
}

func TestIsNotFound(t *testing.T) {
	assert.True(t, IsNotFound(&smithy.GenericAPIError{Code: "ResourceNotFoundException"}))
	assert.False(t, IsNotFound(&smithy.GenericAPIError{Code: "OtherException"}))
	assert.False(t, IsNotFound(nil))
	assert.False(t, IsNotFound(fmt.Errorf("plain error")))
}

func TestIsConflict_OtherError(t *testing.T) {
	assert.False(t, IsConflict(&smithy.GenericAPIError{Code: "ResourceNotFoundException"}))
	assert.False(t, IsConflict(fmt.Errorf("plain error")))
}

func TestIsPreconditionFailed(t *testing.T) {
	assert.True(t, IsPreconditionFailed(&smithy.GenericAPIError{Code: "PreconditionFailedException"}))
	assert.False(t, IsPreconditionFailed(&smithy.GenericAPIError{Code: "ResourceNotFoundException"}))
	assert.False(t, IsPreconditionFailed(nil))
	assert.False(t, IsPreconditionFailed(fmt.Errorf("plain error")))
}

func TestIsThrottled_OtherError(t *testing.T) {
	assert.False(t, IsThrottled(&smithy.GenericAPIError{Code: "ResourceNotFoundException"}))
	assert.False(t, IsThrottled(fmt.Errorf("plain error")))
}

func TestObservedFromStatement_Basic(t *testing.T) {
	stmt := policyStatement{
		Sid:       "allow-s3",
		Principal: map[string]any{"Service": "s3.amazonaws.com"},
		Action:    "lambda:InvokeFunction",
	}
	obs := observedFromStatement("my-function", stmt)
	assert.Equal(t, "allow-s3", obs.StatementId)
	assert.Equal(t, "my-function", obs.FunctionName)
	assert.Equal(t, "lambda:InvokeFunction", obs.Action)
	assert.Equal(t, "s3.amazonaws.com", obs.Principal)
}

func TestObservedFromStatement_WithCondition(t *testing.T) {
	stmt := policyStatement{
		Sid:       "allow-s3",
		Principal: "s3.amazonaws.com",
		Action:    "lambda:InvokeFunction",
		Condition: map[string]any{
			"ArnLike": map[string]any{
				"AWS:SourceArn": "arn:aws:s3:::my-bucket",
			},
			"StringEquals": map[string]any{
				"AWS:SourceAccount": "123456789012",
			},
		},
	}
	obs := observedFromStatement("my-function", stmt)
	assert.Equal(t, "arn:aws:s3:::my-bucket", obs.SourceArn)
	assert.Equal(t, "123456789012", obs.SourceAccount)
}

func TestPrincipalValue_String(t *testing.T) {
	assert.Equal(t, "s3.amazonaws.com", principalValue("s3.amazonaws.com"))
}

func TestPrincipalValue_MapService(t *testing.T) {
	assert.Equal(t, "s3.amazonaws.com", principalValue(map[string]any{"Service": "s3.amazonaws.com"}))
}

func TestPrincipalValue_MapAWS(t *testing.T) {
	assert.Equal(t, "arn:aws:iam::123:root", principalValue(map[string]any{"AWS": "arn:aws:iam::123:root"}))
}

func TestStringValue_String(t *testing.T) {
	assert.Equal(t, "lambda:InvokeFunction", stringValue("lambda:InvokeFunction"))
}

func TestStringValue_Array(t *testing.T) {
	assert.Equal(t, "lambda:InvokeFunction", stringValue([]any{"lambda:InvokeFunction"}))
}

func TestStringValue_EmptyArray(t *testing.T) {
	assert.Equal(t, "", stringValue([]any{}))
}

func TestPermissionStatementFromPolicy(t *testing.T) {
	policy := `{"Statement":[{"Sid":"allow-s3","Principal":"*","Action":"lambda:InvokeFunction"}]}`
	stmt, err := permissionStatementFromPolicy(policy, "allow-s3")
	assert.NoError(t, err)
	assert.Equal(t, "allow-s3", stmt.Sid)
}

func TestPermissionStatementFromPolicy_NotFound(t *testing.T) {
	policy := `{"Statement":[{"Sid":"other","Principal":"*","Action":"lambda:InvokeFunction"}]}`
	_, err := permissionStatementFromPolicy(policy, "allow-s3")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestPermissionStatementFromPolicy_InvalidJSON(t *testing.T) {
	_, err := permissionStatementFromPolicy("{bad", "allow-s3")
	assert.Error(t, err)
}

func TestExtractConditionValue(t *testing.T) {
	condition := map[string]any{
		"ArnLike": map[string]any{
			"AWS:SourceArn": "arn:aws:s3:::bucket",
		},
	}
	assert.Equal(t, "arn:aws:s3:::bucket", extractConditionValue(condition, "AWS:SourceArn"))
	assert.Equal(t, "", extractConditionValue(condition, "AWS:SourceAccount"))
}

func TestExtractConditionValue_NotMap(t *testing.T) {
	assert.Equal(t, "", extractConditionValue("not-a-map", "key"))
}
