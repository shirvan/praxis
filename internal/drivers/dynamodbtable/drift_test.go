package dynamodbtable

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func inSyncSpecObserved() (DynamoDBTableSpec, ObservedState) {
	spec := DynamoDBTableSpec{
		Region:       "us-east-1",
		Name:         "prod",
		BillingMode:  BillingModePayPerRequest,
		HashKey:      "pk",
		HashKeyType:  "S",
		RangeKey:     "sk",
		RangeKeyType: "N",
		Tags:         map[string]string{"env": "prod"},
	}
	observed := ObservedState{
		ARN:          "arn:aws:dynamodb:us-east-1:123456789012:table/prod",
		Name:         "prod",
		Status:       "ACTIVE",
		BillingMode:  BillingModePayPerRequest,
		HashKey:      "pk",
		HashKeyType:  "S",
		RangeKey:     "sk",
		RangeKeyType: "N",
		Tags:         map[string]string{"env": "prod", "praxis:managed-key": "us-east-1~prod"},
	}
	return spec, observed
}

func inSyncProvisioned() (DynamoDBTableSpec, ObservedState) {
	spec, observed := inSyncSpecObserved()
	spec.BillingMode = BillingModeProvisioned
	spec.ReadCapacity = 5
	spec.WriteCapacity = 5
	observed.BillingMode = BillingModeProvisioned
	observed.ReadCapacity = 5
	observed.WriteCapacity = 5
	return spec, observed
}

func TestHasDrift_InSync(t *testing.T) {
	spec, observed := inSyncSpecObserved()
	assert.False(t, HasDrift(spec, observed), "in-sync spec/observed should not drift")
	assert.Empty(t, ComputeFieldDiffs(spec, observed))
}

func TestHasDrift_InSyncProvisioned(t *testing.T) {
	spec, observed := inSyncProvisioned()
	assert.False(t, HasDrift(spec, observed))
	assert.Empty(t, ComputeFieldDiffs(spec, observed))
}

func TestHasDrift_BillingMode(t *testing.T) {
	spec, observed := inSyncSpecObserved()
	spec.BillingMode = BillingModeProvisioned
	spec.ReadCapacity = 5
	spec.WriteCapacity = 5
	assert.True(t, HasDrift(spec, observed))
	assert.Contains(t, pathsOf(ComputeFieldDiffs(spec, observed)), "spec.billingMode")
}

func TestHasDrift_Throughput(t *testing.T) {
	spec, observed := inSyncProvisioned()
	spec.ReadCapacity = 50
	assert.True(t, HasDrift(spec, observed))
	assert.Contains(t, pathsOf(ComputeFieldDiffs(spec, observed)), "spec.readCapacity")

	spec, observed = inSyncProvisioned()
	spec.WriteCapacity = 99
	assert.True(t, HasDrift(spec, observed))
	assert.Contains(t, pathsOf(ComputeFieldDiffs(spec, observed)), "spec.writeCapacity")
}

func TestHasDrift_ThroughputIgnoredForPayPerRequest(t *testing.T) {
	spec, observed := inSyncSpecObserved()
	// PAY_PER_REQUEST: capacity fields are meaningless and must not drift even
	// if they differ.
	spec.ReadCapacity = 100
	spec.WriteCapacity = 100
	assert.False(t, HasDrift(spec, observed))
}

func TestHasDrift_Tags(t *testing.T) {
	spec, observed := inSyncSpecObserved()
	spec.Tags = map[string]string{"env": "staging"}
	assert.True(t, HasDrift(spec, observed))
	assert.Contains(t, pathsOf(ComputeFieldDiffs(spec, observed)), "tags.env")
}

func TestComputeFieldDiffs_ImmutableKeyFieldsAnnotated(t *testing.T) {
	spec, observed := inSyncSpecObserved()
	spec.HashKey = "other"
	spec.HashKeyType = "B"
	spec.RangeKey = "different"
	paths := pathsOf(ComputeFieldDiffs(spec, observed))
	assert.Contains(t, paths, "spec.hashKey (immutable, requires replacement)")
	assert.Contains(t, paths, "spec.hashKeyType (immutable, requires replacement)")
	assert.Contains(t, paths, "spec.rangeKey (immutable, requires replacement)")
	// Immutable-only divergence is not correctable drift.
	assert.False(t, HasDrift(spec, observed), "immutable fields must not report as correctable drift")
}

func TestHasDrift_ManagedKeyNotDrift(t *testing.T) {
	spec, observed := inSyncSpecObserved()
	// The managed-key marker present only on the observed side must be filtered.
	assert.False(t, HasDrift(spec, observed))
}

func pathsOf(diffs []FieldDiffEntry) []string {
	out := make([]string, 0, len(diffs))
	for _, d := range diffs {
		out = append(out, d.Path)
	}
	return out
}
