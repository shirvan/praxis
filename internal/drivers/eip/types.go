package eip

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate Virtual Object name for Elastic IP allocations.
const ServiceName = "ElasticIP"

// ElasticIPSpec is the desired state for an Elastic IP allocation.
type ElasticIPSpec struct {
	Account            string            `json:"account,omitempty"`
	Region             string            `json:"region"`
	Domain             string            `json:"domain"`
	NetworkBorderGroup string            `json:"networkBorderGroup,omitempty"`
	PublicIpv4Pool     string            `json:"publicIpv4Pool,omitempty"`
	Tags               map[string]string `json:"tags,omitempty"`
	ManagedKey         string            `json:"managedKey,omitempty"`
}

// ElasticIPOutputs are the user-facing outputs produced by the driver.
type ElasticIPOutputs struct {
	AllocationId       string `json:"allocationId"`
	PublicIp           string `json:"publicIp"`
	ARN                string `json:"arn,omitempty"`
	Domain             string `json:"domain"`
	NetworkBorderGroup string `json:"networkBorderGroup"`
}

// ObservedState captures the current AWS-side configuration of an Elastic IP.
type ObservedState struct {
	AllocationId       string            `json:"allocationId"`
	PublicIp           string            `json:"publicIp"`
	Domain             string            `json:"domain"`
	NetworkBorderGroup string            `json:"networkBorderGroup"`
	AssociationId      string            `json:"associationId,omitempty"`
	InstanceId         string            `json:"instanceId,omitempty"`
	Tags               map[string]string `json:"tags"`
}

// ElasticIPState is the single atomic state object stored for each object key.
type ElasticIPState struct {
	Desired            ElasticIPSpec        `json:"desired"`
	Observed           ObservedState        `json:"observed"`
	Outputs            ElasticIPOutputs     `json:"outputs"`
	Status             types.ResourceStatus `json:"status"`
	Mode               types.Mode           `json:"mode"`
	Error              string               `json:"error,omitempty"`
	Generation         int64                `json:"generation"`
	LastReconcile      string               `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
