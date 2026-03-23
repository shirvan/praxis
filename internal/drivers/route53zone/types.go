package route53zone

import "github.com/shirvan/praxis/pkg/types"

const ServiceName = "Route53HostedZone"

type HostedZoneVPC struct {
	VpcId     string `json:"vpcId"`
	VpcRegion string `json:"vpcRegion"`
}

type HostedZoneSpec struct {
	Account    string            `json:"account,omitempty"`
	Name       string            `json:"name"`
	Comment    string            `json:"comment,omitempty"`
	IsPrivate  bool              `json:"isPrivate,omitempty"`
	VPCs       []HostedZoneVPC   `json:"vpcs,omitempty"`
	Tags       map[string]string `json:"tags,omitempty"`
	ManagedKey string            `json:"managedKey,omitempty"`
}

type HostedZoneOutputs struct {
	HostedZoneId string   `json:"hostedZoneId"`
	Name         string   `json:"name"`
	NameServers  []string `json:"nameServers,omitempty"`
	IsPrivate    bool     `json:"isPrivate"`
	RecordCount  int64    `json:"recordCount"`
}

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
