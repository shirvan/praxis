// Package secret implements AWS Secrets Manager provider semantics for the
// shared Praxis lifecycle kernel. The secret value remains in desired and
// observed state for drift correction, but never appears in resource outputs.
package secret

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

	// ForceDelete, when true, deletes the secret immediately with no recovery
	// window (ForceDeleteWithoutRecovery). The default (false) uses a 7-day
	// recovery window so an accidental delete — e.g. removing the resource from
	// a template by mistake — can be undone via RestoreSecret. Set true for
	// throwaway/test secrets that will be recreated under the same name.
	ForceDelete bool `json:"forceDelete,omitempty"`
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

	// ScheduledForDeletion is true when the secret exists but is inside its
	// deletion recovery window (DeletedDate set). Such a secret rejects value
	// reads and writes until restored; Provision restores it before converging.
	ScheduledForDeletion bool `json:"scheduledForDeletion,omitempty"`
}
