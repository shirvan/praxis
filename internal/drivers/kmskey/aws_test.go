package kmskey

import (
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
)

func TestManagedTags(t *testing.T) {
	out := managedTags(map[string]string{"env": "prod"}, "us-east-1~prod")
	assert.Equal(t, "prod", out["env"])
	assert.Equal(t, "us-east-1~prod", out["praxis:managed-key"])

	noKey := managedTags(map[string]string{"env": "prod"}, "")
	assert.NotContains(t, noKey, "praxis:managed-key")
}

func TestTagList_SortedAndComplete(t *testing.T) {
	list := tagList(map[string]string{"b": "2", "a": "1"})
	assert.Len(t, list, 2)
	// tagList sorts by key so journaled create input is deterministic.
	assert.Equal(t, "a", aws.ToString(list[0].TagKey))
	assert.Equal(t, "1", aws.ToString(list[0].TagValue))
	assert.Equal(t, "b", aws.ToString(list[1].TagKey))
}

func TestTagDiff_AddsRemovesPreservesManagedKey(t *testing.T) {
	desired := map[string]string{"env": "prod", "team": "core"}
	observed := map[string]string{"env": "dev", "old": "1", "praxis:managed-key": "k"}
	toAdd, toRemove := tagDiff(desired, observed, "k")

	assert.Equal(t, "prod", toAdd["env"], "changed value should be re-tagged")
	assert.Equal(t, "core", toAdd["team"], "new tag should be added")
	assert.NotContains(t, toAdd, "praxis:managed-key", "managed key already present, not re-added")
	assert.Equal(t, []string{"old"}, toRemove, "stale tag should be removed; managed key preserved")
}

func TestTagDiff_ManagedKeyNeverDiffed(t *testing.T) {
	// The managed-key marker is synthesized on both the desired and observed
	// sides, so it must never surface as an add or a removal — reconciling it as
	// drift would fight the create-time tagging on every pass.
	toAdd, toRemove := tagDiff(map[string]string{}, map[string]string{}, "us-east-1~prod")
	assert.NotContains(t, toAdd, "praxis:managed-key")
	assert.Empty(t, toAdd)
	assert.Empty(t, toRemove)
}

func TestErrorClassifiers(t *testing.T) {
	notFound := &smithy.GenericAPIError{Code: "NotFoundException"}
	exists := &smithy.GenericAPIError{Code: "AlreadyExistsException"}
	invalidState := &smithy.GenericAPIError{Code: "KMSInvalidStateException"}
	invalidArn := &smithy.GenericAPIError{Code: "InvalidArnException"}
	limit := &smithy.GenericAPIError{Code: "LimitExceededException"}

	assert.True(t, IsNotFound(notFound))
	assert.False(t, IsNotFound(exists))
	assert.True(t, IsConflict(exists))
	assert.True(t, IsConflict(invalidState))
	assert.True(t, IsInvalidParam(invalidArn))
	assert.True(t, IsLimitExceeded(limit))

	// String fallback: Restate wraps errors and loses the typed code, so the
	// classifiers must still match on the wrapped message.
	wrapped := errors.New("operation error KMS: DescribeKey, NotFoundException: Alias is not found")
	assert.True(t, IsNotFound(wrapped), "classifier must survive error wrapping via string fallback")
}
