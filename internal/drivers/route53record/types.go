package route53record

import "github.com/shirvan/praxis/pkg/types"

const ServiceName = "Route53Record"

type AliasTarget struct {
	HostedZoneId         string `json:"hostedZoneId"`
	DNSName              string `json:"dnsName"`
	EvaluateTargetHealth bool   `json:"evaluateTargetHealth,omitempty"`
}

type GeoLocation struct {
	ContinentCode   string `json:"continentCode,omitempty"`
	CountryCode     string `json:"countryCode,omitempty"`
	SubdivisionCode string `json:"subdivisionCode,omitempty"`
}

type RecordIdentity struct {
	HostedZoneId  string `json:"hostedZoneId"`
	Name          string `json:"name"`
	Type          string `json:"type"`
	SetIdentifier string `json:"setIdentifier,omitempty"`
}

type RecordSpec struct {
	Account          string       `json:"account,omitempty"`
	HostedZoneId     string       `json:"hostedZoneId"`
	Name             string       `json:"name"`
	Type             string       `json:"type"`
	TTL              int64        `json:"ttl,omitempty"`
	ResourceRecords  []string     `json:"resourceRecords,omitempty"`
	AliasTarget      *AliasTarget `json:"aliasTarget,omitempty"`
	SetIdentifier    string       `json:"setIdentifier,omitempty"`
	Weight           int64        `json:"weight,omitempty"`
	Region           string       `json:"region,omitempty"`
	Failover         string       `json:"failover,omitempty"`
	GeoLocation      *GeoLocation `json:"geoLocation,omitempty"`
	MultiValueAnswer bool         `json:"multiValueAnswer,omitempty"`
	HealthCheckId    string       `json:"healthCheckId,omitempty"`
	ManagedKey       string       `json:"managedKey,omitempty"`
}

type RecordOutputs struct {
	HostedZoneId  string `json:"hostedZoneId"`
	FQDN          string `json:"fqdn"`
	Type          string `json:"type"`
	SetIdentifier string `json:"setIdentifier,omitempty"`
}

type ObservedState struct {
	HostedZoneId     string       `json:"hostedZoneId"`
	Name             string       `json:"name"`
	Type             string       `json:"type"`
	TTL              int64        `json:"ttl,omitempty"`
	ResourceRecords  []string     `json:"resourceRecords,omitempty"`
	AliasTarget      *AliasTarget `json:"aliasTarget,omitempty"`
	SetIdentifier    string       `json:"setIdentifier,omitempty"`
	Weight           int64        `json:"weight,omitempty"`
	Region           string       `json:"region,omitempty"`
	Failover         string       `json:"failover,omitempty"`
	GeoLocation      *GeoLocation `json:"geoLocation,omitempty"`
	MultiValueAnswer bool         `json:"multiValueAnswer,omitempty"`
	HealthCheckId    string       `json:"healthCheckId,omitempty"`
}

type RecordState struct {
	Desired            RecordSpec           `json:"desired"`
	Observed           ObservedState        `json:"observed"`
	Outputs            RecordOutputs        `json:"outputs"`
	Status             types.ResourceStatus `json:"status"`
	Mode               types.Mode           `json:"mode"`
	Error              string               `json:"error,omitempty"`
	Generation         int64                `json:"generation"`
	LastReconcile      string               `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
