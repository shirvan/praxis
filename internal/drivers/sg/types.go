// Package sg implements the Praxis driver for AWS EC2 Security Groups.
//
// A Security Group acts as a virtual firewall for EC2 instances, controlling
// inbound (ingress) and outbound (egress) traffic at the instance-network-
// interface level. Each security group is scoped to a VPC and identified by
// a unique group ID (sg-xxxx) assigned by AWS.
//
// Provider-specific rule and tag convergence is bound to the shared Praxis
// lifecycle kernel. Rules are normalized into a canonical set representation
// so AWS ordering differences do not produce false-positive drift.
package sg

// SecurityGroupSpec is the user-declared desired state for a security group.
// It maps directly to the fields accepted by the EC2 CreateSecurityGroup and
// Authorize/Revoke APIs.
type SecurityGroupSpec struct {
	Account      string            `json:"account,omitempty"`      // AWS account alias resolved by AuthClient to obtain credentials.
	GroupName    string            `json:"groupName"`              // Unique name within the VPC (maps to EC2 GroupName).
	Description  string            `json:"description"`            // Human-readable description (immutable after creation in AWS).
	VpcId        string            `json:"vpcId"`                  // VPC in which the security group is created.
	IngressRules []IngressRule     `json:"ingressRules,omitempty"` // Inbound firewall rules (protocol/port/CIDR).
	EgressRules  []EgressRule      `json:"egressRules,omitempty"`  // Outbound firewall rules.
	Tags         map[string]string `json:"tags,omitempty"`         // AWS resource tags applied to the security group.
	ManagedKey   string            `json:"managedKey,omitempty"`   // Internal ownership key derived from the Restate object key.
}

// IngressRule describes a single inbound permission entry.
// Protocol accepts "tcp", "udp", "icmp", or "all" (mapped to "-1" by AWS).
type IngressRule struct {
	Protocol  string `json:"protocol"`            // IP protocol: "tcp", "udp", "icmp", or "all".
	FromPort  int32  `json:"fromPort"`            // Start of the port range (0 for all/ICMP type).
	ToPort    int32  `json:"toPort"`              // End of the port range (65535 for all/ICMP code).
	CidrBlock string `json:"cidrBlock,omitempty"` // IPv4 CIDR block, e.g. "10.0.0.0/8".
}

// EgressRule describes a single outbound permission entry.
type EgressRule struct {
	Protocol  string `json:"protocol"`            // IP protocol.
	FromPort  int32  `json:"fromPort"`            // Start of port range.
	ToPort    int32  `json:"toPort"`              // End of port range.
	CidrBlock string `json:"cidrBlock,omitempty"` // IPv4 CIDR block.
}

// SecurityGroupOutputs holds the AWS-assigned identifiers produced after
// provisioning. These are stored in Restate K/V and returned to callers.
type SecurityGroupOutputs struct {
	GroupId  string `json:"groupId"`  // AWS-assigned security group ID (sg-xxxx).
	GroupArn string `json:"groupArn"` // Synthesized ARN for cross-driver references.
	VpcId    string `json:"vpcId"`    // The VPC enclosing the security group.
}

// ObservedState captures the live AWS configuration of a security group as
// returned by DescribeSecurityGroups. It is compared against the desired spec
// during drift detection.
type ObservedState struct {
	Region       string            `json:"region,omitempty"`
	GroupId      string            `json:"groupId"`
	GroupName    string            `json:"groupName"`
	Description  string            `json:"description"`
	VpcId        string            `json:"vpcId"`
	OwnerId      string            `json:"ownerId,omitempty"`
	IngressRules []NormalizedRule  `json:"ingressRules"`
	EgressRules  []NormalizedRule  `json:"egressRules"`
	Tags         map[string]string `json:"tags"`
}
