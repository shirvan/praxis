package s3

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsBucketNotEmpty_MatchesWrappedErrorText(t *testing.T) {
	err := errors.New("[500] operation error S3: DeleteBucket, api error BucketNotEmpty: The bucket you tried to delete is not empty")
	assert.True(t, IsBucketNotEmpty(err))
}

func TestIsBucketNotEmpty_MatchesRestateWrappedPanicText(t *testing.T) {
	err := errors.New("Invocation panicked, returning error to Restate err=\"[500] [500] (500) operation error S3: DeleteBucket, https response error StatusCode: 409, api error BucketNotEmpty: The bucket you tried to delete is not empty. You must delete all versions in the bucket.\\nRelated command: run []\"")
	assert.True(t, IsBucketNotEmpty(err))
}
