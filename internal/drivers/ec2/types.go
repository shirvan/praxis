package ec2

import "github.com/praxiscloud/praxis/pkg/types"

// ServiceName is the Restate Virtual Object name for EC2 instances.
const ServiceName = "EC2Instance"

// EC2InstanceSpec is the desired state for an EC2 instance.
type EC2InstanceSpec struct {
	Account            string            `json:"account,omitempty"`
	Region             string            `json:"region"`
	ImageId            string            `json:"imageId"`
	InstanceType       string            `json:"instanceType"`
	KeyName            string            `json:"keyName,omitempty"`
	SubnetId           string            `json:"subnetId"`
	SecurityGroupIds   []string          `json:"securityGroupIds,omitempty"`
	UserData           string            `json:"userData,omitempty"`
	IamInstanceProfile string            `json:"iamInstanceProfile,omitempty"`
	RootVolume         *RootVolumeSpec   `json:"rootVolume,omitempty"`
	Monitoring         bool              `json:"monitoring"`
	Tags               map[string]string `json:"tags,omitempty"`
	ManagedKey         string            `json:"managedKey,omitempty"`
}

// RootVolumeSpec configures the root EBS volume.
type RootVolumeSpec struct {
	SizeGiB    int32  `json:"sizeGiB"`
	VolumeType string `json:"volumeType"`
	Encrypted  bool   `json:"encrypted"`
}

// EC2InstanceOutputs are the user-facing outputs produced by the driver.
type EC2InstanceOutputs struct {
	InstanceId       string `json:"instanceId"`
	PrivateIpAddress string `json:"privateIpAddress"`
	PublicIpAddress  string `json:"publicIpAddress,omitempty"`
	PrivateDnsName   string `json:"privateDnsName"`
	ARN              string `json:"arn"`
	State            string `json:"state"`
	SubnetId         string `json:"subnetId"`
	VpcId            string `json:"vpcId"`
}

// ObservedState captures the current AWS-side configuration of an instance.
type ObservedState struct {
	InstanceId          string            `json:"instanceId"`
	ImageId             string            `json:"imageId"`
	InstanceType        string            `json:"instanceType"`
	KeyName             string            `json:"keyName"`
	SubnetId            string            `json:"subnetId"`
	VpcId               string            `json:"vpcId"`
	SecurityGroupIds    []string          `json:"securityGroupIds"`
	IamInstanceProfile  string            `json:"iamInstanceProfile"`
	Monitoring          bool              `json:"monitoring"`
	State               string            `json:"state"`
	PrivateIpAddress    string            `json:"privateIpAddress"`
	PublicIpAddress     string            `json:"publicIpAddress"`
	PrivateDnsName      string            `json:"privateDnsName"`
	RootVolumeType      string            `json:"rootVolumeType"`
	RootVolumeSizeGiB   int32             `json:"rootVolumeSizeGiB"`
	RootVolumeEncrypted bool              `json:"rootVolumeEncrypted"`
	Tags                map[string]string `json:"tags"`
}

// EC2InstanceState is the single atomic state object stored for each object key.
type EC2InstanceState struct {
	Desired            EC2InstanceSpec      `json:"desired"`
	Observed           ObservedState        `json:"observed"`
	Outputs            EC2InstanceOutputs   `json:"outputs"`
	Status             types.ResourceStatus `json:"status"`
	Mode               types.Mode           `json:"mode"`
	Error              string               `json:"error,omitempty"`
	Generation         int64                `json:"generation"`
	LastReconcile      string               `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
