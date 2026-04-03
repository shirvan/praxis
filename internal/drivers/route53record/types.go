// Package route53record implements the Restate virtual-object driver for AWS Route53 DNS Records.
// It manages individual record sets within a hosted zone, supporting standard records (TTL + values),
// alias records, and routing policies (weighted, latency, failover, geolocation, multivalue)
// via UPSERT-based idempotent provisioning with drift detection.
package route53record

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate virtual object service name used to register and address this driver.
const ServiceName = "Route53Record"

// AliasTarget references another AWS resource (e.g., ALB, CloudFront) as a DNS alias.
type AliasTarget struct {
	HostedZoneId         string `json:"hostedZoneId"`
	DNSName              string `json:"dnsName"`
	EvaluateTargetHealth bool   `json:"evaluateTargetHealth,omitempty"`
}

// GeoLocation defines geolocation-based routing parameters.
type GeoLocation struct {
	ContinentCode   string `json:"continentCode,omitempty"`
	CountryCode     string `json:"countryCode,omitempty"`
	SubdivisionCode string `json:"subdivisionCode,omitempty"`
}

// RecordIdentity is the composite key (hostedZoneId + name + type + setIdentifier) that
// uniquely identifies a record set, parsed from the Restate object key via "~" delimiter.
type RecordIdentity struct {
	HostedZoneId  string `json:"hostedZoneId"`
	Name          string `json:"name"`
	Type          string `json:"type"`
	SetIdentifier string `json:"setIdentifier,omitempty"`
}

// RecordSpec defines the desired state of a Route53 DNS record.
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

// RecordOutputs holds identifiers returned after provisioning.
type RecordOutputs struct {
	HostedZoneId  string `json:"hostedZoneId"`
	FQDN          string `json:"fqdn"`
	Type          string `json:"type"`
	SetIdentifier string `json:"setIdentifier,omitempty"`
}

// ObservedState captures the last-known live state of the record from AWS.
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

// RecordState is the full persisted state of this virtual-object, stored via restate.Set.
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
