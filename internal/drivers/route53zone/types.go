// Package route53zone implements the Restate virtual-object driver for AWS Route53 Hosted Zones.
// It manages public and private hosted zones including VPC associations, comments, and tags
// via the create-or-converge pattern with drift detection.
package route53zone

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate virtual object service name used to register and address this driver.
const ServiceName = "Route53HostedZone"

// HostedZoneVPC represents a VPC association for a private hosted zone.
type HostedZoneVPC struct {
	VpcId     string `json:"vpcId"`
	VpcRegion string `json:"vpcRegion"`
}

// HostedZoneSpec defines the desired state of a Route53 hosted zone.
type HostedZoneSpec struct {
	Account    string            `json:"account,omitempty"`    // AWS account alias resolved via the auth service.
	Name       string            `json:"name"`                 // Zone domain name (immutable, derived from Restate key).
	Comment    string            `json:"comment,omitempty"`    // Descriptive comment shown in the AWS console.
	IsPrivate  bool              `json:"isPrivate,omitempty"`  // Whether this is a private hosted zone (immutable).
	VPCs       []HostedZoneVPC   `json:"vpcs,omitempty"`       // VPC associations (required for private zones).
	Tags       map[string]string `json:"tags,omitempty"`       // User-managed tags ("praxis:"-prefixed tags excluded from drift).
	ManagedKey string            `json:"managedKey,omitempty"` // CallerReference for idempotent zone creation.
}

// HostedZoneOutputs holds the Route53-assigned identifiers returned after provisioning.
type HostedZoneOutputs struct {
	HostedZoneId string   `json:"hostedZoneId"`
	Name         string   `json:"name"`
	NameServers  []string `json:"nameServers,omitempty"`
	IsPrivate    bool     `json:"isPrivate"`
	RecordCount  int64    `json:"recordCount"`
}

// ObservedState captures the last-known live state of the hosted zone from AWS.
type ObservedState struct {
	HostedZoneId    string            `json:"hostedZoneId"`
	Name            string            `json:"name"`
	CallerReference string            `json:"callerReference,omitempty"`
	Comment         string            `json:"comment,omitempty"`
	IsPrivate       bool              `json:"isPrivate"`
	VPCs            []HostedZoneVPC   `json:"vpcs,omitempty"`
	Tags            map[string]string `json:"tags,omitempty"`
	NameServers     []string          `json:"nameServers,omitempty"`
	RecordCount     int64             `json:"recordCount"`
}

// HostedZoneState is the full persisted state of this virtual-object, stored via restate.Set.
type HostedZoneState struct {
	Desired            HostedZoneSpec       `json:"desired"`
	Observed           ObservedState        `json:"observed"`
	Outputs            HostedZoneOutputs    `json:"outputs"`
	Status             types.ResourceStatus `json:"status"`
	Mode               types.Mode           `json:"mode"`
	Error              string               `json:"error,omitempty"`
	Generation         int64                `json:"generation"`
	LastReconcile      string               `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
