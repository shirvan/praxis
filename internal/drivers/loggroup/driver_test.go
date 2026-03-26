package loggroup

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/shirvan/praxis/pkg/types"
)

func TestServiceName(t *testing.T) {
	drv := NewLogGroupDriver(nil)
	assert.Equal(t, "LogGroup", drv.ServiceName())
}

func TestSpecFromObserved(t *testing.T) {
	retention := int32(14)
	obs := ObservedState{
		ARN:             "arn:aws:logs:us-east-1:123456789012:log-group:/aws/lambda/app",
		LogGroupName:    "/aws/lambda/app",
		LogGroupClass:   "STANDARD",
		RetentionInDays: &retention,
		KmsKeyID:        "arn:aws:kms:us-east-1:123456789012:key/abc",
		CreationTime:    1700000000000,
		StoredBytes:     1024,
		Tags:            map[string]string{"env": "dev", "praxis:managed-key": "us-east-1~/aws/lambda/app"},
	}

	spec := specFromObserved(obs)
	assert.Equal(t, obs.LogGroupName, spec.LogGroupName)
	assert.Equal(t, obs.LogGroupClass, spec.LogGroupClass)
	assert.Equal(t, obs.RetentionInDays, spec.RetentionInDays)
	assert.Equal(t, obs.KmsKeyID, spec.KmsKeyID)
	assert.Equal(t, map[string]string{"env": "dev"}, spec.Tags, "praxis: tags should be filtered out")
}

func TestOutputsFromObserved(t *testing.T) {
	retention := int32(30)
	out := outputsFromObserved(ObservedState{
		ARN:             "arn:aws:logs:us-east-1:123456789012:log-group:my-group",
		LogGroupName:    "my-group",
		LogGroupClass:   "STANDARD",
		RetentionInDays: &retention,
		KmsKeyID:        "kms-1",
		CreationTime:    1700000000000,
		StoredBytes:     2048,
	})

	assert.Equal(t, "arn:aws:logs:us-east-1:123456789012:log-group:my-group", out.ARN)
	assert.Equal(t, "my-group", out.LogGroupName)
	assert.Equal(t, "STANDARD", out.LogGroupClass)
	assert.Equal(t, int32(30), out.RetentionInDays)
	assert.Equal(t, "kms-1", out.KmsKeyID)
	assert.Equal(t, int64(1700000000000), out.CreationTime)
	assert.Equal(t, int64(2048), out.StoredBytes)
}

func TestOutputsFromObserved_NilRetention(t *testing.T) {
	out := outputsFromObserved(ObservedState{
		LogGroupName:    "my-group",
		RetentionInDays: nil,
	})
	assert.Equal(t, int32(0), out.RetentionInDays)
}

func TestDefaultImportMode(t *testing.T) {
	assert.Equal(t, types.ModeObserved, defaultImportMode(""))
	assert.Equal(t, types.ModeManaged, defaultImportMode(types.ModeManaged))
	assert.Equal(t, types.ModeObserved, defaultImportMode(types.ModeObserved))
}

func TestApplyDefaults(t *testing.T) {
	spec := applyDefaults(LogGroupSpec{LogGroupName: "  my-group  ", Region: "  us-east-1  "})
	assert.Equal(t, "my-group", spec.LogGroupName)
	assert.Equal(t, "us-east-1", spec.Region)
	assert.Equal(t, "STANDARD", spec.LogGroupClass)
	assert.NotNil(t, spec.Tags)
}

func TestApplyDefaults_PreservesExplicit(t *testing.T) {
	spec := applyDefaults(LogGroupSpec{LogGroupClass: "INFREQUENT_ACCESS"})
	assert.Equal(t, "INFREQUENT_ACCESS", spec.LogGroupClass)
}

func TestValidateSpec(t *testing.T) {
	base := applyDefaults(LogGroupSpec{Region: "us-east-1", LogGroupName: "my-group"})
	assert.NoError(t, validateSpec(base))

	noRegion := base
	noRegion.Region = ""
	assert.Error(t, validateSpec(noRegion))

	noName := base
	noName.LogGroupName = ""
	assert.Error(t, validateSpec(noName))

	badClass := base
	badClass.LogGroupClass = "INVALID"
	assert.Error(t, validateSpec(badClass))
}
