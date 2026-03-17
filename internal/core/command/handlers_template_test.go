package command

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	coretemplate "github.com/praxiscloud/praxis/internal/core/template"
)

func TestValidationErrors_FromTemplateErrors(t *testing.T) {
	err := coretemplate.TemplateErrors{{
		Kind:    coretemplate.ErrCUEValidation,
		Path:    "resources.bucket.spec.region",
		Message: "invalid region",
		Detail:  "set a supported region",
	}}

	converted := validationErrors(err)
	require.Len(t, converted, 1)
	assert.Equal(t, "CUEValidation", converted[0].Kind)
	assert.Equal(t, "resources.bucket.spec.region", converted[0].Path)
	assert.Equal(t, "set a supported region", converted[0].Detail)
}

func TestValidationErrors_PreservesPolicyName(t *testing.T) {
	err := coretemplate.TemplateErrors{{
		Kind:       coretemplate.ErrPolicyViolation,
		Path:       "resources.bucket.spec.encryption.enabled",
		Message:    "conflicting values false and true",
		PolicyName: "require-encryption",
	}}

	converted := validationErrors(err)
	require.Len(t, converted, 1)
	assert.Equal(t, "PolicyViolation", converted[0].Kind)
	assert.Equal(t, "require-encryption", converted[0].Policy)
}

func TestValidationErrors_FallsBackForGenericErrors(t *testing.T) {
	converted := validationErrors(errors.New("boom"))
	require.Len(t, converted, 1)
	assert.Equal(t, "Validation", converted[0].Kind)
	assert.Equal(t, "boom", converted[0].Message)
}
