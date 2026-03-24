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
