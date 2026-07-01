// Package secret implements the Praxis driver for AWS Secrets Manager secrets.
//
// This file defines the spec, outputs, observed-state, and reconciliation-state
// types that flow through the driver lifecycle (Provision → Reconcile → Delete).
// The spec is the user's desired configuration; the observed state is read from
// AWS Secrets Manager; the driver state couples both together with status
// tracking. The secret value is sensitive: it lives in the spec and observed
// state so value drift is detectable, but it never leaves the driver as output.
package secret

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate Virtual Object service name used to register the AWS Secrets Manager driver.
const ServiceName = "SecretsManagerSecret"

// SecretsManagerSecretSpec declares the user's desired configuration for a secret.
// Fields are validated before any AWS call and mapped to AWS Secrets Manager API inputs.
type SecretsManagerSecretSpec struct {
	Account      string            `json:"account,omitempty"`
	Region       string            `json:"region"`
	Name         string            `json:"name"`
	Description  string            `json:"description,omitempty"`
	KmsKeyID     string            `json:"kmsKeyId,omitempty"`
	SecretString string            `json:"secretString"`
	Tags         map[string]string `json:"tags,omitempty"`
	ManagedKey   string            `json:"managedKey,omitempty"`
}

// SecretsManagerSecretOutputs holds the values produced after provisioning a
// secret. The secret value is intentionally excluded: deployment outputs flow
// into deployment state and expression hydration, and secret values must not
// leak there. Downstream resources that need the value should use a
// sensitivity-aware resolver.
type SecretsManagerSecretOutputs struct {
	ARN       string `json:"arn"`
	Name      string `json:"name"`
	VersionID string `json:"versionId"`
}

// ObservedState captures the live configuration of a secret as read from AWS
// Secrets Manager. It is compared against the spec during drift detection.
// SecretString holds the current secret value so value drift is detectable; it
// never leaves the driver as an output.
type ObservedState struct {
	ARN          string            `json:"arn"`
	Name         string            `json:"name"`
	Description  string            `json:"description,omitempty"`
	KmsKeyID     string            `json:"kmsKeyId,omitempty"`
	SecretString string            `json:"secretString"`
	VersionID    string            `json:"versionId"`
	Tags         map[string]string `json:"tags,omitempty"`
}

// SecretsManagerSecretState is the single atomic state object persisted under
// drivers.StateKey in the Restate K/V store. It combines desired spec, observed
// state, outputs, lifecycle status, mode (managed/observed), error message,
// generation counter, and reconciliation scheduling metadata.
type SecretsManagerSecretState struct {
	Desired            SecretsManagerSecretSpec    `json:"desired"`
	Observed           ObservedState               `json:"observed"`
	Outputs            SecretsManagerSecretOutputs `json:"outputs"`
	Status             types.ResourceStatus        `json:"status"`
	Mode               types.Mode                  `json:"mode"`
	Error              string                      `json:"error,omitempty"`
	Generation         int64                       `json:"generation"`
	LastReconcile      string                      `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                        `json:"reconcileScheduled"`
}
