package sg

import "github.com/shirvan/praxis/pkg/types"

type SecurityGroupSpec struct {
	Account      string            `json:"account,omitempty"`
	GroupName    string            `json:"groupName"`
	Description  string            `json:"description"`
	VpcId        string            `json:"vpcId"`
	IngressRules []IngressRule     `json:"ingressRules,omitempty"`
	EgressRules  []EgressRule      `json:"egressRules,omitempty"`
	Tags         map[string]string `json:"tags,omitempty"`
}

type IngressRule struct {
	Protocol  string `json:"protocol"`
	FromPort  int32  `json:"fromPort"`
	ToPort    int32  `json:"toPort"`
	CidrBlock string `json:"cidrBlock,omitempty"`
}

type EgressRule struct {
	Protocol  string `json:"protocol"`
	FromPort  int32  `json:"fromPort"`
	ToPort    int32  `json:"toPort"`
	CidrBlock string `json:"cidrBlock,omitempty"`
}

type SecurityGroupOutputs struct {
	GroupId  string `json:"groupId"`
	GroupArn string `json:"groupArn"`
	VpcId    string `json:"vpcId"`
}

type ObservedState struct {
	GroupId      string            `json:"groupId"`
	GroupName    string            `json:"groupName"`
	Description  string            `json:"description"`
	VpcId        string            `json:"vpcId"`
	OwnerId      string            `json:"ownerId,omitempty"`
	IngressRules []NormalizedRule  `json:"ingressRules"`
	EgressRules  []NormalizedRule  `json:"egressRules"`
	Tags         map[string]string `json:"tags"`
}

type SecurityGroupState struct {
	Desired            SecurityGroupSpec    `json:"desired"`
	Observed           ObservedState        `json:"observed"`
	Outputs            SecurityGroupOutputs `json:"outputs"`
	Status             types.ResourceStatus `json:"status"`
	Mode               types.Mode           `json:"mode"`
	Error              string               `json:"error,omitempty"`
	Generation         int64                `json:"generation"`
	LastReconcile      string               `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
