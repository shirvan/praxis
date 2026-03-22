package vpcpeering

import "github.com/praxiscloud/praxis/pkg/types"

const ServiceName = "VPCPeeringConnection"

type VPCPeeringSpec struct {
	Account          string            `json:"account,omitempty"`
	Region           string            `json:"region"`
	RequesterVpcId   string            `json:"requesterVpcId"`
	AccepterVpcId    string            `json:"accepterVpcId"`
	PeerOwnerId      string            `json:"peerOwnerId,omitempty"`
	PeerRegion       string            `json:"peerRegion,omitempty"`
	AutoAccept       bool              `json:"autoAccept"`
	RequesterOptions *PeeringOptions   `json:"requesterOptions,omitempty"`
	AccepterOptions  *PeeringOptions   `json:"accepterOptions,omitempty"`
	Tags             map[string]string `json:"tags,omitempty"`
	ManagedKey       string            `json:"managedKey,omitempty"`
}

type PeeringOptions struct {
	AllowDnsResolutionFromRemoteVpc bool `json:"allowDnsResolutionFromRemoteVpc"`
}

type VPCPeeringOutputs struct {
	VpcPeeringConnectionId string `json:"vpcPeeringConnectionId"`
	RequesterVpcId         string `json:"requesterVpcId"`
	AccepterVpcId          string `json:"accepterVpcId"`
	RequesterCidrBlock     string `json:"requesterCidrBlock"`
	AccepterCidrBlock      string `json:"accepterCidrBlock"`
	Status                 string `json:"status"`
	RequesterOwnerId       string `json:"requesterOwnerId"`
	AccepterOwnerId        string `json:"accepterOwnerId"`
}

type ObservedState struct {
	VpcPeeringConnectionId string            `json:"vpcPeeringConnectionId"`
	RequesterVpcId         string            `json:"requesterVpcId"`
	AccepterVpcId          string            `json:"accepterVpcId"`
	RequesterCidrBlock     string            `json:"requesterCidrBlock"`
	AccepterCidrBlock      string            `json:"accepterCidrBlock"`
	Status                 string            `json:"status"`
	RequesterOwnerId       string            `json:"requesterOwnerId"`
	AccepterOwnerId        string            `json:"accepterOwnerId"`
	RequesterOptions       *PeeringOptions   `json:"requesterOptions,omitempty"`
	AccepterOptions        *PeeringOptions   `json:"accepterOptions,omitempty"`
	Tags                   map[string]string `json:"tags"`
}

type VPCPeeringState struct {
	Desired            VPCPeeringSpec       `json:"desired"`
	Observed           ObservedState        `json:"observed"`
	Outputs            VPCPeeringOutputs    `json:"outputs"`
	Status             types.ResourceStatus `json:"status"`
	Mode               types.Mode           `json:"mode"`
	Error              string               `json:"error,omitempty"`
	Generation         int64                `json:"generation"`
	LastReconcile      string               `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
