package ami

import "github.com/shirvan/praxis/pkg/types"

const ServiceName = "AMI"

type AMISpec struct {
	Account           string            `json:"account,omitempty"`
	Region            string            `json:"region"`
	Name              string            `json:"name"`
	Description       string            `json:"description,omitempty"`
	Source            SourceSpec        `json:"source"`
	LaunchPermissions *LaunchPermsSpec  `json:"launchPermissions,omitempty"`
	Deprecation       *DeprecationSpec  `json:"deprecation,omitempty"`
	Tags              map[string]string `json:"tags,omitempty"`
	ManagedKey        string            `json:"managedKey,omitempty"`
}

type SourceSpec struct {
	FromSnapshot *FromSnapshotSpec `json:"fromSnapshot,omitempty"`
	FromAMI      *FromAMISpec      `json:"fromAMI,omitempty"`
}

type FromSnapshotSpec struct {
	SnapshotId         string `json:"snapshotId"`
	Architecture       string `json:"architecture"`
	VirtualizationType string `json:"virtualizationType"`
	RootDeviceName     string `json:"rootDeviceName"`
	VolumeType         string `json:"volumeType"`
	VolumeSize         int32  `json:"volumeSize,omitempty"`
	EnaSupport         *bool  `json:"enaSupport,omitempty"`
}

type FromAMISpec struct {
	SourceImageId string `json:"sourceImageId"`
	SourceRegion  string `json:"sourceRegion,omitempty"`
	Encrypted     bool   `json:"encrypted,omitempty"`
	KmsKeyId      string `json:"kmsKeyId,omitempty"`
}

type LaunchPermsSpec struct {
	AccountIds []string `json:"accountIds,omitempty"`
	Public     bool     `json:"public"`
}

type DeprecationSpec struct {
	DeprecateAt string `json:"deprecateAt"`
}

type AMIOutputs struct {
	ImageId            string `json:"imageId"`
	Name               string `json:"name"`
	State              string `json:"state"`
	Architecture       string `json:"architecture"`
	VirtualizationType string `json:"virtualizationType"`
	RootDeviceName     string `json:"rootDeviceName"`
	OwnerId            string `json:"ownerId"`
	CreationDate       string `json:"creationDate"`
}

type ObservedState struct {
	ImageId            string            `json:"imageId"`
	Name               string            `json:"name"`
	Description        string            `json:"description"`
	State              string            `json:"state"`
	Architecture       string            `json:"architecture"`
	VirtualizationType string            `json:"virtualizationType"`
	RootDeviceName     string            `json:"rootDeviceName"`
	OwnerId            string            `json:"ownerId"`
	CreationDate       string            `json:"creationDate"`
	Tags               map[string]string `json:"tags"`
	LaunchPermPublic   bool              `json:"launchPermPublic"`
	LaunchPermAccounts []string          `json:"launchPermAccounts,omitempty"`
	DeprecationTime    string            `json:"deprecationTime,omitempty"`
}

type AMIState struct {
	Desired            AMISpec              `json:"desired"`
	Observed           ObservedState        `json:"observed"`
	Outputs            AMIOutputs           `json:"outputs"`
	Status             types.ResourceStatus `json:"status"`
	Mode               types.Mode           `json:"mode"`
	Error              string               `json:"error,omitempty"`
	Generation         int64                `json:"generation"`
	LastReconcile      string               `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
