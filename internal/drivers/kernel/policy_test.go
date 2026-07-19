package kernel

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/pkg/types"
)

type policySpec struct {
	Encryption struct {
		Algorithm string `json:"algorithm"`
	} `json:"encryption"`
	Tags map[string]string `json:"tags,omitempty"`
}

type policyObserved struct {
	EncryptionAlgo string            `json:"encryptionAlgo"`
	Tags           map[string]string `json:"tags,omitempty"`
}

func policyDescriptor() Descriptor[policySpec, struct{}, policyObserved] {
	return Descriptor[policySpec, struct{}, policyObserved]{
		ServiceName: "PolicyTest",
		HasDrift: func(desired policySpec, observed policyObserved) bool {
			return desired.Encryption.Algorithm != observed.EncryptionAlgo || !assert.ObjectsAreEqual(desired.Tags, observed.Tags)
		},
		FieldDiffs: func(desired policySpec, observed policyObserved) []types.FieldDiff {
			var diffs []types.FieldDiff
			if desired.Encryption.Algorithm != observed.EncryptionAlgo {
				diffs = append(diffs, types.FieldDiff{Path: "spec.encryption.algorithm"})
			}
			for key, desiredValue := range desired.Tags {
				if observed.Tags[key] != desiredValue {
					diffs = append(diffs, types.FieldDiff{Path: "tags." + key})
				}
			}
			return diffs
		},
	}
}

func TestActionableDriftUsesSemanticDiffsAcrossDifferentStateShapes(t *testing.T) {
	desired := policySpec{Tags: map[string]string{"audit": "desired"}}
	desired.Encryption.Algorithm = "aws:kms"
	observed := policyObserved{EncryptionAlgo: "AES256", Tags: map[string]string{"audit": "external"}}

	drift, err := actionableDrift(policyDescriptor(), desired, observed, []string{"encryption.algorithm", "tags.audit"})
	require.NoError(t, err)
	assert.False(t, drift)

	drift, err = actionableDrift(policyDescriptor(), desired, observed, []string{"tags.audit"})
	require.NoError(t, err)
	assert.True(t, drift)
}

func TestActionableDriftRejectsDescriptorInconsistency(t *testing.T) {
	descriptor := policyDescriptor()
	descriptor.FieldDiffs = func(policySpec, policyObserved) []types.FieldDiff { return nil }
	desired := policySpec{}
	desired.Encryption.Algorithm = "aws:kms"

	_, err := actionableDrift(descriptor, desired, policyObserved{EncryptionAlgo: "AES256"}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "inconsistent drift")
}
