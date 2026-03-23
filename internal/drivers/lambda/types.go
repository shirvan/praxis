package lambda

import "github.com/shirvan/praxis/pkg/types"

const ServiceName = "LambdaFunction"

type LambdaFunctionSpec struct {
	Account          string                `json:"account,omitempty"`
	Region           string                `json:"region"`
	FunctionName     string                `json:"functionName"`
	Role             string                `json:"role"`
	PackageType      string                `json:"packageType,omitempty"`
	Runtime          string                `json:"runtime,omitempty"`
	Handler          string                `json:"handler,omitempty"`
	Description      string                `json:"description,omitempty"`
	Code             CodeSpec              `json:"code"`
	MemorySize       int32                 `json:"memorySize,omitempty"`
	Timeout          int32                 `json:"timeout,omitempty"`
	Environment      map[string]string     `json:"environment,omitempty"`
	Layers           []string              `json:"layers,omitempty"`
	VPCConfig        *VPCConfigSpec        `json:"vpcConfig,omitempty"`
	DeadLetterConfig *DeadLetterConfigSpec `json:"deadLetterConfig,omitempty"`
	TracingConfig    *TracingConfigSpec    `json:"tracingConfig,omitempty"`
	Architectures    []string              `json:"architectures,omitempty"`
	EphemeralStorage *EphemeralStorageSpec `json:"ephemeralStorage,omitempty"`
	Tags             map[string]string     `json:"tags,omitempty"`
	ManagedKey       string                `json:"managedKey,omitempty"`
}

type CodeSpec struct {
	S3       *S3CodeSpec `json:"s3,omitempty"`
	ZipFile  string      `json:"zipFile,omitempty"`
	ImageURI string      `json:"imageUri,omitempty"`
}

type S3CodeSpec struct {
	Bucket        string `json:"bucket"`
	Key           string `json:"key"`
	ObjectVersion string `json:"objectVersion,omitempty"`
}

type VPCConfigSpec struct {
	SubnetIds        []string `json:"subnetIds,omitempty"`
	SecurityGroupIds []string `json:"securityGroupIds,omitempty"`
}

type DeadLetterConfigSpec struct {
	TargetArn string `json:"targetArn"`
}

type TracingConfigSpec struct {
	Mode string `json:"mode,omitempty"`
}

type EphemeralStorageSpec struct {
	Size int32 `json:"size"`
}

type LambdaFunctionOutputs struct {
	FunctionArn      string `json:"functionArn"`
	FunctionName     string `json:"functionName"`
	Version          string `json:"version,omitempty"`
	State            string `json:"state,omitempty"`
	LastModified     string `json:"lastModified,omitempty"`
	LastUpdateStatus string `json:"lastUpdateStatus,omitempty"`
	CodeSha256       string `json:"codeSha256,omitempty"`
}

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
	VpcConfig        VPCConfigSpec     `json:"vpcConfig,omitempty"`
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
