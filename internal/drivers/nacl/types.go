package nacl

import "github.com/shirvan/praxis/pkg/types"

const ServiceName = "NetworkACL"

type NetworkACLSpec struct {
	Account            string            `json:"account,omitempty"`
	Region             string            `json:"region"`
	VpcId              string            `json:"vpcId"`
	IngressRules       []NetworkACLRule  `json:"ingressRules,omitempty"`
	EgressRules        []NetworkACLRule  `json:"egressRules,omitempty"`
	SubnetAssociations []string          `json:"subnetAssociations,omitempty"`
	Tags               map[string]string `json:"tags,omitempty"`
	ManagedKey         string            `json:"managedKey,omitempty"`
}

type NetworkACLRule struct {
	RuleNumber int    `json:"ruleNumber"`
	Protocol   string `json:"protocol"`
	RuleAction string `json:"ruleAction"`
	CidrBlock  string `json:"cidrBlock"`
	FromPort   int    `json:"fromPort,omitempty"`
	ToPort     int    `json:"toPort,omitempty"`
}

type NetworkACLAssociation struct {
	AssociationId string `json:"associationId"`
	SubnetId      string `json:"subnetId"`
}

type NetworkACLOutputs struct {
	NetworkAclId string                  `json:"networkAclId"`
	VpcId        string                  `json:"vpcId"`
	IsDefault    bool                    `json:"isDefault"`
	IngressRules []NetworkACLRule        `json:"ingressRules"`
	EgressRules  []NetworkACLRule        `json:"egressRules"`
	Associations []NetworkACLAssociation `json:"associations"`
}

type ObservedState struct {
	NetworkAclId string                  `json:"networkAclId"`
	VpcId        string                  `json:"vpcId"`
	IsDefault    bool                    `json:"isDefault"`
	IngressRules []NetworkACLRule        `json:"ingressRules"`
	EgressRules  []NetworkACLRule        `json:"egressRules"`
	Associations []NetworkACLAssociation `json:"associations"`
	Tags         map[string]string       `json:"tags"`
}

type NetworkACLState struct {
	Desired            NetworkACLSpec       `json:"desired"`
	Observed           ObservedState        `json:"observed"`
	Outputs            NetworkACLOutputs    `json:"outputs"`
	Status             types.ResourceStatus `json:"status"`
	Mode               types.Mode           `json:"mode"`
	Error              string               `json:"error,omitempty"`
	Generation         int64                `json:"generation"`
	LastReconcile      string               `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
