package lambdalayer

import "github.com/praxiscloud/praxis/pkg/types"

const ServiceName = "LambdaLayer"

type LambdaLayerSpec struct {
	Account                 string           `json:"account,omitempty"`
	Region                  string           `json:"region"`
	LayerName               string           `json:"layerName"`
	Description             string           `json:"description,omitempty"`
	Code                    CodeSpec         `json:"code"`
	CompatibleRuntimes      []string         `json:"compatibleRuntimes,omitempty"`
	CompatibleArchitectures []string         `json:"compatibleArchitectures,omitempty"`
	LicenseInfo             string           `json:"licenseInfo,omitempty"`
	Permissions             *PermissionsSpec `json:"permissions,omitempty"`
	ManagedKey              string           `json:"managedKey,omitempty"`
}

type CodeSpec struct {
	S3      *S3CodeSpec `json:"s3,omitempty"`
	ZipFile string      `json:"zipFile,omitempty"`
}

type S3CodeSpec struct {
	Bucket        string `json:"bucket"`
	Key           string `json:"key"`
	ObjectVersion string `json:"objectVersion,omitempty"`
}

type PermissionsSpec struct {
	AccountIds []string `json:"accountIds,omitempty"`
	Public     bool     `json:"public,omitempty"`
}

type LambdaLayerOutputs struct {
	LayerArn        string `json:"layerArn"`
	LayerVersionArn string `json:"layerVersionArn"`
	LayerName       string `json:"layerName"`
	Version         int64  `json:"version"`
	CodeSize        int64  `json:"codeSize"`
	CodeSha256      string `json:"codeSha256,omitempty"`
	CreatedDate     string `json:"createdDate,omitempty"`
}

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
