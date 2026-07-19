// Package keypair implements the Praxis driver for AWS EC2 Key Pairs.
package keypair

const ServiceName = "KeyPair"

type KeyPairSpec struct {
	Account           string            `json:"account,omitempty"`
	Region            string            `json:"region"`
	KeyName           string            `json:"keyName"`
	KeyType           string            `json:"keyType"`
	PublicKeyMaterial string            `json:"publicKeyMaterial,omitempty"`
	Tags              map[string]string `json:"tags,omitempty"`
	ManagedKey        string            `json:"managedKey,omitempty"`
}

// KeyPairOutputs contains public metadata. PrivateKeyMaterial is populated
// only in the initial create response; the generic state envelope never stores it.
type KeyPairOutputs struct {
	KeyName            string `json:"keyName"`
	KeyPairId          string `json:"keyPairId"`
	KeyFingerprint     string `json:"keyFingerprint"`
	KeyType            string `json:"keyType"`
	PrivateKeyMaterial string `json:"privateKeyMaterial,omitempty"`
}

type ObservedState struct {
	KeyName        string            `json:"keyName"`
	KeyPairId      string            `json:"keyPairId"`
	KeyFingerprint string            `json:"keyFingerprint"`
	KeyType        string            `json:"keyType"`
	Tags           map[string]string `json:"tags"`
}
