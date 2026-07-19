package metricalarm

import (
	"errors"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
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

func TestIsNotFound(t *testing.T) {
	assert.True(t, IsNotFound(&mockAPIError{code: "ResourceNotFound"}))
	assert.False(t, IsNotFound(errors.New("timeout")))
}

func TestIsInvalidParam(t *testing.T) {
	assert.True(t, IsInvalidParam(&mockAPIError{code: "InvalidParameterValue"}))
	assert.True(t, IsInvalidParam(&mockAPIError{code: "InvalidParameterCombination"}))
	assert.False(t, IsInvalidParam(&mockAPIError{code: "LimitExceeded"}))
}

func TestIsLimitExceeded(t *testing.T) {
	assert.True(t, IsLimitExceeded(&mockAPIError{code: "LimitExceeded"}))
}

func TestRequestCollectionsAreDeterministic(t *testing.T) {
	dimensions := toDimensionList(map[string]string{"Zone": "b", "InstanceId": "i-123"})
	assert.Equal(t, []string{"InstanceId", "Zone"}, []string{
		aws.ToString(dimensions[0].Name), aws.ToString(dimensions[1].Name),
	})
	tags := toTagList(map[string]string{"team": "platform", "env": "prod"})
	assert.Equal(t, []string{"env", "team"}, []string{
		aws.ToString(tags[0].Key), aws.ToString(tags[1].Key),
	})
}

func TestSyncTagDiffRepairsManagedKey(t *testing.T) {
	toAdd, toRemove := syncTagDiff(
		map[string]string{"env": "prod"},
		map[string]string{"env": "dev", "stale": "remove", "praxis:managed-key": "wrong"},
		"us-east-1~alarm",
	)
	assert.Equal(t, map[string]string{"env": "prod", "praxis:managed-key": "us-east-1~alarm"}, toAdd)
	assert.Equal(t, []string{"stale"}, toRemove)
}
