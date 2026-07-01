package secret

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/shirvan/praxis/pkg/types"
)

func TestServiceName(t *testing.T) {
	drv := NewSecretsManagerSecretDriver(nil)
	assert.Equal(t, "SecretsManagerSecret", drv.ServiceName())
}

func TestSpecFromObserved(t *testing.T) {
	obs := ObservedState{
		ARN:          "arn:aws:secretsmanager:us-east-1:123456789012:secret:app/db-password-AbCdEf",
		Name:         "app/db-password",
		Description:  "database password",
		SecretString: "s3cr3t",
		VersionID:    "v1",
		Tags:         map[string]string{"env": "dev", "praxis:managed-key": "us-east-1~app/db-password"},
	}
	spec := specFromObserved(obs)
	assert.Equal(t, obs.Name, spec.Name)
	assert.Equal(t, obs.Description, spec.Description)
	assert.Equal(t, obs.SecretString, spec.SecretString)
	assert.Equal(t, map[string]string{"env": "dev"}, spec.Tags, "praxis: tags should be filtered out")
}

func TestSpecFromObserved_DefaultKmsKeyDropped(t *testing.T) {
	spec := specFromObserved(ObservedState{Name: "app/secret", KmsKeyID: "alias/aws/secretsmanager"})
	assert.Empty(t, spec.KmsKeyID, "account default key should map to an empty desired key")

	custom := specFromObserved(ObservedState{Name: "app/secret", KmsKeyID: "alias/my-key"})
	assert.Equal(t, "alias/my-key", custom.KmsKeyID)
}

func TestOutputsFromObserved(t *testing.T) {
	out := outputsFromObserved(ObservedState{
		ARN:          "arn:aws:secretsmanager:us-east-1:123456789012:secret:app/db-password-AbCdEf",
		Name:         "app/db-password",
		SecretString: "super-secret",
		VersionID:    "v9",
	})
	assert.Equal(t, "arn:aws:secretsmanager:us-east-1:123456789012:secret:app/db-password-AbCdEf", out.ARN)
	assert.Equal(t, "app/db-password", out.Name)
	assert.Equal(t, "v9", out.VersionID)
}

func TestDefaultImportMode(t *testing.T) {
	assert.Equal(t, types.ModeObserved, defaultImportMode(""))
	assert.Equal(t, types.ModeManaged, defaultImportMode(types.ModeManaged))
	assert.Equal(t, types.ModeObserved, defaultImportMode(types.ModeObserved))
}

func TestApplyDefaults(t *testing.T) {
	spec := applyDefaults(SecretsManagerSecretSpec{Name: "  app/secret  ", Region: "  us-east-1  "})
	assert.Equal(t, "app/secret", spec.Name)
	assert.Equal(t, "us-east-1", spec.Region)
	assert.NotNil(t, spec.Tags)
}

func TestMetadataDrift(t *testing.T) {
	desired := desiredSpec()
	assert.False(t, metadataDrift(desired, observedInSync()))

	changed := observedInSync()
	changed.Description = "stale"
	assert.True(t, metadataDrift(desired, changed))

	desired.KmsKeyID = "alias/my-key"
	assert.True(t, metadataDrift(desired, observedInSync()))
}

func TestValidateSpec(t *testing.T) {
	base := applyDefaults(SecretsManagerSecretSpec{Region: "us-east-1", Name: "app/secret", SecretString: "s3cr3t"})
	assert.NoError(t, validateSpec(base))

	noRegion := base
	noRegion.Region = ""
	assert.Error(t, validateSpec(noRegion))

	noName := base
	noName.Name = ""
	assert.Error(t, validateSpec(noName))

	noValue := base
	noValue.SecretString = ""
	assert.Error(t, validateSpec(noValue))
}
