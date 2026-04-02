package sqspolicy

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsNotFound(t *testing.T) {
	assert.False(t, IsNotFound(nil))
	assert.True(t, IsNotFound(errors.New("QueueDoesNotExist: missing")))
	assert.True(t, IsNotFound(errors.New("AWS.SimpleQueueService.NonExistentQueue: missing")))
	assert.False(t, IsNotFound(errors.New("timeout")))
}

func TestIsInvalidInput(t *testing.T) {
	assert.False(t, IsInvalidInput(nil))
	assert.True(t, IsInvalidInput(errors.New("InvalidAttributeValue: bad")))
	assert.True(t, IsInvalidInput(errors.New("InvalidParameterValue: bad")))
	assert.False(t, IsInvalidInput(errors.New("timeout")))
}
