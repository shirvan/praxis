package secret

import (
	"errors"
	"testing"

	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
)

func TestManagedTags(t *testing.T) {
	out := managedTags(map[string]string{"env": "prod"}, "us-east-1~app/secret")
	assert.Equal(t, "prod", out["env"])
	assert.Equal(t, "us-east-1~app/secret", out["praxis:managed-key"])

	noKey := managedTags(map[string]string{"env": "prod"}, "")
	assert.NotContains(t, noKey, "praxis:managed-key")
}

func TestTagList_Sorted(t *testing.T) {
	list := tagList(map[string]string{"b": "2", "a": "1"})
	assert.Len(t, list, 2)
	assert.Equal(t, "a", *list[0].Key)
	assert.Equal(t, "b", *list[1].Key)
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
	toAdd, toRemove := tagDiff(map[string]string{}, map[string]string{}, "us-east-1~app/secret")
	assert.NotContains(t, toAdd, "praxis:managed-key")
	assert.Empty(t, toAdd)
	assert.Empty(t, toRemove)
}

func TestErrorClassifiers(t *testing.T) {
	notFound := &smithy.GenericAPIError{Code: "ResourceNotFoundException"}
	exists := &smithy.GenericAPIError{Code: "ResourceExistsException"}
	invalid := &smithy.GenericAPIError{Code: "InvalidParameterException"}
	limit := &smithy.GenericAPIError{Code: "LimitExceededException"}

	assert.True(t, IsNotFound(notFound))
	assert.False(t, IsNotFound(exists))
	assert.True(t, IsAlreadyExists(exists))
	assert.True(t, IsInvalidParam(invalid))
	assert.True(t, IsLimitExceeded(limit))

	// String fallback: Restate wraps errors and loses the typed code, so the
	// classifiers must still match on the wrapped message.
	wrapped := errors.New("operation error Secrets Manager: DescribeSecret, ResourceNotFoundException: not found")
	assert.True(t, IsNotFound(wrapped), "classifier must survive error wrapping via string fallback")
}
