// Package keypair implements the Praxis driver for AWS EC2 Key Pairs.
// Key pairs are used for SSH access to EC2 instances and can be either
// AWS-generated (private key returned once) or imported from a public key.
package keypair

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate Virtual Object name for the Key Pair driver.
const ServiceName = "KeyPair"

// KeyPairSpec defines the desired state for an EC2 Key Pair.
// KeyName and KeyType are immutable after creation.
// If PublicKeyMaterial is set, ImportKeyPair is used; otherwise CreateKeyPair
// generates both halves and returns the private key exactly once.
type KeyPairSpec struct {
	Account           string            `json:"account,omitempty"`           // Praxis account alias → resolved to AWS credentials.
	Region            string            `json:"region"`                      // AWS region.
	KeyName           string            `json:"keyName"`                     // Immutable: the name of the key pair in AWS.
	KeyType           string            `json:"keyType"`                     // Immutable: "rsa" or "ed25519".
	PublicKeyMaterial string            `json:"publicKeyMaterial,omitempty"` // Optional: user-provided public key (triggers ImportKeyPair instead of CreateKeyPair).
	Tags              map[string]string `json:"tags,omitempty"`              // Mutable: user-defined tags (praxis: prefix tags are system-managed).
}

// KeyPairOutputs are the user-facing outputs after provisioning.
// PrivateKeyMaterial is only populated on initial CreateKeyPair (not on subsequent reads).
type KeyPairOutputs struct {
	KeyName            string `json:"keyName"`
	KeyPairId          string `json:"keyPairId"`
	KeyFingerprint     string `json:"keyFingerprint"`
	KeyType            string `json:"keyType"`
	PrivateKeyMaterial string `json:"privateKeyMaterial,omitempty"` // Only present on first creation.
}

// ObservedState captures the last-observed AWS state of the key pair.
type ObservedState struct {
	KeyName        string            `json:"keyName"`
	KeyPairId      string            `json:"keyPairId"`
	KeyFingerprint string            `json:"keyFingerprint"`
	KeyType        string            `json:"keyType"`
	Tags           map[string]string `json:"tags"`
}

// KeyPairState is the full durable state stored in the Restate Virtual Object.
// A single drivers.StateKey maps to this struct for each key pair instance.
type KeyPairState struct {
	Desired            KeyPairSpec          `json:"desired"`                 // Last-accepted spec from Provision or Import.
	Observed           ObservedState        `json:"observed"`                // Last-observed AWS state.
	Outputs            KeyPairOutputs       `json:"outputs"`                 // User-facing outputs.
	Status             types.ResourceStatus `json:"status"`                  // Lifecycle status (Provisioning, Ready, Error, Deleted).
	Mode               types.Mode           `json:"mode"`                    // Managed (full control) or Observed (read-only).
	Error              string               `json:"error,omitempty"`         // Last error message, if any.
	Generation         int64                `json:"generation"`              // Monotonically increasing version counter.
	LastReconcile      string               `json:"lastReconcile,omitempty"` // RFC3339 timestamp of last reconcile.
	ReconcileScheduled bool                 `json:"reconcileScheduled"`      // Dedup guard for delayed Reconcile messages.
}
