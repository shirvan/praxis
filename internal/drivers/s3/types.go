// Package s3 implements the Praxis S3 bucket driver as a Restate Virtual Object.
// It manages the full lifecycle of S3 buckets: provisioning, importing,
// reconcile (drift detection and correction), and status/output queries.
package s3

import "github.com/praxiscloud/praxis/pkg/types"

// S3BucketSpec is the desired state for an S3 bucket.
// This is what Core sends to the Provision handler after hydrating
// output expressions and resolving SSM references.
//
// Fields map 1:1 to the #S3Bucket CUE schema in schemas/aws/s3/s3.cue.
type S3BucketSpec struct {
	// Account is the operator-defined AWS account name used for this bucket.
	Account string `json:"account,omitempty"`

	// BucketName is the globally unique S3 bucket name.
	// Must follow S3 naming rules: 3-63 chars, lowercase, numbers, hyphens, periods.
	BucketName string `json:"bucketName"`

	// Region is the AWS region where the bucket will be created.
	Region string `json:"region"`

	// Versioning enables S3 object versioning (protects against accidental deletion).
	Versioning bool `json:"versioning"`

	// Encryption configures server-side encryption for the bucket.
	Encryption EncryptionSpec `json:"encryption"`

	// ACL is the access control list. One of: "private", "public-read".
	ACL string `json:"acl"`

	// Tags are key-value pairs applied to the bucket for cost allocation,
	// organizational tracking, and policy enforcement.
	Tags map[string]string `json:"tags"`
}

// EncryptionSpec configures server-side encryption for the bucket.
type EncryptionSpec struct {
	// Enabled controls whether SSE is explicitly configured.
	// Since Jan 2023, AWS enables SSE-S3 by default on all new buckets.
	Enabled bool `json:"enabled"`

	// Algorithm is the encryption algorithm: "AES256" (SSE-S3) or "aws:kms" (SSE-KMS).
	Algorithm string `json:"algorithm"`
}

// S3BucketOutputs is produced after provisioning and stored in Restate's K/V store.
// Dependent resources reference these values via output expressions
// (e.g., "${ resources.bucket.outputs.arn }").
type S3BucketOutputs struct {
	// ARN is the Amazon Resource Name for the bucket.
	ARN string `json:"arn"`

	// BucketName is the bucket name (same as spec, returned for convenience).
	BucketName string `json:"bucketName"`

	// Region is the AWS region the bucket resides in.
	Region string `json:"region"`

	// DomainName is the bucket's S3 domain name for HTTP access.
	DomainName string `json:"domainName"`
}

// ObservedState captures the actual configuration of a bucket as
// returned by the AWS Describe calls. Used for drift comparison.
type ObservedState struct {
	BucketName       string            `json:"bucketName"`
	Region           string            `json:"region"`
	VersioningStatus string            `json:"versioningStatus"` // "Enabled" | "Suspended" | ""
	EncryptionAlgo   string            `json:"encryptionAlgo"`
	Tags             map[string]string `json:"tags"`
}

// S3BucketState is the single atomic state object stored under driver.StateKey.
// All fields are written together in one restate.Set() call, ensuring no
// torn state after crash-during-replay.
type S3BucketState struct {
	// Desired is the user's declared configuration.
	Desired S3BucketSpec `json:"desired"`

	// Observed is the actual configuration in AWS, populated during reconcile.
	Observed ObservedState `json:"observed"`

	// Outputs are the values produced after provisioning (ARN, domain, etc.).
	Outputs S3BucketOutputs `json:"outputs"`

	// Status is the current lifecycle status of this resource.
	Status types.ResourceStatus `json:"status"`

	// Mode is Managed (drift corrected) or Observed (drift reported only).
	Mode types.Mode `json:"mode"`

	// Error holds the error message when Status is Error.
	Error string `json:"error,omitempty"`

	// Generation is a monotonically increasing counter incremented on every
	// Provision and Import call. Enables:
	// - Cheap conflict detection ("am I reconciling the spec the user intended?")
	// - Core to correlate "I requested generation N, has the driver reached it?"
	Generation int64 `json:"generation"`

	// LastReconcile is the RFC3339 timestamp of the last completed reconciliation.
	LastReconcile string `json:"lastReconcile,omitempty"`

	// ReconcileScheduled is set to true when a delayed reconcile message has been
	// sent and cleared when Reconcile begins execution. This prevents fan-out:
	// without it, Provision, Import, and each Reconcile would all schedule their
	// own successor, leading to exponentially growing timers. At most one pending
	// reconcile exists per object at any time.
	ReconcileScheduled bool `json:"reconcileScheduled"`
}
