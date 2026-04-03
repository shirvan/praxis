// Package lambdalayer implements the Praxis driver for AWS Lambda Layers.
// Lambda layers are versioned and immutable — updating any content or metadata
// publishes a new version. Permissions can be synced independently.
package lambdalayer

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate Virtual Object name for the Lambda Layer driver.
const ServiceName = "LambdaLayer"

// LambdaLayerSpec defines the desired state for a Lambda Layer.
// All content/metadata fields are effectively immutable per version —
// changes trigger a new PublishLayerVersion.
type LambdaLayerSpec struct {
	Account                 string           `json:"account,omitempty"`
	Region                  string           `json:"region"`
	LayerName               string           `json:"layerName"`                         // Layer name.
	Description             string           `json:"description,omitempty"`             // Per-version description.
	Code                    CodeSpec         `json:"code"`                              // Deployment artifact (S3 or Zip).
	CompatibleRuntimes      []string         `json:"compatibleRuntimes,omitempty"`      // Runtime compatibility hints.
	CompatibleArchitectures []string         `json:"compatibleArchitectures,omitempty"` // Architecture compatibility hints.
	LicenseInfo             string           `json:"licenseInfo,omitempty"`             // SPDX license string.
	Permissions             *PermissionsSpec `json:"permissions,omitempty"`             // Cross-account and public sharing.
	ManagedKey              string           `json:"managedKey,omitempty"`              // praxis:managed-key tag value.
}

// CodeSpec defines the layer deployment artifact. Exactly one of S3 or ZipFile must be set.
type CodeSpec struct {
	S3      *S3CodeSpec `json:"s3,omitempty"`
	ZipFile string      `json:"zipFile,omitempty"`
}

// S3CodeSpec locates a deployment package in S3.
type S3CodeSpec struct {
	Bucket        string `json:"bucket"`
	Key           string `json:"key"`
	ObjectVersion string `json:"objectVersion,omitempty"`
}

// PermissionsSpec controls cross-account sharing and public access.
type PermissionsSpec struct {
	AccountIds []string `json:"accountIds,omitempty"` // AWS account IDs to share with.
	Public     bool     `json:"public,omitempty"`     // If true, all accounts can use this layer.
}

// LambdaLayerOutputs are the user-facing outputs after provisioning.
type LambdaLayerOutputs struct {
	LayerArn        string `json:"layerArn"`
	LayerVersionArn string `json:"layerVersionArn"`
	LayerName       string `json:"layerName"`
	Version         int64  `json:"version"`
	CodeSize        int64  `json:"codeSize"`
	CodeSha256      string `json:"codeSha256,omitempty"`
	CreatedDate     string `json:"createdDate,omitempty"`
}

// ObservedState captures the latest layer version state from AWS.
type ObservedState struct {
	LayerArn                string          `json:"layerArn"`
	LayerVersionArn         string          `json:"layerVersionArn"`
	LayerName               string          `json:"layerName"`
	Version                 int64           `json:"version"`
	Description             string          `json:"description,omitempty"`
	CompatibleRuntimes      []string        `json:"compatibleRuntimes,omitempty"`
	CompatibleArchitectures []string        `json:"compatibleArchitectures,omitempty"`
	LicenseInfo             string          `json:"licenseInfo,omitempty"`
	CodeSize                int64           `json:"codeSize"`
	CodeSha256              string          `json:"codeSha256,omitempty"`
	CreatedDate             string          `json:"createdDate,omitempty"`
	Permissions             PermissionsSpec `json:"permissions"`
}

// LambdaLayerState is the full durable state stored in the Restate Virtual Object.
type LambdaLayerState struct {
	Desired            LambdaLayerSpec      `json:"desired"`
	Observed           ObservedState        `json:"observed"`
	Outputs            LambdaLayerOutputs   `json:"outputs"`
	Status             types.ResourceStatus `json:"status"`
	Mode               types.Mode           `json:"mode"`
	Error              string               `json:"error,omitempty"`
	Generation         int64                `json:"generation"`
	LastReconcile      string               `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
