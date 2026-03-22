# Route 53 DNS Record Driver — Implementation Plan

> NYI
> Target: A Restate Virtual Object driver that manages Route 53 DNS Record Sets,
> providing full lifecycle management including creation, import, deletion, drift
> detection, and drift correction for standard records, alias records, and routing
> policies (simple, weighted, latency, failover, geolocation, multivalue).
>
> Key scope: `KeyScopeCustom` — key format is `hostedZoneId~fqdn~type` (with an
> optional `~setIdentifier` suffix for routing policy records), permanent and
> immutable for the lifetime of the Virtual Object. A DNS record set is uniquely
> identified by the combination of hosted zone, fully-qualified domain name, record
> type, and set identifier.

---

## Table of Contents

1. [Overview & Scope](#1-overview--scope)
2. [Key Strategy](#2-key-strategy)
3. [File Inventory](#3-file-inventory)
4. [Step 1 — CUE Schema](#step-1--cue-schema)
5. [Step 2 — AWS Client Factory](#step-2--aws-client-factory)
6. [Step 3 — Driver Types](#step-3--driver-types)
7. [Step 4 — AWS API Abstraction Layer](#step-4--aws-api-abstraction-layer)
8. [Step 5 — Drift Detection](#step-5--drift-detection)
9. [Step 6 — Driver Implementation](#step-6--driver-implementation)
10. [Step 7 — Provider Adapter](#step-7--provider-adapter)
11. [Step 8 — Registry Integration](#step-8--registry-integration)
12. [Step 9 — DNS Driver Pack Entry Point](#step-9--dns-driver-pack-entry-point)
13. [Step 10 — Docker Compose & Justfile](#step-10--docker-compose--justfile)
14. [Step 11 — Unit Tests](#step-11--unit-tests)
15. [Step 12 — Integration Tests](#step-12--integration-tests)
16. [DNS-Record-Specific Design Decisions](#dns-record-specific-design-decisions)
17. [Design Decisions (Resolved)](#design-decisions-resolved)
18. [Checklist](#checklist)

---

## 1. Overview & Scope

The DNS Record driver manages the lifecycle of Route 53 **resource record sets**
within a hosted zone. It creates, imports, updates, and deletes DNS records of all
standard types (A, AAAA, CNAME, MX, TXT, NS, SRV, CAA, PTR) as well as alias
records (A, AAAA aliases to AWS resources). It supports all Route 53 routing
policies: simple, weighted, latency-based, failover, geolocation, and multivalue
answer.

This is the most complex driver in the Route 53 family due to the diversity of
record types, the distinction between standard and alias records, the variety of
routing policies, and the change-batch API model (Route 53 uses a transactional
change batch instead of individual CRUD operations).

**Out of scope**:
- **Hosted zone management** — separate driver.
- **Health check management** — separate driver. This driver only references health
  check IDs as an optional property.
- **Traffic policies/traffic policy instances** — advanced Route 53 feature with its
  own lifecycle, deferred.
- **DNSSEC records (DS, DNSKEY)** — managed by DNSSEC signing configuration, deferred.

### Resource Scope for This Plan

| In Scope | Out of Scope |
|---|---|
| Standard records (A, AAAA, CNAME, MX, TXT, NS, SRV, CAA, PTR) | Traffic policies |
| Alias records (A, AAAA) | DNSSEC records |
| Simple routing | Traffic flow visual editor records |
| Weighted routing | |
| Latency-based routing | |
| Failover routing | |
| Geolocation routing | |
| Multivalue answer routing | |
| Health check association | |
| TTL management | |

### Driver Contract

| Handler | Context | Purpose |
|---|---|---|
| `Provision` | `ObjectContext` (exclusive) | Create or update a DNS record set |
| `Import` | `ObjectContext` (exclusive) | Adopt an existing DNS record set |
| `Delete` | `ObjectContext` (exclusive) | Delete a DNS record set |
| `Reconcile` | `ObjectContext` (exclusive) | Detect/correct drift (report-only for Observed mode) |
| `GetStatus` | `ObjectSharedContext` (shared) | Return current status |
| `GetOutputs` | `ObjectSharedContext` (shared) | Return DNS record outputs |

### Mutable vs Immutable Attributes

| Attribute | Mutability | Notes |
|---|---|---|
| `hostedZoneId` | Immutable | Part of the Virtual Object key |
| `name` (FQDN) | Immutable | Part of the Virtual Object key |
| `type` | Immutable | Part of the Virtual Object key (A, AAAA, CNAME, etc.) |
| `setIdentifier` | Immutable | Part of the key for routing policy records |
| `ttl` | Mutable | Updated via UPSERT change batch |
| `resourceRecords` | Mutable | Updated via UPSERT change batch |
| `aliasTarget` | Mutable | Updated via UPSERT change batch |
| `weight` | Mutable | Weighted routing parameter |
| `region` | Mutable | Latency routing parameter |
| `failover` | Mutable | Failover routing parameter (PRIMARY/SECONDARY) |
| `geoLocation` | Mutable | Geolocation routing parameter |
| `multiValueAnswer` | Mutable | Multivalue routing flag |
| `healthCheckId` | Mutable | Optional health check association |

### Downstream Consumers

```
${resources.my-record.outputs.fqdn}          → Application configuration, other DNS records
${resources.my-record.outputs.type}           → Informational
```

> **Note**: DNS records are typically leaf nodes in the dependency graph — they
> consume outputs from other resources (EIPs, ELBs, EC2 instances) rather than
> producing outputs consumed by others.

---

## 2. Key Strategy

### Key Scope: `KeyScopeCustom`

DNS record sets are uniquely identified by the combination of hosted zone ID,
fully-qualified domain name, record type, and optionally a set identifier (for
routing policy records). The key composes all identity components.

### Key Format

**Simple routing** (no set identifier):
```
<hostedZoneId>~<fqdn>~<type>
```

**Routing policy records** (with set identifier):
```
<hostedZoneId>~<fqdn>~<type>~<setIdentifier>
```

Examples:
```
Z1234567890~www.example.com~A
Z1234567890~api.example.com~CNAME
Z1234567890~app.example.com~A~primary        (failover)
Z1234567890~app.example.com~A~secondary      (failover)
Z1234567890~app.example.com~A~us-east-1      (latency)
Z1234567890~app.example.com~A~weight-80      (weighted)
```

### BuildKey

```go
func (a *Route53RecordAdapter) BuildKey(doc types.ResourceDocument) string {
    hostedZoneId := doc.Spec["hostedZoneId"].(string)
    name := doc.Spec["name"].(string)
    recordType := doc.Spec["type"].(string)

    key := hostedZoneId + "~" + name + "~" + recordType

    if setId, ok := doc.Spec["setIdentifier"].(string); ok && setId != "" {
        key += "~" + setId
    }
    return key
}
```

### BuildImportKey

```go
func (a *Route53RecordAdapter) BuildImportKey(region, resourceID string) string {
    // resourceID format: "<hostedZoneId>/<fqdn>/<type>" or
    //                    "<hostedZoneId>/<fqdn>/<type>/<setIdentifier>"
    // Converted to key format with ~ separator
    return strings.ReplaceAll(resourceID, "/", "~")
}
```

### No Ownership Tags

DNS records do not support tags. The driver relies on the record's natural identity
(zone + name + type + set identifier) for ownership. Route 53 does not allow
duplicate record sets with the same identity, providing natural conflict prevention.

---

## 3. File Inventory

```text
✦ schemas/aws/route53/record.cue                         — CUE schema for Route53Record
✦ internal/drivers/route53record/types.go                 — Spec, Outputs, ObservedState, State structs
✦ internal/drivers/route53record/aws.go                   — DNSRecordAPI interface + realDNSRecordAPI
✦ internal/drivers/route53record/drift.go                 — HasDrift(), ComputeFieldDiffs()
✦ internal/drivers/route53record/driver.go                — DNSRecordDriver Virtual Object
✦ internal/drivers/route53record/driver_test.go           — Unit tests for driver
✦ internal/drivers/route53record/aws_test.go              — Unit tests for error classification
✦ internal/drivers/route53record/drift_test.go            — Unit tests for drift detection
✦ internal/core/provider/route53record_adapter.go         — Route53RecordAdapter implementing provider.Adapter
✦ internal/core/provider/route53record_adapter_test.go    — Unit tests for adapter
✦ tests/integration/route53_record_driver_test.go         — Integration tests
✎ cmd/praxis-dns/main.go                                 — Add DNSRecord driver .Bind()
✎ internal/core/provider/registry.go                      — Add adapter to NewRegistry()
```

---

## Step 1 — CUE Schema

**File**: `schemas/aws/route53/record.cue`

```cue
package route53

#Route53Record: {
    apiVersion: "praxis.io/v1"
    kind:       "Route53Record"

    metadata: {
        // name is the logical resource name (used for DAG references).
        // The actual DNS name is specified in spec.name.
        name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9._-]{0,254}$"
        labels: [string]: string
    }

    spec: {
        // hostedZoneId is the ID of the hosted zone containing this record.
        hostedZoneId: string

        // name is the fully-qualified domain name for the record (e.g., "www.example.com").
        // Trailing dot is optional — the driver normalizes.
        name: string

        // type is the DNS record type.
        type: "A" | "AAAA" | "CNAME" | "MX" | "TXT" | "NS" | "SRV" | "CAA" | "PTR"

        // ttl is the time-to-live in seconds. Required for standard records.
        // Must be omitted for alias records.
        ttl?: int & >=0 & <=2147483647

        // resourceRecords is the list of record values.
        // Required for standard records. Must be omitted for alias records.
        resourceRecords?: [...string]

        // aliasTarget configures this as an alias record (Route 53 extension).
        // Mutually exclusive with ttl and resourceRecords.
        aliasTarget?: {
            // hostedZoneId is the hosted zone ID of the alias target
            // (e.g., the ELB's canonical hosted zone ID).
            hostedZoneId: string

            // dnsName is the DNS name of the alias target
            // (e.g., the ELB's DNS name).
            dnsName: string

            // evaluateTargetHealth determines whether Route 53 checks
            // the health of the alias target.
            evaluateTargetHealth: bool | *false
        }

        // --- Routing Policy Fields ---
        // At most one routing policy can be specified. If none is specified,
        // the record uses simple routing.

        // setIdentifier is required for all routing policies.
        // Must be unique among records with the same name and type.
        setIdentifier?: string

        // weight for weighted routing (0–255).
        weight?: int & >=0 & <=255

        // region for latency-based routing.
        region?: string

        // failover for failover routing.
        failover?: "PRIMARY" | "SECONDARY"

        // geoLocation for geolocation routing.
        geoLocation?: {
            continentCode?:   string
            countryCode?:     string
            subdivisionCode?: string
        }

        // multiValueAnswer enables multivalue answer routing.
        multiValueAnswer?: bool

        // healthCheckId is the ID of a health check to associate with this record.
        // Used with failover, weighted, latency, geolocation, and multivalue routing.
        healthCheckId?: string
    }

    outputs?: {
        fqdn: string
        type: string
    }
}
```

### Key Design Decisions

- **`metadata.name` vs `spec.name`**: The `metadata.name` is the logical resource
  name for DAG references (e.g., `web-record`). The `spec.name` is the actual DNS
  FQDN (e.g., `www.example.com`). This distinction exists because DNS names contain
  dots and may conflict with CUE path syntax in expressions.

- **Mutual exclusivity**: Standard records use `ttl` + `resourceRecords`. Alias
  records use `aliasTarget`. These are mutually exclusive — AWS enforces this.
  CUE's type system can express this via disjunctions, but for simplicity the
  driver validates at provision time.

- **Routing policy fields are optional**: Simple routing is the default (no set
  identifier, no routing fields). Other routing policies require `setIdentifier`
  plus the policy-specific field. The driver validates consistency at provision time.

- **Record type as string enum**: Only the most common record types are included.
  SOA records are excluded (auto-managed by AWS). NS records at the zone apex are
  auto-managed but can be created for subdomain delegation.

---

## Step 2 — AWS Client Factory

**File**: `internal/infra/awsclient/client.go` — Uses same Route 53 client as
Hosted Zone driver. No additional changes needed.

---

## Step 3 — Driver Types

**File**: `internal/drivers/route53record/types.go`

```go
package route53record

import "github.com/praxiscloud/praxis/pkg/types"

const ServiceName = "Route53Record"

// AliasTarget represents an alias record target.
type AliasTarget struct {
    HostedZoneId         string `json:"hostedZoneId"`
    DNSName              string `json:"dnsName"`
    EvaluateTargetHealth bool   `json:"evaluateTargetHealth"`
}

// GeoLocation represents geolocation routing parameters.
type GeoLocation struct {
    ContinentCode   string `json:"continentCode,omitempty"`
    CountryCode     string `json:"countryCode,omitempty"`
    SubdivisionCode string `json:"subdivisionCode,omitempty"`
}

// DNSRecordSpec is the desired state for a DNS record set.
type DNSRecordSpec struct {
    Account          string            `json:"account,omitempty"`
    HostedZoneId     string            `json:"hostedZoneId"`
    Name             string            `json:"name"`
    Type             string            `json:"type"`
    TTL              *int64            `json:"ttl,omitempty"`
    ResourceRecords  []string          `json:"resourceRecords,omitempty"`
    AliasTarget      *AliasTarget      `json:"aliasTarget,omitempty"`
    SetIdentifier    string            `json:"setIdentifier,omitempty"`
    Weight           *int64            `json:"weight,omitempty"`
    Region           string            `json:"region,omitempty"`
    Failover         string            `json:"failover,omitempty"`
    GeoLocation      *GeoLocation      `json:"geoLocation,omitempty"`
    MultiValueAnswer *bool             `json:"multiValueAnswer,omitempty"`
    HealthCheckId    string            `json:"healthCheckId,omitempty"`
}

// DNSRecordOutputs is produced after provisioning.
type DNSRecordOutputs struct {
    FQDN string `json:"fqdn"`
    Type string `json:"type"`
}

// ObservedState captures the actual DNS record set from AWS.
type ObservedState struct {
    Name             string       `json:"name"`
    Type             string       `json:"type"`
    TTL              *int64       `json:"ttl,omitempty"`
    ResourceRecords  []string     `json:"resourceRecords,omitempty"`
    AliasTarget      *AliasTarget `json:"aliasTarget,omitempty"`
    SetIdentifier    string       `json:"setIdentifier,omitempty"`
    Weight           *int64       `json:"weight,omitempty"`
    Region           string       `json:"region,omitempty"`
    Failover         string       `json:"failover,omitempty"`
    GeoLocation      *GeoLocation `json:"geoLocation,omitempty"`
    MultiValueAnswer *bool        `json:"multiValueAnswer,omitempty"`
    HealthCheckId    string       `json:"healthCheckId,omitempty"`
}

// DNSRecordState is the single atomic state object stored under drivers.StateKey.
type DNSRecordState struct {
    Desired            DNSRecordSpec        `json:"desired"`
    Observed           ObservedState        `json:"observed"`
    Outputs            DNSRecordOutputs     `json:"outputs"`
    Status             types.ResourceStatus `json:"status"`
    Mode               types.Mode           `json:"mode"`
    Error              string               `json:"error,omitempty"`
    Generation         int64                `json:"generation"`
    LastReconcile      string               `json:"lastReconcile,omitempty"`
    ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
```

### Why These Fields

- **`TTL` as `*int64`**: Pointer type because TTL must be absent for alias records.
  A zero TTL is valid and different from "not set".
- **`Weight` as `*int64`**: Pointer because a weight of 0 is valid (record is never
  returned) and different from "no weight" (not a weighted record).
- **`MultiValueAnswer` as `*bool`**: Pointer to distinguish "not a multivalue record"
  from "multivalue = false".
- **`ResourceRecords` as `[]string`**: Each string is the record value in AWS format.
  For MX records: `"10 mail.example.com"`. For TXT records: `"\"v=spf1 ...\""`
  (double-quoted). The driver passes values to AWS as-is.

---

## Step 4 — AWS API Abstraction Layer

**File**: `internal/drivers/route53record/aws.go`

### DNSRecordAPI Interface

```go
type DNSRecordAPI interface {
    // UpsertRecord creates or updates a DNS record set via a UPSERT change batch.
    UpsertRecord(ctx context.Context, hostedZoneId string, spec DNSRecordSpec) error

    // DescribeRecord returns the observed state of a specific record set.
    DescribeRecord(ctx context.Context, hostedZoneId, name, recordType, setIdentifier string) (ObservedState, error)

    // DeleteRecord deletes a DNS record set via a DELETE change batch.
    DeleteRecord(ctx context.Context, hostedZoneId string, observed ObservedState) error

    // WaitForChange waits for a Route 53 change to propagate.
    WaitForChange(ctx context.Context, changeId string) error
}
```

### realDNSRecordAPI Implementation

```go
type realDNSRecordAPI struct {
    client  *route53.Client
    limiter *ratelimit.Limiter
}

func NewDNSRecordAPI(client *route53.Client) DNSRecordAPI {
    return &realDNSRecordAPI{
        client:  client,
        limiter: ratelimit.New("route53", 5, 3),
    }
}
```

### Key Implementation Details

#### `UpsertRecord`

Route 53 uses a change batch API — there is no separate "Create" or "Update" call.
`UPSERT` creates the record if it doesn't exist, or replaces it if it does.

```go
func (r *realDNSRecordAPI) UpsertRecord(ctx context.Context, hostedZoneId string, spec DNSRecordSpec) error {
    // Normalize FQDN: ensure trailing dot
    name := spec.Name
    if !strings.HasSuffix(name, ".") {
        name = name + "."
    }

    rrs := &route53types.ResourceRecordSet{
        Name: aws.String(name),
        Type: route53types.RRType(spec.Type),
    }

    if spec.AliasTarget != nil {
        // Alias record
        dnsName := spec.AliasTarget.DNSName
        if !strings.HasSuffix(dnsName, ".") {
            dnsName = dnsName + "."
        }
        rrs.AliasTarget = &route53types.AliasTarget{
            HostedZoneId:         aws.String(spec.AliasTarget.HostedZoneId),
            DNSName:              aws.String(dnsName),
            EvaluateTargetHealth: spec.AliasTarget.EvaluateTargetHealth,
        }
    } else {
        // Standard record
        if spec.TTL != nil {
            rrs.TTL = spec.TTL
        }
        records := make([]route53types.ResourceRecord, 0, len(spec.ResourceRecords))
        for _, value := range spec.ResourceRecords {
            records = append(records, route53types.ResourceRecord{
                Value: aws.String(value),
            })
        }
        rrs.ResourceRecords = records
    }

    // Routing policy fields
    if spec.SetIdentifier != "" {
        rrs.SetIdentifier = aws.String(spec.SetIdentifier)
    }
    if spec.Weight != nil {
        rrs.Weight = spec.Weight
    }
    if spec.Region != "" {
        rrs.Region = route53types.ResourceRecordSetRegion(spec.Region)
    }
    if spec.Failover != "" {
        rrs.Failover = route53types.ResourceRecordSetFailover(spec.Failover)
    }
    if spec.GeoLocation != nil {
        rrs.GeoLocation = &route53types.GeoLocation{
            ContinentCode:   nilIfEmpty(spec.GeoLocation.ContinentCode),
            CountryCode:     nilIfEmpty(spec.GeoLocation.CountryCode),
            SubdivisionCode: nilIfEmpty(spec.GeoLocation.SubdivisionCode),
        }
    }
    if spec.MultiValueAnswer != nil {
        rrs.MultiValueAnswer = spec.MultiValueAnswer
    }
    if spec.HealthCheckId != "" {
        rrs.HealthCheckId = aws.String(spec.HealthCheckId)
    }

    input := &route53.ChangeResourceRecordSetsInput{
        HostedZoneId: aws.String(hostedZoneId),
        ChangeBatch: &route53types.ChangeBatch{
            Changes: []route53types.Change{{
                Action:            route53types.ChangeActionUpsert,
                ResourceRecordSet: rrs,
            }},
        },
    }

    _, err := r.client.ChangeResourceRecordSets(ctx, input)
    return err
}

func nilIfEmpty(s string) *string {
    if s == "" {
        return nil
    }
    return aws.String(s)
}
```

#### `DescribeRecord`

Route 53 does not have a "GetRecordSet" API. The driver uses
`ListResourceRecordSets` with a start name/type filter and scans for the exact
match.

```go
func (r *realDNSRecordAPI) DescribeRecord(ctx context.Context, hostedZoneId, name, recordType, setIdentifier string) (ObservedState, error) {
    // Normalize: ensure trailing dot
    if !strings.HasSuffix(name, ".") {
        name = name + "."
    }

    input := &route53.ListResourceRecordSetsInput{
        HostedZoneId:    aws.String(hostedZoneId),
        StartRecordName: aws.String(name),
        StartRecordType: route53types.RRType(recordType),
        MaxItems:        aws.Int32(10),
    }

    out, err := r.client.ListResourceRecordSets(ctx, input)
    if err != nil {
        return ObservedState{}, err
    }

    // Find exact match
    for _, rrs := range out.ResourceRecordSets {
        rrsName := strings.TrimSuffix(aws.ToString(rrs.Name), ".")
        queryName := strings.TrimSuffix(name, ".")

        if !strings.EqualFold(rrsName, queryName) {
            continue
        }
        if string(rrs.Type) != recordType {
            continue
        }
        if setIdentifier != "" && aws.ToString(rrs.SetIdentifier) != setIdentifier {
            continue
        }
        if setIdentifier == "" && rrs.SetIdentifier != nil {
            continue
        }

        return observedFromRecordSet(rrs), nil
    }

    return ObservedState{}, fmt.Errorf("record %s %s not found in zone %s", name, recordType, hostedZoneId)
}

func observedFromRecordSet(rrs route53types.ResourceRecordSet) ObservedState {
    obs := ObservedState{
        Name: strings.TrimSuffix(aws.ToString(rrs.Name), "."),
        Type: string(rrs.Type),
    }

    if rrs.AliasTarget != nil {
        obs.AliasTarget = &AliasTarget{
            HostedZoneId:         aws.ToString(rrs.AliasTarget.HostedZoneId),
            DNSName:              strings.TrimSuffix(aws.ToString(rrs.AliasTarget.DNSName), "."),
            EvaluateTargetHealth: rrs.AliasTarget.EvaluateTargetHealth,
        }
    } else {
        if rrs.TTL != nil {
            obs.TTL = rrs.TTL
        }
        for _, rr := range rrs.ResourceRecords {
            obs.ResourceRecords = append(obs.ResourceRecords, aws.ToString(rr.Value))
        }
    }

    if rrs.SetIdentifier != nil {
        obs.SetIdentifier = aws.ToString(rrs.SetIdentifier)
    }
    if rrs.Weight != nil {
        obs.Weight = rrs.Weight
    }
    if rrs.Region != "" {
        obs.Region = string(rrs.Region)
    }
    if rrs.Failover != "" {
        obs.Failover = string(rrs.Failover)
    }
    if rrs.GeoLocation != nil {
        obs.GeoLocation = &GeoLocation{
            ContinentCode:   aws.ToString(rrs.GeoLocation.ContinentCode),
            CountryCode:     aws.ToString(rrs.GeoLocation.CountryCode),
            SubdivisionCode: aws.ToString(rrs.GeoLocation.SubdivisionCode),
        }
    }
    if rrs.MultiValueAnswer != nil {
        obs.MultiValueAnswer = rrs.MultiValueAnswer
    }
    if rrs.HealthCheckId != nil {
        obs.HealthCheckId = aws.ToString(rrs.HealthCheckId)
    }

    return obs
}
```

#### `DeleteRecord`

Deleting a record requires the exact current record state — Route 53's DELETE
action must specify the complete record set as it currently exists.

```go
func (r *realDNSRecordAPI) DeleteRecord(ctx context.Context, hostedZoneId string, observed ObservedState) error {
    name := observed.Name
    if !strings.HasSuffix(name, ".") {
        name = name + "."
    }

    rrs := &route53types.ResourceRecordSet{
        Name: aws.String(name),
        Type: route53types.RRType(observed.Type),
    }

    if observed.AliasTarget != nil {
        dnsName := observed.AliasTarget.DNSName
        if !strings.HasSuffix(dnsName, ".") {
            dnsName = dnsName + "."
        }
        rrs.AliasTarget = &route53types.AliasTarget{
            HostedZoneId:         aws.String(observed.AliasTarget.HostedZoneId),
            DNSName:              aws.String(dnsName),
            EvaluateTargetHealth: observed.AliasTarget.EvaluateTargetHealth,
        }
    } else {
        rrs.TTL = observed.TTL
        for _, value := range observed.ResourceRecords {
            rrs.ResourceRecords = append(rrs.ResourceRecords, route53types.ResourceRecord{
                Value: aws.String(value),
            })
        }
    }

    if observed.SetIdentifier != "" {
        rrs.SetIdentifier = aws.String(observed.SetIdentifier)
    }
    if observed.Weight != nil {
        rrs.Weight = observed.Weight
    }
    if observed.Region != "" {
        rrs.Region = route53types.ResourceRecordSetRegion(observed.Region)
    }
    if observed.Failover != "" {
        rrs.Failover = route53types.ResourceRecordSetFailover(observed.Failover)
    }
    if observed.GeoLocation != nil {
        rrs.GeoLocation = &route53types.GeoLocation{
            ContinentCode:   nilIfEmpty(observed.GeoLocation.ContinentCode),
            CountryCode:     nilIfEmpty(observed.GeoLocation.CountryCode),
            SubdivisionCode: nilIfEmpty(observed.GeoLocation.SubdivisionCode),
        }
    }
    if observed.MultiValueAnswer != nil {
        rrs.MultiValueAnswer = observed.MultiValueAnswer
    }
    if observed.HealthCheckId != "" {
        rrs.HealthCheckId = aws.String(observed.HealthCheckId)
    }

    input := &route53.ChangeResourceRecordSetsInput{
        HostedZoneId: aws.String(hostedZoneId),
        ChangeBatch: &route53types.ChangeBatch{
            Changes: []route53types.Change{{
                Action:            route53types.ChangeActionDelete,
                ResourceRecordSet: rrs,
            }},
        },
    }

    _, err := r.client.ChangeResourceRecordSets(ctx, input)
    return err
}
```

> **Why delete needs observed state**: Route 53 DELETE requires the change batch to
> specify the exact current record values. If the record has drifted since last
> observed, the delete will fail with `InvalidChangeBatch`. The driver re-describes
> the record immediately before deletion to ensure consistency.

### Error Classification Helpers

```go
func IsInvalidChangeBatch(err error) bool {
    if err == nil {
        return false
    }
    var apiErr smithy.APIError
    if errors.As(err, &apiErr) {
        return apiErr.ErrorCode() == "InvalidChangeBatch"
    }
    return strings.Contains(err.Error(), "InvalidChangeBatch")
}

func IsNoSuchHostedZone(err error) bool {
    if err == nil {
        return false
    }
    var apiErr smithy.APIError
    if errors.As(err, &apiErr) {
        return apiErr.ErrorCode() == "NoSuchHostedZone"
    }
    return strings.Contains(err.Error(), "NoSuchHostedZone")
}

func IsNoSuchHealthCheck(err error) bool {
    if err == nil {
        return false
    }
    var apiErr smithy.APIError
    if errors.As(err, &apiErr) {
        return apiErr.ErrorCode() == "NoSuchHealthCheck"
    }
    return strings.Contains(err.Error(), "NoSuchHealthCheck")
}

func IsPriorRequestNotComplete(err error) bool {
    if err == nil {
        return false
    }
    var apiErr smithy.APIError
    if errors.As(err, &apiErr) {
        return apiErr.ErrorCode() == "PriorRequestNotComplete"
    }
    return strings.Contains(err.Error(), "PriorRequestNotComplete")
}

func IsRecordNotFound(err error) bool {
    if err == nil {
        return false
    }
    return strings.Contains(err.Error(), "not found in zone")
}
```

---

## Step 5 — Drift Detection

**File**: `internal/drivers/route53record/drift.go`

### Core Functions

**`HasDrift(desired DNSRecordSpec, observed ObservedState) bool`**

```go
func HasDrift(desired DNSRecordSpec, observed ObservedState) bool {
    // Standard record fields
    if desired.AliasTarget == nil && observed.AliasTarget == nil {
        if !ptrInt64Equal(desired.TTL, observed.TTL) {
            return true
        }
        if !recordValuesEqual(desired.ResourceRecords, observed.ResourceRecords) {
            return true
        }
    }

    // Alias record fields
    if desired.AliasTarget != nil && observed.AliasTarget != nil {
        if !aliasTargetsEqual(desired.AliasTarget, observed.AliasTarget) {
            return true
        }
    }

    // Alias vs standard mismatch
    if (desired.AliasTarget == nil) != (observed.AliasTarget == nil) {
        return true
    }

    // Routing policy fields
    if !ptrInt64Equal(desired.Weight, observed.Weight) {
        return true
    }
    if desired.Region != observed.Region {
        return true
    }
    if desired.Failover != observed.Failover {
        return true
    }
    if !geoLocationsEqual(desired.GeoLocation, observed.GeoLocation) {
        return true
    }
    if !ptrBoolEqual(desired.MultiValueAnswer, observed.MultiValueAnswer) {
        return true
    }
    if desired.HealthCheckId != observed.HealthCheckId {
        return true
    }

    return false
}
```

**`ComputeFieldDiffs(desired DNSRecordSpec, observed ObservedState) []FieldDiffEntry`**

Produces human-readable diffs for:

- Immutable fields: `name`, `type`, `setIdentifier` — reported with "(immutable)" suffix.
- TTL: old → new.
- Resource records: set diff (added, removed).
- Alias target: hostedZoneId, dnsName, evaluateTargetHealth changes.
- Routing policy fields: weight, region, failover, geoLocation changes.
- Health check association: old → new.

### Comparison Helpers

```go
func recordValuesEqual(desired, observed []string) bool {
    if len(desired) != len(observed) {
        return false
    }
    dSet := make(map[string]bool, len(desired))
    for _, v := range desired {
        dSet[v] = true
    }
    for _, v := range observed {
        if !dSet[v] {
            return false
        }
    }
    return true
}

func aliasTargetsEqual(a, b *AliasTarget) bool {
    if a == nil && b == nil {
        return true
    }
    if a == nil || b == nil {
        return false
    }
    return a.HostedZoneId == b.HostedZoneId &&
        strings.EqualFold(
            strings.TrimSuffix(a.DNSName, "."),
            strings.TrimSuffix(b.DNSName, ".")) &&
        a.EvaluateTargetHealth == b.EvaluateTargetHealth
}

func geoLocationsEqual(a, b *GeoLocation) bool {
    if a == nil && b == nil {
        return true
    }
    if a == nil || b == nil {
        return false
    }
    return a.ContinentCode == b.ContinentCode &&
        a.CountryCode == b.CountryCode &&
        a.SubdivisionCode == b.SubdivisionCode
}

func ptrInt64Equal(a, b *int64) bool {
    if a == nil && b == nil {
        return true
    }
    if a == nil || b == nil {
        return false
    }
    return *a == *b
}

func ptrBoolEqual(a, b *bool) bool {
    if a == nil && b == nil {
        return true
    }
    if a == nil || b == nil {
        return false
    }
    return *a == *b
}
```

---

## Step 6 — Driver Implementation

**File**: `internal/drivers/route53record/driver.go`

### Constructor Pattern

```go
func NewDNSRecordDriver(accounts *auth.Registry) *DNSRecordDriver
func NewDNSRecordDriverWithFactory(accounts *auth.Registry, factory func(aws.Config) DNSRecordAPI) *DNSRecordDriver
```

### Provision Handler

1. **Input validation**:
   - `hostedZoneId`, `name`, `type` must be non-empty.
   - Standard records: `ttl` and `resourceRecords` must be present.
   - Alias records: `aliasTarget` must be present; `ttl` and `resourceRecords` must be absent.
   - Routing policy: if `setIdentifier` is present, exactly one routing policy field
     must be set. If absent, no routing fields should be set.
   - Returns `TerminalError(400)` on validation failure.

2. **Load current state**: Reads `DNSRecordState` from Restate K/V. Sets status to
   `Provisioning`, increments generation.

3. **UPSERT record**: Calls `api.UpsertRecord` inside `restate.Run`. UPSERT is
   inherently idempotent — it creates if absent, replaces if present. This simplifies
   the create/update logic significantly compared to EC2-style drivers.
   - `IsInvalidChangeBatch` → `TerminalError(400)` (bad input, e.g., alias to
     non-existent target, invalid record value format).
   - `IsNoSuchHostedZone` → `TerminalError(404)` (zone doesn't exist).
   - `IsNoSuchHealthCheck` → `TerminalError(404)` (health check doesn't exist).
   - `IsPriorRequestNotComplete` → retryable (non-terminal).

4. **Describe final state**: Calls `api.DescribeRecord` to populate observed state.

5. **Commit state**: Sets status to `Ready`, saves atomically, schedules reconcile.

> **No conflict check needed**: DNS record identity is fully determined by the key
> (zone + name + type + set identifier). UPSERT replaces any existing record with
> matching identity. There is no separate "ownership" concept for DNS records.

### Import Handler

1. Parses `ref.ResourceID` into hosted zone ID, name, type, and optional set
   identifier.
2. Describes the record by these components.
3. Synthesizes a `DNSRecordSpec` from the observed state.
4. Commits state with `ModeObserved`.
5. Schedules reconciliation.

### Delete Handler

1. Sets status to `Deleting`.
2. **Re-describe before delete**: Route 53 DELETE requires the exact current record
   values. The driver describes the record immediately before deletion to handle
   any drift since last reconcile.
3. **Delete record**: Calls `api.DeleteRecord` with the freshly-observed state.
   - `IsRecordNotFound` or `IsInvalidChangeBatch` (record already gone) → silent success.
4. On success, sets status to `StatusDeleted`.

### Reconcile Handler

Standard 5-minute timer pattern:

1. Describes current record state from Route 53.
2. **Managed + drift**: UPSERTs the record with desired values (UPSERT is the
   convergence mechanism — it replaces the entire record set).
3. **Observed + drift**: Reports only.
4. Re-schedules.

> **Reconcile simplicity**: Unlike Security Group or Route Table drivers where
> convergence requires add-before-remove rule diffing, DNS record convergence is a
> single UPSERT call. The entire record set is replaced atomically.

---

## Step 7 — Provider Adapter

**File**: `internal/core/provider/route53record_adapter.go`

```go
type Route53RecordAdapter struct {
    accounts *auth.Registry
}

func (a *Route53RecordAdapter) Kind() string       { return "Route53Record" }
func (a *Route53RecordAdapter) Service() string    { return route53record.ServiceName }
func (a *Route53RecordAdapter) KeyScope() KeyScope  { return KeyScopeCustom }

func (a *Route53RecordAdapter) BuildKey(doc types.ResourceDocument) string {
    hostedZoneId := extractString(doc.Spec, "hostedZoneId")
    name := extractString(doc.Spec, "name")
    recordType := extractString(doc.Spec, "type")

    key := hostedZoneId + "~" + name + "~" + recordType

    if setId := extractString(doc.Spec, "setIdentifier"); setId != "" {
        key += "~" + setId
    }
    return key
}

func (a *Route53RecordAdapter) BuildImportKey(region, resourceID string) string {
    return strings.ReplaceAll(resourceID, "/", "~")
}
```

### Plan Method

The adapter's `Plan()` method reads the VO's stored outputs via `GetOutputs`. If
outputs exist (record has been provisioned), the adapter describes the record and
computes field diffs. If no outputs exist, it reports `OpCreate`.

---

## Step 8 — Registry Integration

**File**: `internal/core/provider/registry.go`

```go
r.Register(NewRoute53RecordAdapterWithRegistry(accounts))
```

---

## Step 9 — DNS Driver Pack Entry Point

See [ROUTE53_DRIVER_PACK_OVERVIEW.md](ROUTE53_DRIVER_PACK_OVERVIEW.md) §3.

---

## Step 10 — Docker Compose & Justfile

See [ROUTE53_DRIVER_PACK_OVERVIEW.md](ROUTE53_DRIVER_PACK_OVERVIEW.md) §7 and §8.

---

## Step 11 — Unit Tests

**File**: `internal/drivers/route53record/driver_test.go`

### Test Categories

| Category | Tests |
|---|---|
| Provision — A record (simple) | Create A record with TTL + values, verify UPSERT |
| Provision — AAAA record | Create AAAA record, verify IPv6 format |
| Provision — CNAME record | Create CNAME, verify single value |
| Provision — MX record | Create MX with priority prefix values |
| Provision — TXT record | Create TXT with quoted values |
| Provision — alias record | Create alias to ELB, verify no TTL |
| Provision — weighted routing | Create weighted records with set identifiers |
| Provision — latency routing | Create latency-based records with regions |
| Provision — failover routing | Create PRIMARY/SECONDARY pair |
| Provision — geolocation routing | Create geo-routed records |
| Provision — multivalue answer | Create multivalue records with health checks |
| Provision — with health check | Associate health check ID |
| Provision — update TTL | Change TTL, verify UPSERT |
| Provision — update records | Change record values, verify UPSERT |
| Provision — invalid input | Missing TTL for standard record → 400 |
| Provision — alias + TTL | Both present → 400 |
| Provision — no zone | NoSuchHostedZone → 404 |
| Import — by component parts | Describe record, synthesize spec |
| Delete — existing record | Re-describe + DELETE |
| Delete — already gone | Silent success |
| Reconcile — no drift | No changes, re-schedule |
| Reconcile — drift detected (managed) | UPSERT to correct |
| Reconcile — drift detected (observed) | Report only |
| GetStatus / GetOutputs | Return stored state |

**File**: `internal/drivers/route53record/drift_test.go`

| Test | Behavior |
|---|---|
| No drift — standard record | TTL + values match |
| No drift — alias record | Alias target matches |
| TTL changed | Reports TTL diff |
| Records changed | Reports record value diff |
| Alias target changed | Reports alias diff |
| Weight changed | Reports weight diff |
| Health check added/removed | Reports health check diff |
| Standard → alias switch | Reports type mismatch |

---

## Step 12 — Integration Tests

**File**: `tests/integration/route53_record_driver_test.go`

Uses Testcontainers + LocalStack. LocalStack supports Route 53 records.

### Test Scenarios

| Test | Flow |
|---|---|
| `TestRoute53Record_SimpleA` | Create zone → create A record → verify → update TTL → verify → delete |
| `TestRoute53Record_CNAME` | Create zone → create CNAME → verify → delete |
| `TestRoute53Record_Alias` | Create zone → create alias record → verify no TTL → delete |
| `TestRoute53Record_MX` | Create zone → create MX records → verify priority values → delete |
| `TestRoute53Record_TXT` | Create zone → create TXT record → verify quoting → delete |
| `TestRoute53Record_Weighted` | Create zone → create 2 weighted records → verify routing → delete |
| `TestRoute53Record_Failover` | Create zone → create health check → create primary/secondary → delete |
| `TestRoute53Record_Latency` | Create zone → create latency records for 2 regions → delete |
| `TestRoute53Record_Geolocation` | Create zone → create geo-routed records → delete |
| `TestRoute53Record_Import` | Create zone + record externally → import → verify → delete |
| `TestRoute53Record_Reconcile` | Create record → modify externally → reconcile → verify correction |
| `TestRoute53Record_UpdateValues` | Create A record → change IP addresses → verify UPSERT |
| `TestRoute53Record_HealthCheck` | Create zone + health check → create record with HC → verify |

---

## DNS-Record-Specific Design Decisions

### 1. UPSERT-Only Model

Route 53 supports `CREATE`, `UPSERT`, and `DELETE` actions in change batches. The
driver uses only `UPSERT` for creation and updates. `UPSERT` is idempotent — it
creates if absent, replaces if present. This eliminates the need for separate code
paths for first provision vs re-provision:

- First provision: UPSERT creates the record.
- Re-provision (update): UPSERT replaces the record.
- Reconcile correction: UPSERT replaces the record.

`CREATE` would fail if the record already exists, requiring error handling and retry
logic. `UPSERT` is strictly simpler.

### 2. Trailing Dot Normalization

Route 53 requires trailing dots on FQDNs (e.g., `www.example.com.`). The driver
normalizes all domain names by appending a trailing dot before AWS API calls. When
reading from AWS, the driver strips trailing dots for cleaner output and consistent
comparison. This prevents false drift detection.

### 3. Delete Requires Fresh Describe

Route 53's DELETE action requires the change batch to specify the exact current
record values and TTL. If the stored observed state is stale (the record was modified
externally since last reconcile), the delete will fail with `InvalidChangeBatch`.
The driver always re-describes the record immediately before deletion to ensure the
delete change batch matches current state.

### 4. Record Values as Strings

Record values are stored and compared as strings. The driver does not parse or
validate the format of record values (e.g., MX priority, TXT quoting) — it passes
values to AWS as-is and lets the API validate. This keeps the driver simple and
avoids duplicating AWS's validation logic for every record type.

### 5. Set Identifier in Key

Routing policy records (weighted, latency, failover, geolocation, multivalue) are
distinguished by their set identifier. Two records with the same name and type but
different set identifiers are different record sets in Route 53. The driver includes
set identifier in the Virtual Object key to model this correctly.

### 6. No Batch Operations

The driver manages one record set per Virtual Object. It does not batch multiple
record changes into a single change batch. This is consistent with the Praxis model
where each resource (record) has its own lifecycle and state. Batch optimization is
a future consideration for performance-sensitive deployments.

### 7. CNAME Exclusivity

AWS enforces that a CNAME record cannot coexist with any other record type at the
same name (except in alias mode). The driver does not enforce this cross-record
constraint — it relies on AWS's `InvalidChangeBatch` error, which maps to a terminal
400 error. Cross-record constraints are outside the scope of a single record driver.

---

## Design Decisions (Resolved)

| # | Decision | Resolution |
|---|---|---|
| 1 | Key format | `hostedZoneId~fqdn~type[~setIdentifier]` — `KeyScopeCustom` |
| 2 | Create vs UPSERT | UPSERT-only — simpler, idempotent |
| 3 | Ownership | None — DNS records don't support tags; natural identity prevents duplicates |
| 4 | Trailing dots | Driver normalizes internally in both directions |
| 5 | Delete change batch | Re-describe before delete to ensure consistency |
| 6 | Record value validation | Delegated to AWS API |
| 7 | Batch operations | One record per VO; no batching |
| 8 | Import format | `<hostedZoneId>/<fqdn>/<type>[/<setIdentifier>]` → converted to key |

---

## Checklist

### Schema
- [ ] `schemas/aws/route53/record.cue`

### Driver
- [ ] `internal/drivers/route53record/types.go`
- [ ] `internal/drivers/route53record/aws.go`
- [ ] `internal/drivers/route53record/drift.go`
- [ ] `internal/drivers/route53record/driver.go`
- [ ] `internal/drivers/route53record/driver_test.go`
- [ ] `internal/drivers/route53record/aws_test.go`
- [ ] `internal/drivers/route53record/drift_test.go`

### Adapter
- [ ] `internal/core/provider/route53record_adapter.go`
- [ ] `internal/core/provider/route53record_adapter_test.go`

### Registry
- [ ] Adapter registered in `NewRegistry()`

### Integration Tests
- [ ] `tests/integration/route53_record_driver_test.go`

### Infrastructure
- [ ] `cmd/praxis-dns/main.go` — `.Bind()` call
