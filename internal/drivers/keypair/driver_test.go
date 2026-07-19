package keypair

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestServiceName(t *testing.T) {
	assert.Equal(t, ServiceName, NewGenericKeyPairDriver(nil).ServiceName())
}

func TestSpecFromObservedRoundTrip(t *testing.T) {
	observed := ObservedState{
		KeyName: "web-key", KeyPairId: "key-123", KeyFingerprint: "aa:bb:cc", KeyType: "ed25519",
		Tags: map[string]string{"env": "dev", "praxis:managed-key": "ignored"},
	}
	spec := specFromObserved(observed)
	assert.Equal(t, observed.KeyName, spec.KeyName)
	assert.Equal(t, observed.KeyType, spec.KeyType)
	assert.Equal(t, map[string]string{"env": "dev"}, spec.Tags)
	assert.Empty(t, spec.PublicKeyMaterial)
	outputs := outputsFromObserved(observed)
	assert.Equal(t, observed.KeyPairId, outputs.KeyPairId)
	assert.Empty(t, outputs.PrivateKeyMaterial)
}
