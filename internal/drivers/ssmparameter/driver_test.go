package ssmparameter

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/shirvan/praxis/pkg/types"
)

func TestServiceName(t *testing.T) {
	drv := NewSSMParameterDriver(nil)
	assert.Equal(t, "SSMParameter", drv.ServiceName())
}

func TestSpecFromObserved(t *testing.T) {
	obs := ObservedState{
		ARN:            "arn:aws:ssm:us-east-1:123456789012:parameter/praxis/dev/db-host",
		ParameterName:  "/praxis/dev/db-host",
		Type:           "String",
		Value:          "db.internal",
		Description:    "primary database host",
		Tier:           "Standard",
		AllowedPattern: "^[a-z.]+$",
		DataType:       "text",
		Version:        3,
		Tags:           map[string]string{"env": "dev", "praxis:managed-key": "us-east-1~/praxis/dev/db-host"},
	}

	spec := specFromObserved(obs)
	assert.Equal(t, obs.ParameterName, spec.ParameterName)
	assert.Equal(t, obs.Type, spec.Type)
	assert.Equal(t, obs.Value, spec.Value)
	assert.Equal(t, obs.Description, spec.Description)
	assert.Equal(t, obs.Tier, spec.Tier)
	assert.Equal(t, obs.AllowedPattern, spec.AllowedPattern)
	assert.Equal(t, map[string]string{"env": "dev"}, spec.Tags, "praxis: tags should be filtered out")
}

func TestSpecFromObserved_DefaultKmsKeyDropped(t *testing.T) {
	spec := specFromObserved(ObservedState{
		ParameterName: "/praxis/dev/secret",
		Type:          "SecureString",
		KmsKeyID:      "alias/aws/ssm",
	})
	assert.Empty(t, spec.KmsKeyID, "account default key should map to an empty desired key")

	custom := specFromObserved(ObservedState{
		ParameterName: "/praxis/dev/secret",
		Type:          "SecureString",
		KmsKeyID:      "alias/my-key",
	})
	assert.Equal(t, "alias/my-key", custom.KmsKeyID)
}

func TestOutputsFromObserved(t *testing.T) {
	out := outputsFromObserved(ObservedState{
		ARN:           "arn:aws:ssm:us-east-1:123456789012:parameter/praxis/dev/db-host",
		ParameterName: "/praxis/dev/db-host",
		Type:          "SecureString",
		Value:         "super-secret",
		Version:       2,
	})

	assert.Equal(t, "arn:aws:ssm:us-east-1:123456789012:parameter/praxis/dev/db-host", out.ARN)
	assert.Equal(t, "/praxis/dev/db-host", out.ParameterName)
	assert.Equal(t, "SecureString", out.Type)
	assert.Equal(t, int64(2), out.Version)
	assert.Equal(t, "Standard", out.Tier, "unset tier should normalize to Standard")
	assert.Equal(t, "text", out.DataType, "unset data type should normalize to text")
}

func TestDefaultImportMode(t *testing.T) {
	assert.Equal(t, types.ModeObserved, defaultImportMode(""))
	assert.Equal(t, types.ModeManaged, defaultImportMode(types.ModeManaged))
	assert.Equal(t, types.ModeObserved, defaultImportMode(types.ModeObserved))
}

func TestApplyDefaults(t *testing.T) {
	spec := applyDefaults(SSMParameterSpec{ParameterName: "  /praxis/dev/db-host  ", Region: "  us-east-1  "})
	assert.Equal(t, "/praxis/dev/db-host", spec.ParameterName)
	assert.Equal(t, "us-east-1", spec.Region)
	assert.Equal(t, "String", spec.Type)
	assert.Equal(t, "Standard", spec.Tier)
	assert.Equal(t, "text", spec.DataType)
	assert.NotNil(t, spec.Tags)
}

func TestApplyDefaults_PreservesExplicit(t *testing.T) {
	spec := applyDefaults(SSMParameterSpec{Type: "SecureString", Tier: "Advanced", DataType: "aws:ec2:image"})
	assert.Equal(t, "SecureString", spec.Type)
	assert.Equal(t, "Advanced", spec.Tier)
	assert.Equal(t, "aws:ec2:image", spec.DataType)
}

func TestValidateSpec(t *testing.T) {
	base := applyDefaults(SSMParameterSpec{Region: "us-east-1", ParameterName: "/praxis/dev/db-host", Value: "db.internal"})
	assert.NoError(t, validateSpec(base))

	noRegion := base
	noRegion.Region = ""
	assert.Error(t, validateSpec(noRegion))

	noName := base
	noName.ParameterName = ""
	assert.Error(t, validateSpec(noName))

	noValue := base
	noValue.Value = ""
	assert.Error(t, validateSpec(noValue))

	badType := base
	badType.Type = "Integer"
	assert.Error(t, validateSpec(badType))

	badTier := base
	badTier.Tier = "Premium"
	assert.Error(t, validateSpec(badTier))

	kmsOnString := base
	kmsOnString.KmsKeyID = "alias/my-key"
	assert.Error(t, validateSpec(kmsOnString), "kmsKeyId requires SecureString")

	kmsOnSecure := base
	kmsOnSecure.Type = "SecureString"
	kmsOnSecure.KmsKeyID = "alias/my-key"
	assert.NoError(t, validateSpec(kmsOnSecure))

	badDataType := base
	badDataType.DataType = "binary"
	assert.Error(t, validateSpec(badDataType))
}
