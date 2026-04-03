// Package nacl implements the Praxis driver for AWS Network ACLs.
//
// A Network ACL is a stateless firewall at the subnet level within a VPC.
// Unlike security groups, NACLs have explicit deny rules and numbered rule
// evaluation order. Each subnet must be associated with exactly one NACL;
// when a subnet is disassociated from a custom NACL it reverts to the VPC's
// default NACL.
//
// This driver manages create, rule convergence (individual rules via
// CreateEntry/DeleteEntry/ReplaceEntry), subnet associations (via
// ReplaceNetworkAclAssociation), tag management, drift detection, and
// deletion. On delete, all subnets are reassociated to the default NACL
// before the custom NACL is removed.
package nacl

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate Virtual Object name used to register this driver.
const ServiceName = "NetworkACL"

// NetworkACLSpec is the user-declared desired state for a Network ACL.
type NetworkACLSpec struct {
	Account            string            `json:"account,omitempty"`            // AWS account alias resolved to credentials.
	Region             string            `json:"region"`                       // AWS region.
	VpcId              string            `json:"vpcId"`                        // VPC that owns this NACL.
	IngressRules       []NetworkACLRule  `json:"ingressRules,omitempty"`       // Inbound rules evaluated in rule-number order.
	EgressRules        []NetworkACLRule  `json:"egressRules,omitempty"`        // Outbound rules evaluated in rule-number order.
	SubnetAssociations []string          `json:"subnetAssociations,omitempty"` // Subnet IDs to associate with this NACL.
	Tags               map[string]string `json:"tags,omitempty"`               // AWS resource tags.
	ManagedKey         string            `json:"managedKey,omitempty"`         // Ownership key as praxis:managed-key tag.
}

// NetworkACLRule defines a single numbered NACL entry (ingress or egress).
// Protocol uses IANA numbers ("6" for TCP, "17" for UDP, "-1" for all)
// but the driver also accepts names ("tcp", "udp", "icmp", "all") and
// normalizes them before sending to the AWS API.
type NetworkACLRule struct {
	RuleNumber int    `json:"ruleNumber"`         // Priority 1-32766; lower numbers evaluated first.
	Protocol   string `json:"protocol"`           // IANA protocol number or name.
	RuleAction string `json:"ruleAction"`         // "allow" or "deny".
	CidrBlock  string `json:"cidrBlock"`          // IPv4 CIDR block.
	FromPort   int    `json:"fromPort,omitempty"` // Start of port range (0 for all/ICMP type).
	ToPort     int    `json:"toPort,omitempty"`   // End of port range (0 for all/ICMP code).
}

// NetworkACLAssociation records the association between a NACL and a subnet.
type NetworkACLAssociation struct {
	AssociationId string `json:"associationId"` // AWS-assigned association ID (aclassoc-xxxx).
	SubnetId      string `json:"subnetId"`      // Subnet that is associated.
}

// NetworkACLOutputs holds AWS-assigned identifiers after provisioning.
type NetworkACLOutputs struct {
	NetworkAclId string                  `json:"networkAclId"` // AWS-assigned NACL ID (acl-xxxx).
	VpcId        string                  `json:"vpcId"`        // Owning VPC.
	IsDefault    bool                    `json:"isDefault"`    // Whether this is the VPC's default NACL.
	IngressRules []NetworkACLRule        `json:"ingressRules"` // Current ingress rules from AWS.
	EgressRules  []NetworkACLRule        `json:"egressRules"`  // Current egress rules from AWS.
	Associations []NetworkACLAssociation `json:"associations"` // Current subnet associations.
}

// ObservedState captures the live AWS configuration of a Network ACL.
// Rule number 32767 (the implicit default deny) and IPv6 entries are
// filtered out during Describe so they don't produce false-positive drift.
type ObservedState struct {
	NetworkAclId string                  `json:"networkAclId"`
	VpcId        string                  `json:"vpcId"`
	IsDefault    bool                    `json:"isDefault"`
	IngressRules []NetworkACLRule        `json:"ingressRules"`
	EgressRules  []NetworkACLRule        `json:"egressRules"`
	Associations []NetworkACLAssociation `json:"associations"`
	Tags         map[string]string       `json:"tags"`
}

// NetworkACLState is the single atomic state object stored under drivers.StateKey.
type NetworkACLState struct {
	Desired            NetworkACLSpec       `json:"desired"`                 // User-declared target configuration.
	Observed           ObservedState        `json:"observed"`                // Last-known AWS state.
	Outputs            NetworkACLOutputs    `json:"outputs"`                 // Stable identifiers returned to callers.
	Status             types.ResourceStatus `json:"status"`                  // Lifecycle status.
	Mode               types.Mode           `json:"mode"`                    // Managed (drift corrected) or Observed (drift reported only).
	Error              string               `json:"error,omitempty"`         // Error message when Status == Error.
	Generation         int64                `json:"generation"`              // Monotonically increasing counter.
	LastReconcile      string               `json:"lastReconcile,omitempty"` // RFC 3339 timestamp of last reconcile.
	ReconcileScheduled bool                 `json:"reconcileScheduled"`      // Deduplication flag for reconcile scheduling.
}
