// Package lambda implements the Praxis driver for AWS Lambda Functions.
// Supports both Zip and Image (container) package types, with full lifecycle
// management including code updates, configuration convergence, and tag sync.
package lambda

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate Virtual Object name for the Lambda Function driver.
const ServiceName = "LambdaFunction"

// LambdaFunctionSpec defines the desired state for a Lambda function.
// FunctionName, PackageType, and Architectures are immutable after creation.
type LambdaFunctionSpec struct {
	Account          string                `json:"account,omitempty"`          // Praxis account alias.
	Region           string                `json:"region"`                     // AWS region.
	FunctionName     string                `json:"functionName"`               // Immutable: Lambda function name.
	Role             string                `json:"role"`                       // Mutable: IAM execution role ARN.
	PackageType      string                `json:"packageType,omitempty"`      // Immutable: "Zip" or "Image".
	Runtime          string                `json:"runtime,omitempty"`          // Mutable: runtime identifier (Zip only).
	Handler          string                `json:"handler,omitempty"`          // Mutable: handler entry point (Zip only).
	Description      string                `json:"description,omitempty"`      // Mutable.
	Code             CodeSpec              `json:"code"`                       // Code deployment source (exactly one must be set).
	MemorySize       int32                 `json:"memorySize,omitempty"`       // Mutable: MB (default 128).
	Timeout          int32                 `json:"timeout,omitempty"`          // Mutable: seconds (default 3).
	Environment      map[string]string     `json:"environment,omitempty"`      // Mutable: environment variables.
	Layers           []string              `json:"layers,omitempty"`           // Mutable: layer ARNs.
	VPCConfig        *VPCConfigSpec        `json:"vpcConfig,omitempty"`        // Mutable: VPC subnets and security groups.
	DeadLetterConfig *DeadLetterConfigSpec `json:"deadLetterConfig,omitempty"` // Mutable: DLQ target ARN.
	TracingConfig    *TracingConfigSpec    `json:"tracingConfig,omitempty"`    // Mutable: X-Ray tracing mode.
	Architectures    []string              `json:"architectures,omitempty"`    // Immutable: "x86_64" or "arm64".
	EphemeralStorage *EphemeralStorageSpec `json:"ephemeralStorage,omitempty"` // Mutable: /tmp size in MB.
	Tags             map[string]string     `json:"tags,omitempty"`             // Mutable: user-defined tags.
	ManagedKey       string                `json:"managedKey,omitempty"`       // praxis:managed-key tag value for ownership.
}

// CodeSpec defines the deployment artifact. Exactly one source must be set.
type CodeSpec struct {
	S3       *S3CodeSpec `json:"s3,omitempty"`       // S3 deployment package.
	ZipFile  string      `json:"zipFile,omitempty"`  // Base64-encoded zip bytes.
	ImageURI string      `json:"imageUri,omitempty"` // Container image URI.
}

// S3CodeSpec locates a deployment package in S3.
type S3CodeSpec struct {
	Bucket        string `json:"bucket"`
	Key           string `json:"key"`
	ObjectVersion string `json:"objectVersion,omitempty"`
}

// VPCConfigSpec attaches the function to a VPC.
type VPCConfigSpec struct {
	SubnetIds        []string `json:"subnetIds,omitempty"`
	SecurityGroupIds []string `json:"securityGroupIds,omitempty"`
}

// DeadLetterConfigSpec configures a dead-letter queue for async invocations.
type DeadLetterConfigSpec struct {
	TargetArn string `json:"targetArn"`
}

// TracingConfigSpec controls AWS X-Ray tracing mode.
type TracingConfigSpec struct {
	Mode string `json:"mode,omitempty"`
}

// EphemeralStorageSpec sets the /tmp directory size (512–10240 MB).
type EphemeralStorageSpec struct {
	Size int32 `json:"size"`
}

// LambdaFunctionOutputs are the user-facing outputs after provisioning.
type LambdaFunctionOutputs struct {
	FunctionArn      string `json:"functionArn"`
	FunctionName     string `json:"functionName"`
	Version          string `json:"version,omitempty"`
	State            string `json:"state,omitempty"`
	LastModified     string `json:"lastModified,omitempty"`
	LastUpdateStatus string `json:"lastUpdateStatus,omitempty"`
	CodeSha256       string `json:"codeSha256,omitempty"`
}

// ObservedState captures the last-observed AWS state from GetFunction.
type ObservedState struct {
	FunctionArn      string            `json:"functionArn"`
	FunctionName     string            `json:"functionName"`
	Role             string            `json:"role"`
	PackageType      string            `json:"packageType,omitempty"`
	Runtime          string            `json:"runtime,omitempty"`
	Handler          string            `json:"handler,omitempty"`
	Description      string            `json:"description,omitempty"`
	MemorySize       int32             `json:"memorySize,omitempty"`
	Timeout          int32             `json:"timeout,omitempty"`
	Environment      map[string]string `json:"environment,omitempty"`
	Layers           []string          `json:"layers,omitempty"`
	VpcConfig        VPCConfigSpec     `json:"vpcConfig,omitzero"`
	DeadLetterTarget string            `json:"deadLetterTarget,omitempty"`
	TracingMode      string            `json:"tracingMode,omitempty"`
	Architectures    []string          `json:"architectures,omitempty"`
	EphemeralSize    int32             `json:"ephemeralSize,omitempty"`
	Tags             map[string]string `json:"tags,omitempty"`
	ImageURI         string            `json:"imageUri,omitempty"`
	Version          string            `json:"version,omitempty"`
	State            string            `json:"state,omitempty"`
	LastModified     string            `json:"lastModified,omitempty"`
	LastUpdateStatus string            `json:"lastUpdateStatus,omitempty"`
	CodeSha256       string            `json:"codeSha256,omitempty"`
}

// LambdaFunctionState is the full durable state stored in the Restate Virtual Object.
type LambdaFunctionState struct {
	Desired            LambdaFunctionSpec    `json:"desired"`
	Observed           ObservedState         `json:"observed"`
	Outputs            LambdaFunctionOutputs `json:"outputs"`
	Status             types.ResourceStatus  `json:"status"`
	Mode               types.Mode            `json:"mode"`
	Error              string                `json:"error,omitempty"`
	Generation         int64                 `json:"generation"`
	LastReconcile      string                `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                  `json:"reconcileScheduled"`
}
