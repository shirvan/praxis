package kmskey

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/shirvan/praxis/pkg/types"
)

func TestServiceName(t *testing.T) {
	drv := NewKMSKeyDriver(nil)
	assert.Equal(t, "KMSKey", drv.ServiceName())
}

func TestApplyDefaults_TrimsAndInitializes(t *testing.T) {
	spec := applyDefaults(KMSKeySpec{
		Region:      "  us-east-1  ",
		Name:        "  alias/prod  ",
		Description: "  a key  ",
	})
	assert.Equal(t, "us-east-1", spec.Region)
	assert.Equal(t, "prod", spec.Name, "leading alias/ prefix and whitespace should be stripped")
	assert.Equal(t, "a key", spec.Description)
	assert.Equal(t, defaultKeyUsage, spec.KeyUsage)
	assert.Equal(t, defaultKeySpec, spec.KeySpec)
	assert.Equal(t, defaultDeleteWait, spec.DeletionWindowInDays)
	assert.NotNil(t, spec.Tags)
}

func baseSpec() KMSKeySpec {
	return applyDefaults(KMSKeySpec{Region: "us-east-1", Name: "prod"})
}

func TestValidateSpec(t *testing.T) {
	assert.NoError(t, validateSpec(baseSpec()))

	noRegion := baseSpec()
	noRegion.Region = ""
	assert.Error(t, validateSpec(noRegion))

	noName := baseSpec()
	noName.Name = ""
	assert.Error(t, validateSpec(noName))

	badUsage := baseSpec()
	badUsage.KeyUsage = "BOGUS"
	assert.Error(t, validateSpec(badUsage))

	badWindow := baseSpec()
	badWindow.DeletionWindowInDays = 3
	assert.Error(t, validateSpec(badWindow), "deletion window below 7 is invalid")
}

func TestAliasFor(t *testing.T) {
	assert.Equal(t, "alias/prod", aliasFor("prod"))
	assert.Equal(t, "alias/prod", aliasFor("  prod  "))
	assert.Equal(t, "alias/prod", aliasFor("alias/prod"), "already-prefixed names are not double-prefixed")
}

func TestSpecFromObserved_FiltersPraxisTags(t *testing.T) {
	obs := ObservedState{
		KeyID:             "abcd",
		Description:       "prod key",
		KeyUsage:          "ENCRYPT_DECRYPT",
		KeySpec:           "SYMMETRIC_DEFAULT",
		EnableKeyRotation: true,
		Tags:              map[string]string{"env": "prod", "praxis:managed-key": "us-east-1~prod"},
	}
	spec := specFromObserved("prod", obs)
	assert.Equal(t, "prod", spec.Name)
	assert.Equal(t, "prod key", spec.Description)
	assert.True(t, spec.EnableKeyRotation)
	assert.Equal(t, defaultDeleteWait, spec.DeletionWindowInDays)
	assert.Equal(t, map[string]string{"env": "prod"}, spec.Tags, "praxis: tags should be filtered out")
}

func TestOutputsFromObserved(t *testing.T) {
	out := outputsFromObserved(ObservedState{
		ARN:       "arn:aws:kms:us-east-1:123456789012:key/abcd",
		KeyID:     "abcd",
		AliasName: "alias/prod",
	})
	assert.Equal(t, "arn:aws:kms:us-east-1:123456789012:key/abcd", out.ARN)
	assert.Equal(t, "abcd", out.KeyID)
	assert.Equal(t, "alias/prod", out.AliasName)
}

func TestDefaultImportMode(t *testing.T) {
	assert.Equal(t, types.ModeObserved, defaultImportMode(""))
	assert.Equal(t, types.ModeManaged, defaultImportMode(types.ModeManaged))
	assert.Equal(t, types.ModeObserved, defaultImportMode(types.ModeObserved))
}
