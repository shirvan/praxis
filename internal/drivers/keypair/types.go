package keypair

import "github.com/shirvan/praxis/pkg/types"

const ServiceName = "KeyPair"

type KeyPairSpec struct {
	Account           string            `json:"account,omitempty"`
	Region            string            `json:"region"`
	KeyName           string            `json:"keyName"`
	KeyType           string            `json:"keyType"`
	PublicKeyMaterial string            `json:"publicKeyMaterial,omitempty"`
	Tags              map[string]string `json:"tags,omitempty"`
}

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

type KeyPairState struct {
	Desired            KeyPairSpec          `json:"desired"`
	Observed           ObservedState        `json:"observed"`
	Outputs            KeyPairOutputs       `json:"outputs"`
	Status             types.ResourceStatus `json:"status"`
	Mode               types.Mode           `json:"mode"`
	Error              string               `json:"error,omitempty"`
	Generation         int64                `json:"generation"`
	LastReconcile      string               `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
