package sqs

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

func TestIsAlreadyExists(t *testing.T) {
	assert.False(t, IsAlreadyExists(nil))
	assert.True(t, IsAlreadyExists(errors.New("QueueNameExists: different attributes")))
	assert.False(t, IsAlreadyExists(errors.New("timeout")))
}

func TestIsConflict(t *testing.T) {
	assert.False(t, IsConflict(nil))
	assert.True(t, IsConflict(errors.New("QueueDeletedRecently: retry later")))
	assert.True(t, IsConflict(errors.New("You must wait 60 seconds after deleting a queue before you can create another with the same name")))
	assert.False(t, IsConflict(errors.New("timeout")))
}

func TestIsInvalidInput(t *testing.T) {
	assert.False(t, IsInvalidInput(nil))
	assert.True(t, IsInvalidInput(errors.New("InvalidAttributeValue: bad")))
	assert.True(t, IsInvalidInput(errors.New("InvalidAttributeName: bad")))
	assert.False(t, IsInvalidInput(errors.New("timeout")))
}

func TestExtractQueueName(t *testing.T) {
	assert.Equal(t, "orders", extractQueueName("https://sqs.us-east-1.amazonaws.com/123/orders"))
	assert.Equal(t, "orders.fifo", extractQueueName("http://localhost:4566/000000000000/orders.fifo"))
}
