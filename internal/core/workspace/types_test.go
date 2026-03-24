package workspace

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateName_Valid(t *testing.T) {
	assert.NoError(t, ValidateName("dev"))
	assert.NoError(t, ValidateName("prod-us"))
	assert.NoError(t, ValidateName("test_123"))
	assert.NoError(t, ValidateName("a"))
	assert.NoError(t, ValidateName("0start"))
}

func TestValidateName_Invalid(t *testing.T) {
	assert.Error(t, ValidateName(""))
	assert.Error(t, ValidateName("UPPER"))
	assert.Error(t, ValidateName("-starts-with-dash"))
	assert.Error(t, ValidateName("_starts-with-underscore"))
	assert.Error(t, ValidateName("has space"))
	assert.Error(t, ValidateName("has.dot"))
}

func TestValidateName_MaxLength(t *testing.T) {
	valid := "a" + strings.Repeat("b", 62)
	assert.NoError(t, ValidateName(valid))
	assert.Error(t, ValidateName(valid+"c"))
}
