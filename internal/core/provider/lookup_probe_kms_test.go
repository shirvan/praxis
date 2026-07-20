package provider

import (
	"context"
	"testing"

	restate "github.com/restatedev/sdk-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kmsdriver "github.com/shirvan/praxis/internal/drivers/kmskey"
)

type kmsLookupAPIStub struct {
	kmsdriver.KMSKeyAPI
	observed kmsdriver.ObservedState
	found    bool
	err      error
}

func (s kmsLookupAPIStub) DescribeKey(context.Context, string) (kmsdriver.ObservedState, bool, error) {
	return s.observed, s.found, s.err
}

func TestKMSKeyLookupProbe_ByNameNormalizesAlias(t *testing.T) {
	probe := kmsKeyLookupProbe(kmsLookupAPIStub{found: true, observed: kmsdriver.ObservedState{
		ARN: "arn:aws:kms:us-west-2:123:key/key-123", KeyID: "key-123", Tags: map[string]string{"env": "prod"},
	}})
	outputs, found, err := probe(nil, LookupFilter{Name: "payments", Tag: map[string]string{"env": "prod"}})
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "alias/payments", outputs.AliasName)
}

func TestKMSKeyLookupProbe_ByKeyIDDoesNotMisreportAlias(t *testing.T) {
	probe := kmsKeyLookupProbe(kmsLookupAPIStub{found: true, observed: kmsdriver.ObservedState{
		ARN: "arn:aws:kms:us-west-2:123:key/key-123", KeyID: "key-123", AliasName: "key-123",
	}})
	outputs, found, err := probe(nil, LookupFilter{ID: "key-123"})
	require.NoError(t, err)
	assert.True(t, found)
	assert.Empty(t, outputs.AliasName)
}

func TestKMSKeyLookupProbe_RejectsIDAndNameTogether(t *testing.T) {
	probe := kmsKeyLookupProbe(kmsLookupAPIStub{})
	_, _, err := probe(nil, LookupFilter{ID: "key-123", Name: "payments"})
	require.Error(t, err)
	assert.True(t, restate.IsTerminalError(err))
	assert.Equal(t, uint16(400), uint16(restate.ErrorCode(err)))
}
