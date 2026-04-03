// Package vpcpeering implements the Praxis driver for AWS VPC Peering Connections.
//
// A VPC Peering Connection is a networking link between two VPCs that enables
// routing traffic using private IPv4 addresses. Peering can be within the same
// account/region or cross-account/cross-region. After creation, the peering
// must be accepted by the peer VPC owner before traffic can flow.
//
// This driver manages creation, auto-acceptance (same-account only), peering
// options (DNS resolution), tag management, drift detection, and deletion.
// The VPC IDs are immutable; only tags and peering options can be corrected.
package vpcpeering

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate Virtual Object name used to register this driver.
const ServiceName = "VPCPeeringConnection"

// VPCPeeringSpec is the user-declared desired state for a VPC Peering Connection.
type VPCPeeringSpec struct {
	Account          string            `json:"account,omitempty"`          // AWS account alias resolved to credentials.
	Region           string            `json:"region"`                     // AWS region for the requester side.
	RequesterVpcId   string            `json:"requesterVpcId"`             // VPC ID initiating the peering (immutable).
	AccepterVpcId    string            `json:"accepterVpcId"`              // VPC ID receiving the peering request (immutable).
	PeerOwnerId      string            `json:"peerOwnerId,omitempty"`      // AWS account ID of the accepter (cross-account).
	PeerRegion       string            `json:"peerRegion,omitempty"`       // AWS region of the accepter VPC (cross-region).
	AutoAccept       bool              `json:"autoAccept"`                 // Automatically accept the peering (same-account only).
	RequesterOptions *PeeringOptions   `json:"requesterOptions,omitempty"` // Requester-side peering options.
	AccepterOptions  *PeeringOptions   `json:"accepterOptions,omitempty"`  // Accepter-side peering options.
	Tags             map[string]string `json:"tags,omitempty"`             // AWS resource tags.
	ManagedKey       string            `json:"managedKey,omitempty"`       // Ownership key as praxis:managed-key tag.
}

// PeeringOptions configures VPC peering behavior for one side of the connection.
type PeeringOptions struct {
	AllowDnsResolutionFromRemoteVpc bool `json:"allowDnsResolutionFromRemoteVpc"` // Whether DNS queries from the remote VPC resolve to private IPs.
}

// VPCPeeringOutputs holds AWS-assigned identifiers after provisioning.
type VPCPeeringOutputs struct {
	VpcPeeringConnectionId string `json:"vpcPeeringConnectionId"` // AWS-assigned peering ID (pcx-xxxx).
	RequesterVpcId         string `json:"requesterVpcId"`         // Requester VPC ID.
	AccepterVpcId          string `json:"accepterVpcId"`          // Accepter VPC ID.
	RequesterCidrBlock     string `json:"requesterCidrBlock"`     // Primary CIDR of the requester VPC.
	AccepterCidrBlock      string `json:"accepterCidrBlock"`      // Primary CIDR of the accepter VPC.
	Status                 string `json:"status"`                 // Peering status: pending-acceptance, active, deleted, etc.
	RequesterOwnerId       string `json:"requesterOwnerId"`       // AWS account ID of the requester.
	AccepterOwnerId        string `json:"accepterOwnerId"`        // AWS account ID of the accepter.
}

// ObservedState captures the live AWS configuration of a VPC Peering Connection.
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

// VPCPeeringState is the single atomic state object stored under drivers.StateKey.
type VPCPeeringState struct {
	Desired            VPCPeeringSpec       `json:"desired"`                 // User-declared target configuration.
	Observed           ObservedState        `json:"observed"`                // Last-known AWS state.
	Outputs            VPCPeeringOutputs    `json:"outputs"`                 // Stable identifiers returned to callers.
	Status             types.ResourceStatus `json:"status"`                  // Lifecycle status.
	Mode               types.Mode           `json:"mode"`                    // Managed or Observed.
	Error              string               `json:"error,omitempty"`         // Error message when Status == Error.
	Generation         int64                `json:"generation"`              // Monotonically increasing counter.
	LastReconcile      string               `json:"lastReconcile,omitempty"` // RFC 3339 timestamp of last reconcile.
	ReconcileScheduled bool                 `json:"reconcileScheduled"`      // Deduplication flag for reconcile scheduling.
}
