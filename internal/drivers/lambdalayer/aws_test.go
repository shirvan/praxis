package lambdalayer

import (
	"testing"

	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
)

func TestLayerErrorClassifiers(t *testing.T) {
	assert.True(t, IsInvalidParameter(&smithy.GenericAPIError{Code: "InvalidParameterValueException"}))
	assert.True(t, IsPolicyNotFound(&smithy.GenericAPIError{Code: "ResourceNotFoundException", Message: "Layer policy missing"}))
	assert.False(t, IsInvalidParameter(nil))
	assert.False(t, IsPolicyNotFound(nil))
}

func TestLayerContent_InvalidBase64(t *testing.T) {
	_, err := layerContent(CodeSpec{ZipFile: "not-valid-base64!!!"})
	assert.ErrorContains(t, err, "decode zipFile")
}

func TestLayerContent_ValidBase64(t *testing.T) {
	content, err := layerContent(CodeSpec{ZipFile: "aGVsbG8="})
	assert.NoError(t, err)
	assert.Equal(t, []byte("hello"), content.ZipFile)
}
