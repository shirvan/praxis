// Package igw implements the Praxis driver for AWS Internet Gateways.
//
// An Internet Gateway (IGW) enables communication between instances in a VPC
// and the internet. Each IGW can be attached to exactly one VPC at a time.
// The resource itself is region-level; the VPC attachment is the mutable
// relationship. Drift detection checks the VPC attachment and tags.
package igw

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate Virtual Object name used to register this driver.
const ServiceName = "InternetGateway"

// IGWSpec is the user-declared desired state for an Internet Gateway.
type IGWSpec struct {
	Account    string            `json:"account,omitempty"`    // AWS account alias resolved to credentials.
	Region     string            `json:"region"`               // AWS region for the IGW.
	VpcId      string            `json:"vpcId"`                // VPC to attach this IGW to (maps to EC2 AttachInternetGateway).
	Tags       map[string]string `json:"tags,omitempty"`       // AWS resource tags; praxis:-prefixed tags are reserved.
	ManagedKey string            `json:"managedKey,omitempty"` // Ownership key stored as praxis:managed-key tag.
}

// IGWOutputs holds AWS-assigned identifiers produced after provisioning.
type IGWOutputs struct {
	InternetGatewayId string `json:"internetGatewayId"` // AWS-assigned ID (igw-xxxx).
	VpcId             string `json:"vpcId"`             // Currently attached VPC.
	OwnerId           string `json:"ownerId"`           // AWS account ID that owns the IGW.
	State             string `json:"state"`             // Attachment state from AWS.
}

// ObservedState captures the live AWS configuration of an Internet Gateway.
// AttachedVpcId may differ from the spec VpcId when the IGW has been detached
// or re-attached outside of Praxis.
type ObservedState struct {
	InternetGatewayId string            `json:"internetGatewayId"`
	AttachedVpcId     string            `json:"attachedVpcId"` // VPC currently attached, empty if detached.
	OwnerId           string            `json:"ownerId"`
	Tags              map[string]string `json:"tags"`
}

// IGWState is the single atomic state object stored under drivers.StateKey.
type IGWState struct {
	Desired            IGWSpec              `json:"desired"`                 // User-declared target configuration.
	Observed           ObservedState        `json:"observed"`                // Last-known AWS state.
	Outputs            IGWOutputs           `json:"outputs"`                 // Stable identifiers returned to callers.
	Status             types.ResourceStatus `json:"status"`                  // Lifecycle status.
	Mode               types.Mode           `json:"mode"`                    // Managed or Observed.
	Error              string               `json:"error,omitempty"`         // Error message when Status == Error.
	Generation         int64                `json:"generation"`              // Monotonically increasing counter.
	LastReconcile      string               `json:"lastReconcile,omitempty"` // RFC 3339 timestamp of last reconcile.
	ReconcileScheduled bool                 `json:"reconcileScheduled"`      // Deduplication flag for reconcile scheduling.
}
