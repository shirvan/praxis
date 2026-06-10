package lambda

import (
	"testing"

	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
)

func TestErrorClassifiers(t *testing.T) {
	assert.True(t, IsConflict(&smithy.GenericAPIError{Code: "ResourceConflictException"}))
	assert.True(t, IsAccessDenied(&smithy.GenericAPIError{Code: "AccessDeniedException"}))
	assert.False(t, IsConflict(nil))
	assert.False(t, IsAccessDenied(nil))
}

func TestFunctionCode_InvalidBase64(t *testing.T) {
	_, err := functionCode(CodeSpec{ZipFile: "not-valid-base64!!!"})
	assert.ErrorContains(t, err, "decode zipFile")
}

func TestFunctionCode_ValidBase64(t *testing.T) {
	code, err := functionCode(CodeSpec{ZipFile: "aGVsbG8="})
	assert.NoError(t, err)
	assert.Equal(t, []byte("hello"), code.ZipFile)
}
