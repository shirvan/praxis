package dynamodbtable

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/shirvan/praxis/pkg/types"
)

func TestServiceName(t *testing.T) {
	drv := NewDynamoDBTableDriver(nil)
	assert.Equal(t, "DynamoDBTable", drv.ServiceName())
}

func baseSpec() DynamoDBTableSpec {
	return DynamoDBTableSpec{
		Region:      "us-east-1",
		Name:        "prod",
		BillingMode: BillingModePayPerRequest,
		HashKey:     "pk",
		HashKeyType: "S",
	}
}

func TestApplyDefaults_TrimsAndInitializes(t *testing.T) {
	spec := applyDefaults(DynamoDBTableSpec{
		Region:  "  us-east-1  ",
		Name:    "  prod  ",
		HashKey: "  pk  ",
	})
	assert.Equal(t, "us-east-1", spec.Region)
	assert.Equal(t, "prod", spec.Name)
	assert.Equal(t, "pk", spec.HashKey)
	assert.Equal(t, "S", spec.HashKeyType, "hashKeyType defaults to S")
	assert.Equal(t, BillingModePayPerRequest, spec.BillingMode, "billingMode defaults to on-demand")
	assert.NotNil(t, spec.Tags)
}

func TestApplyDefaults_RangeKeyTypeClearedWhenNoRangeKey(t *testing.T) {
	spec := applyDefaults(DynamoDBTableSpec{Region: "us-east-1", Name: "t", HashKey: "pk", RangeKeyType: "N"})
	assert.Empty(t, spec.RangeKeyType, "rangeKeyType is cleared when no range key is set")

	withRange := applyDefaults(DynamoDBTableSpec{Region: "us-east-1", Name: "t", HashKey: "pk", RangeKey: "sk"})
	assert.Equal(t, "S", withRange.RangeKeyType, "rangeKeyType defaults to S when a range key is set")
}

func TestValidateSpec(t *testing.T) {
	assert.NoError(t, validateSpec(baseSpec()))

	noRegion := baseSpec()
	noRegion.Region = ""
	assert.Error(t, validateSpec(noRegion))

	noName := baseSpec()
	noName.Name = ""
	assert.Error(t, validateSpec(noName))

	noHash := baseSpec()
	noHash.HashKey = ""
	assert.Error(t, validateSpec(noHash))

	badHashType := baseSpec()
	badHashType.HashKeyType = "X"
	assert.Error(t, validateSpec(badHashType))

	badRangeType := baseSpec()
	badRangeType.RangeKey = "sk"
	badRangeType.RangeKeyType = "Z"
	assert.Error(t, validateSpec(badRangeType))

	badBilling := baseSpec()
	badBilling.BillingMode = "WHATEVER"
	assert.Error(t, validateSpec(badBilling))

	provisioned := baseSpec()
	provisioned.BillingMode = BillingModeProvisioned
	assert.NoError(t, validateSpec(provisioned))
}

func TestSpecFromObserved_FiltersPraxisTags(t *testing.T) {
	obs := ObservedState{
		Name:          "prod",
		BillingMode:   BillingModeProvisioned,
		HashKey:       "pk",
		HashKeyType:   "S",
		RangeKey:      "sk",
		RangeKeyType:  "N",
		ReadCapacity:  5,
		WriteCapacity: 7,
		Tags:          map[string]string{"env": "prod", "praxis:managed-key": "us-east-1~prod"},
	}
	spec := specFromObserved(obs)
	assert.Equal(t, "prod", spec.Name)
	assert.Equal(t, BillingModeProvisioned, spec.BillingMode)
	assert.Equal(t, "pk", spec.HashKey)
	assert.Equal(t, "sk", spec.RangeKey)
	assert.Equal(t, int64(5), spec.ReadCapacity)
	assert.Equal(t, map[string]string{"env": "prod"}, spec.Tags, "praxis: tags should be filtered out")
}

func TestOutputsFromObserved(t *testing.T) {
	out := outputsFromObserved(ObservedState{
		ARN:       "arn:aws:dynamodb:us-east-1:123456789012:table/prod",
		Name:      "prod",
		Status:    "ACTIVE",
		ItemCount: 42,
	})
	assert.Equal(t, "arn:aws:dynamodb:us-east-1:123456789012:table/prod", out.ARN)
	assert.Equal(t, "prod", out.Name)
	assert.Equal(t, "ACTIVE", out.Status)
	assert.Equal(t, int64(42), out.ItemCount)
}

func TestDefaultImportMode(t *testing.T) {
	assert.Equal(t, types.ModeObserved, defaultImportMode(""))
	assert.Equal(t, types.ModeManaged, defaultImportMode(types.ModeManaged))
	assert.Equal(t, types.ModeObserved, defaultImportMode(types.ModeObserved))
}

func TestTagDiff_AddsRemovesPreservesManagedKey(t *testing.T) {
	desired := map[string]string{"env": "prod", "team": "core"}
	observed := map[string]string{"env": "dev", "old": "1", "praxis:managed-key": "k"}
	toAdd, toRemove := tagDiff(desired, observed, "k")

	assert.Equal(t, "prod", toAdd["env"], "changed value should be re-tagged")
	assert.Equal(t, "core", toAdd["team"], "new tag should be added")
	assert.NotContains(t, toAdd, "praxis:managed-key", "managed key already present, not re-added")
	assert.Equal(t, []string{"old"}, toRemove, "stale tag should be removed; managed key preserved")
}

func TestTagDiff_ManagedKeyNeverDiffed(t *testing.T) {
	toAdd, toRemove := tagDiff(map[string]string{}, map[string]string{}, "us-east-1~prod")
	assert.NotContains(t, toAdd, "praxis:managed-key")
	assert.Empty(t, toAdd)
	assert.Empty(t, toRemove)
}
