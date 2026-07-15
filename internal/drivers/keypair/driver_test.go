package keypair

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/pkg/types"
)

func TestServiceName(t *testing.T) {
	drv := NewKeyPairDriver(nil)
	assert.Equal(t, "KeyPair", drv.ServiceName())
}

func TestSpecFromObserved_RoundTrip(t *testing.T) {
	obs := ObservedState{
		KeyName:        "web-key",
		KeyPairId:      "key-123",
		KeyFingerprint: "aa:bb:cc",
		KeyType:        "ed25519",
		Tags:           map[string]string{"Name": "web-key", "env": "dev", "praxis:managed-key": "ignore-me"},
	}

	spec := specFromObserved(obs)
	assert.Equal(t, obs.KeyName, spec.KeyName)
	assert.Equal(t, obs.KeyType, spec.KeyType)
	assert.Equal(t, map[string]string{"Name": "web-key", "env": "dev"}, spec.Tags)
	assert.Empty(t, spec.PublicKeyMaterial)
}

func TestOutputsFromObserved(t *testing.T) {
	outputs := outputsFromObserved(ObservedState{
		KeyName:        "web-key",
		KeyPairId:      "key-123",
		KeyFingerprint: "aa:bb:cc",
		KeyType:        "rsa",
	})

	assert.Equal(t, "web-key", outputs.KeyName)
	assert.Equal(t, "key-123", outputs.KeyPairId)
	assert.Equal(t, "aa:bb:cc", outputs.KeyFingerprint)
	assert.Equal(t, "rsa", outputs.KeyType)
}

func TestPrivateKeyCannotBeRepresentedInDurableState(t *testing.T) {
	const privateKey = "-----BEGIN PRIVATE KEY-----secret-----END PRIVATE KEY-----"

	persisted := durableOutputs(KeyPairOutputs{
		KeyName:            "web-key",
		KeyPairId:          "key-123",
		KeyFingerprint:     "aa:bb:cc",
		KeyType:            "ed25519",
		PrivateKeyMaterial: privateKey,
	})

	state := KeyPairState{Outputs: persisted, Status: types.StatusReady}
	assert.Equal(t, types.StatusReady, state.Status)

	encoded, err := json.Marshal(state)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), privateKey)
	assert.NotContains(t, strings.ToLower(string(encoded)), "privatekeymaterial")

	returned := outputsFromDurable(state.Outputs)
	assert.Empty(t, returned.PrivateKeyMaterial)
}
