# Route 53 Hosted Zone Driver — Implementation Plan

> **Status: IMPLEMENTED** — Driver is fully implemented with unit tests,
> integration tests, CUE schema, provider adapter, and registry integration.
>
> **Implementation note:** This plan references a `praxis-dns` driver pack.
> The actual implementation places the Hosted Zone driver in **`praxis-network`**
> (`cmd/praxis-network/main.go`).

> Target: A Restate Virtual Object driver that manages Route 53 Hosted Zones,
> providing full lifecycle management including creation, import, deletion, drift
> detection, and drift correction for zone properties, VPC associations (private
> zones), and tags.
>
> Key scope: `KeyScopeGlobal` — key format is `zoneName` (e.g., `example.com`),
> permanent and immutable for the lifetime of the Virtual Object. Route 53 is a
> global AWS service. The AWS-assigned hosted zone ID lives only in state/outputs.

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
16. [Hosted-Zone-Specific Design Decisions](#hosted-zone-specific-design-decisions)
17. [Design Decisions (Resolved)](#design-decisions-resolved)
18. [Checklist](#checklist)

---

## 1. Overview & Scope

The Hosted Zone driver manages the lifecycle of Route 53 **hosted zones** only.
It creates, imports, updates, and deletes hosted zones along with their comments,
VPC associations (for private hosted zones), and tags.

Hosted zones are the top-level container for DNS records. Every DNS record in
Route 53 exists within a hosted zone. In compound templates, the hosted zone is a
dependency of all DNS records — the DAG ensures zone creation before record creation.

**Out of scope**: DNS records (separate driver), health checks (separate driver),
delegation sets (advanced feature, deferred), query logging configurations, DNSSEC
signing. Each operates as a distinct resource type with its own lifecycle.

### Resource Scope for This Plan

| In Scope | Out of Scope (Separate Drivers) |
|---|---|
| Hosted zone creation (public and private) | DNS records |
| Zone comment/description | Health checks |
| VPC associations (private zones) | Delegation sets |
| Tags | DNSSEC signing |
| Import and drift detection | Query logging |
| CallerReference-based idempotency | Traffic policies |

### Driver Contract

| Handler | Context | Purpose |
|---|---|---|
| `Provision` | `ObjectContext` (exclusive) | Create or converge a hosted zone |
| `Import` | `ObjectContext` (exclusive) | Adopt an existing hosted zone |
| `Delete` | `ObjectContext` (exclusive) | Delete a hosted zone (must be empty of records) |
| `Reconcile` | `ObjectContext` (exclusive) | Detect/correct drift (report-only for Observed mode) |
| `GetStatus` | `ObjectSharedContext` (shared) | Return current status |
| `GetOutputs` | `ObjectSharedContext` (shared) | Return hosted zone outputs |

### Mutable vs Immutable Attributes

| Attribute | Mutability | Notes |
|---|---|---|
| `name` (domain name) | Immutable | Part of the Virtual Object key; cannot change after creation |
| `isPrivate` | Immutable | Public vs private is set at creation; cannot be changed |
| `comment` | Mutable | Updated via `UpdateHostedZoneComment` |
| `vpcs` (private zones) | Mutable | VPC associations added/removed via `AssociateVPCWithHostedZone` / `DisassociateVPCFromHostedZone` |
| `tags` | Mutable | Full replace via `ChangeTagsForResource` |

### Downstream Consumers

```
${resources.my-zone.outputs.hostedZoneId}    → DNS Record spec.hostedZoneId
${resources.my-zone.outputs.nameServers}     → Parent zone NS delegation records
${resources.my-zone.outputs.name}            → Alias record zone associations
```

---

## 2. Key Strategy

### Key Scope: `KeyScopeGlobal`

Route 53 is a global AWS service — hosted zone domain names are managed globally
within an account. The key is the zone's domain name (e.g., `example.com`).

> **Note**: AWS technically allows multiple hosted zones with the same domain name
> (distinguished by hosted zone ID). Praxis treats the domain name as the logical
> identity and uses caller reference-based idempotency to prevent duplicate creation.
> If a user needs multiple zones for the same domain, they must use different
> template resource names — but this is an edge case.

### BuildKey vs BuildImportKey

- **`BuildKey(resourceDoc)`**: Extracts `metadata.name` from the resource document.
  The `metadata.name` IS the domain name (e.g., `example.com`). Returns the domain
  name directly.

- **`BuildImportKey(region, resourceID)`**: Returns `resourceID`. For hosted zones,
  `resourceID` is the hosted zone ID (e.g., `Z1234567890ABC`). Import creates a VO
  keyed by the zone ID — this is a **different key** from template management (which
  uses the domain name). This matches the EC2/VPC import pattern where the AWS ID
  is used as the import key.

### CallerReference for Idempotent Creation

Route 53 `CreateHostedZone` requires a `CallerReference` — a unique string per
creation attempt. The driver uses the Restate Virtual Object key (the domain name)
as the caller reference. This ensures:

- Retries after crash/replay produce the same caller reference → AWS returns the
  existing zone instead of creating a duplicate.
- Different template resources with different names produce different caller
  references → distinct zones.

If `CreateHostedZone` is called with a previously-used caller reference AND the same
domain name, AWS returns the existing zone. If the caller reference matches but the
domain name differs, AWS returns a `HostedZoneAlreadyExists` error — the driver
surfaces this as a terminal 409 error.

### No Ownership Tags on the Zone

Hosted zones use caller reference-based idempotency. However, for import and
conflict detection, the driver tags zones with `praxis:managed-key=<key>` using
`ChangeTagsForResource`. This tag enables `FindByManagedKey` lookups for conflict
detection on first provision, consistent with the EC2/VPC pattern.

---

## 3. File Inventory

```text
✦ schemas/aws/route53/hosted_zone.cue                      — CUE schema for Route53HostedZone
✦ internal/drivers/route53zone/types.go                     — Spec, Outputs, ObservedState, State structs
✦ internal/drivers/route53zone/aws.go                       — HostedZoneAPI interface + realHostedZoneAPI
✦ internal/drivers/route53zone/drift.go                     — HasDrift(), ComputeFieldDiffs()
✦ internal/drivers/route53zone/driver.go                    — HostedZoneDriver Virtual Object
✦ internal/drivers/route53zone/driver_test.go               — Unit tests for driver (mocked AWS)
✦ internal/drivers/route53zone/aws_test.go                  — Unit tests for error classification
✦ internal/drivers/route53zone/drift_test.go                — Unit tests for drift detection
✦ internal/core/provider/route53zone_adapter.go             — HostedZoneAdapter implementing provider.Adapter
✦ internal/core/provider/route53zone_adapter_test.go        — Unit tests for adapter
✦ tests/integration/route53_hosted_zone_driver_test.go      — Integration tests
✦ cmd/praxis-dns/main.go                                    — DNS driver pack entry point (NEW pack)
✦ cmd/praxis-dns/Dockerfile                                 — Multi-stage Docker build
✎ internal/infra/awsclient/client.go                        — Add NewRoute53Client factory
✎ internal/core/provider/registry.go                        — Add NewRoute53HostedZoneAdapter to NewRegistry()
✎ docker-compose.yaml                                       — Add praxis-dns service on port 9086
✎ justfile                                                  — Add Route 53 build/test/register targets
```

---

## Step 1 — CUE Schema

**File**: `schemas/aws/route53/hosted_zone.cue`

```cue
package route53

#Route53HostedZone: {
    apiVersion: "praxis.io/v1"
    kind:       "Route53HostedZone"

    metadata: {
        // name is the domain name for the hosted zone (e.g., "example.com").
        // Must be a valid DNS domain name. Trailing dot is optional — the driver
        // normalizes to a trailing dot when calling the AWS API.
        name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9.-]{0,253}[a-zA-Z0-9]$"
        labels: [string]: string
    }

    spec: {
        // isPrivate determines whether this is a private hosted zone.
        // Private zones require at least one VPC association.
        // Immutable after creation.
        isPrivate: bool | *false

        // comment is a human-readable description of the hosted zone.
        // Updated via UpdateHostedZoneComment.
        comment?: string

        // vpcs is the list of VPCs to associate with a private hosted zone.
        // Required when isPrivate is true. Ignored for public zones.
        vpcs?: [...{
            // vpcId is the ID of the VPC to associate.
            vpcId: string

            // vpcRegion is the AWS region of the VPC.
            vpcRegion: string
        }]

        // tags applied to the hosted zone.
        tags: [string]: string
    }

    outputs?: {
        hostedZoneId: string
        name:         string
        nameServers:  [...string]
        isPrivate:    bool
        recordCount:  int
    }
}
```

### Key Design Decisions

- **`name` as domain name**: The metadata.name IS the DNS domain name. This is
  natural for users: `name: "example.com"` creates a zone for `example.com`. The
  driver appends a trailing dot when calling AWS APIs (Route 53 requires it).

- **`vpcs` as a list**: Private zones can be associated with multiple VPCs. Each
  entry specifies a VPC ID and region because VPC associations can span regions.
  Public zones ignore this field.

- **No `delegationSetId`**: Reusable delegation sets are an advanced feature for
  white-label DNS hosting. Deferred to a future enhancement.

- **No `forceDestroy`**: The driver requires the hosted zone to be empty (only NS
  and SOA records remain) before deletion. Automatic record cleanup on zone deletion
  is intentionally out of scope — the DAG scheduler should order record deletions
  before zone deletion.

---

## Step 2 — AWS Client Factory

**File**: `internal/infra/awsclient/client.go` — **NEEDS NEW ROUTE 53 CLIENT FACTORY**

Route 53 operations use the Route 53 SDK client, not the EC2 or IAM client.

```go
func NewRoute53Client(cfg aws.Config) *route53.Client {
    return route53.NewFromConfig(cfg)
}
```

This requires adding `github.com/aws/aws-sdk-go-v2/service/route53` to `go.mod`.

---

## Step 3 — Driver Types

**File**: `internal/drivers/route53zone/types.go`

```go
package route53zone

import "github.com/praxiscloud/praxis/pkg/types"

const ServiceName = "Route53HostedZone"

// VPCAssociation represents a VPC associated with a private hosted zone.
type VPCAssociation struct {
    VpcId     string `json:"vpcId"`
    VpcRegion string `json:"vpcRegion"`
}

// HostedZoneSpec is the desired state for a hosted zone.
type HostedZoneSpec struct {
    Account    string            `json:"account,omitempty"`
    Name       string            `json:"name"`
    IsPrivate  bool              `json:"isPrivate"`
    Comment    string            `json:"comment,omitempty"`
    VPCs       []VPCAssociation  `json:"vpcs,omitempty"`
    Tags       map[string]string `json:"tags,omitempty"`
    ManagedKey string            `json:"managedKey,omitempty"`
}

// HostedZoneOutputs is produced after provisioning and stored in Restate K/V.
type HostedZoneOutputs struct {
    HostedZoneId string   `json:"hostedZoneId"`
    Name         string   `json:"name"`
    NameServers  []string `json:"nameServers,omitempty"`
    IsPrivate    bool     `json:"isPrivate"`
    RecordCount  int64    `json:"recordCount"`
}

// ObservedState captures the actual configuration from AWS.
type ObservedState struct {
    HostedZoneId    string            `json:"hostedZoneId"`
    Name            string            `json:"name"`
    CallerReference string            `json:"callerReference"`
    Comment         string            `json:"comment"`
    IsPrivate       bool              `json:"isPrivate"`
    RecordCount     int64             `json:"recordCount"`
    NameServers     []string          `json:"nameServers"`
    VPCs            []VPCAssociation  `json:"vpcs"`
    Tags            map[string]string `json:"tags"`
}

// HostedZoneState is the single atomic state object stored under drivers.StateKey.
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
```

### Why These Fields

- **`CallerReference` in ObservedState**: Stored for reference and conflict detection.
  The driver uses the VO key as the caller reference, but imported zones may have
  arbitrary caller references.
- **`NameServers`**: Public zones get 4 name servers assigned by AWS. These are
  critical outputs — the user needs them to configure NS delegation in the parent zone.
  Private zones do not have publicly-queryable name servers.
- **`RecordCount`**: Useful for visibility and pre-deletion validation (zone must have
  ≤2 records — the NS and SOA records AWS auto-creates).

---

## Step 4 — AWS API Abstraction Layer

**File**: `internal/drivers/route53zone/aws.go`

### HostedZoneAPI Interface

```go
type HostedZoneAPI interface {
    // CreateHostedZone creates a new hosted zone.
    // Returns the hosted zone ID and name servers (public zones).
    CreateHostedZone(ctx context.Context, spec HostedZoneSpec) (string, []string, error)

    // DescribeHostedZone returns the observed state of a hosted zone.
    DescribeHostedZone(ctx context.Context, hostedZoneId string) (ObservedState, error)

    // DeleteHostedZone deletes a hosted zone (must be empty except NS/SOA).
    DeleteHostedZone(ctx context.Context, hostedZoneId string) error

    // UpdateComment updates the comment/description of a hosted zone.
    UpdateComment(ctx context.Context, hostedZoneId string, comment string) error

    // AssociateVPC associates a VPC with a private hosted zone.
    AssociateVPC(ctx context.Context, hostedZoneId string, vpc VPCAssociation) error

    // DisassociateVPC removes a VPC association from a private hosted zone.
    DisassociateVPC(ctx context.Context, hostedZoneId string, vpc VPCAssociation) error

    // ListVPCAssociations returns all VPC associations for a hosted zone.
    ListVPCAssociations(ctx context.Context, hostedZoneId string) ([]VPCAssociation, error)

    // UpdateTags replaces all user-managed tags on the hosted zone.
    UpdateTags(ctx context.Context, hostedZoneId string, tags map[string]string) error

    // FindByManagedKey searches for hosted zones tagged with
    // praxis:managed-key=managedKey.
    FindByManagedKey(ctx context.Context, managedKey string) (string, error)

    // FindByName searches for hosted zones matching the given domain name.
    // Used during import to resolve domain name → hosted zone ID.
    FindByName(ctx context.Context, name string, isPrivate bool) (string, error)
}
```

### realHostedZoneAPI Implementation

```go
type realHostedZoneAPI struct {
    client  *route53.Client
    limiter *ratelimit.Limiter
}

func NewHostedZoneAPI(client *route53.Client) HostedZoneAPI {
    return &realHostedZoneAPI{
        client:  client,
        limiter: ratelimit.New("route53", 5, 3),
    }
}
```

**Rate limiting**: Route 53 API has notably conservative rate limits — 5 requests
per second for most operations. The shared `"route53"` namespace prevents aggregate
throttling across the three Route 53 drivers.

### Key Implementation Details

#### `CreateHostedZone`

```go
func (r *realHostedZoneAPI) CreateHostedZone(ctx context.Context, spec HostedZoneSpec) (string, []string, error) {
    // Normalize domain name: ensure trailing dot
    name := spec.Name
    if !strings.HasSuffix(name, ".") {
        name = name + "."
    }

    input := &route53.CreateHostedZoneInput{
        Name:            aws.String(name),
        CallerReference: aws.String(spec.ManagedKey),
    }

    if spec.Comment != "" {
        input.HostedZoneConfig = &route53types.HostedZoneConfig{
            Comment:     aws.String(spec.Comment),
            PrivateZone: spec.IsPrivate,
        }
    } else if spec.IsPrivate {
        input.HostedZoneConfig = &route53types.HostedZoneConfig{
            PrivateZone: true,
        }
    }

    // Private zones require at least one VPC association at creation
    if spec.IsPrivate && len(spec.VPCs) > 0 {
        input.VPC = &route53types.VPC{
            VPCId:     aws.String(spec.VPCs[0].VpcId),
            VPCRegion: route53types.VPCRegion(spec.VPCs[0].VpcRegion),
        }
    }

    out, err := r.client.CreateHostedZone(ctx, input)
    if err != nil {
        return "", nil, err
    }

    hostedZoneId := extractHostedZoneId(aws.ToString(out.HostedZone.Id))

    var nameServers []string
    if out.DelegationSet != nil {
        nameServers = out.DelegationSet.NameServers
    }

    return hostedZoneId, nameServers, nil
}

// extractHostedZoneId strips the "/hostedzone/" prefix from the ID returned by AWS.
func extractHostedZoneId(id string) string {
    return strings.TrimPrefix(id, "/hostedzone/")
}
```

> **Private zone VPC limitation**: `CreateHostedZone` accepts only ONE VPC
> association. If the spec declares multiple VPCs, the driver creates the zone with
> the first VPC, then calls `AssociateVPC` for the remaining VPCs in subsequent
> `restate.Run` blocks.

#### `DescribeHostedZone`

```go
func (r *realHostedZoneAPI) DescribeHostedZone(ctx context.Context, hostedZoneId string) (ObservedState, error) {
    // 1. GetHostedZone — base zone attributes + delegation set
    out, err := r.client.GetHostedZone(ctx, &route53.GetHostedZoneInput{
        Id: aws.String(hostedZoneId),
    })
    if err != nil {
        return ObservedState{}, err
    }

    zone := out.HostedZone
    obs := ObservedState{
        HostedZoneId:    extractHostedZoneId(aws.ToString(zone.Id)),
        Name:            strings.TrimSuffix(aws.ToString(zone.Name), "."),
        CallerReference: aws.ToString(zone.CallerReference),
        IsPrivate:       zone.Config != nil && zone.Config.PrivateZone,
    }

    if zone.Config != nil && zone.Config.Comment != nil {
        obs.Comment = aws.ToString(zone.Config.Comment)
    }
    if zone.ResourceRecordSetCount != nil {
        obs.RecordCount = aws.ToInt64(zone.ResourceRecordSetCount)
    }

    // Name servers from delegation set (public zones)
    if out.DelegationSet != nil {
        obs.NameServers = out.DelegationSet.NameServers
    }

    // VPC associations (private zones)
    if len(out.VPCs) > 0 {
        obs.VPCs = make([]VPCAssociation, 0, len(out.VPCs))
        for _, vpc := range out.VPCs {
            obs.VPCs = append(obs.VPCs, VPCAssociation{
                VpcId:     aws.ToString(vpc.VPCId),
                VpcRegion: string(vpc.VPCRegion),
            })
        }
    }

    // 2. ListTagsForResource — tags
    tagOut, err := r.client.ListTagsForResource(ctx, &route53.ListTagsForResourceInput{
        ResourceId:   aws.String(hostedZoneId),
        ResourceType: route53types.TagResourceTypeHostedzone,
    })
    if err != nil {
        return ObservedState{}, fmt.Errorf("list tags for hosted zone %s: %w", hostedZoneId, err)
    }

    obs.Tags = make(map[string]string)
    if tagOut.ResourceTagSet != nil {
        for _, tag := range tagOut.ResourceTagSet.Tags {
            obs.Tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
        }
    }

    return obs, nil
}
```

> **API call count**: `DescribeHostedZone` makes 2 API calls: `GetHostedZone` (which
> includes VPC associations inline) + `ListTagsForResource`. Tags require a separate
> API call because `GetHostedZone` does not return them.

#### `DeleteHostedZone`

```go
func (r *realHostedZoneAPI) DeleteHostedZone(ctx context.Context, hostedZoneId string) error {
    _, err := r.client.DeleteHostedZone(ctx, &route53.DeleteHostedZoneInput{
        Id: aws.String(hostedZoneId),
    })
    return err
}
```

> **Pre-deletion requirement**: AWS requires the zone to contain only the auto-created
> NS and SOA records. If other records exist, `DeleteHostedZone` fails with
> `HostedZoneNotEmpty`. The driver surfaces this as a terminal error. The DAG
> scheduler should order DNS record deletions before hosted zone deletion.

#### `UpdateComment`

```go
func (r *realHostedZoneAPI) UpdateComment(ctx context.Context, hostedZoneId string, comment string) error {
    _, err := r.client.UpdateHostedZoneComment(ctx, &route53.UpdateHostedZoneCommentInput{
        Id:      aws.String(hostedZoneId),
        Comment: aws.String(comment),
    })
    return err
}
```

#### `AssociateVPC` / `DisassociateVPC`

```go
func (r *realHostedZoneAPI) AssociateVPC(ctx context.Context, hostedZoneId string, vpc VPCAssociation) error {
    _, err := r.client.AssociateVPCWithHostedZone(ctx, &route53.AssociateVPCWithHostedZoneInput{
        HostedZoneId: aws.String(hostedZoneId),
        VPC: &route53types.VPC{
            VPCId:     aws.String(vpc.VpcId),
            VPCRegion: route53types.VPCRegion(vpc.VpcRegion),
        },
    })
    return err
}

func (r *realHostedZoneAPI) DisassociateVPC(ctx context.Context, hostedZoneId string, vpc VPCAssociation) error {
    _, err := r.client.DisassociateVPCFromHostedZone(ctx, &route53.DisassociateVPCFromHostedZoneInput{
        HostedZoneId: aws.String(hostedZoneId),
        VPC: &route53types.VPC{
            VPCId:     aws.String(vpc.VpcId),
            VPCRegion: route53types.VPCRegion(vpc.VpcRegion),
        },
    })
    return err
}
```

> **VPC association constraint**: A private hosted zone cannot have zero VPC
> associations. The driver must ensure at least one VPC remains associated when
> removing VPCs. If the desired spec reduces VPCs, the driver adds new VPCs before
> removing old ones (add-before-remove pattern, consistent with SG rule updates).

#### `UpdateTags`

```go
func (r *realHostedZoneAPI) UpdateTags(ctx context.Context, hostedZoneId string, tags map[string]string) error {
    // Route 53 uses a single ChangeTagsForResource call that can add and remove
    // tags atomically.
    input := &route53.ChangeTagsForResourceInput{
        ResourceId:   aws.String(hostedZoneId),
        ResourceType: route53types.TagResourceTypeHostedzone,
    }

    // Get current tags to determine removals
    tagOut, err := r.client.ListTagsForResource(ctx, &route53.ListTagsForResourceInput{
        ResourceId:   aws.String(hostedZoneId),
        ResourceType: route53types.TagResourceTypeHostedzone,
    })
    if err != nil {
        return err
    }

    // Build removal list (keys present in current but not in desired)
    var removeKeys []string
    if tagOut.ResourceTagSet != nil {
        for _, tag := range tagOut.ResourceTagSet.Tags {
            key := aws.ToString(tag.Key)
            if strings.HasPrefix(key, "praxis:") {
                continue // preserve system tags
            }
            if _, ok := tags[key]; !ok {
                removeKeys = append(removeKeys, key)
            }
        }
    }

    // Build add list
    var addTags []route53types.Tag
    for k, v := range tags {
        if strings.HasPrefix(k, "praxis:") {
            continue
        }
        addTags = append(addTags, route53types.Tag{
            Key:   aws.String(k),
            Value: aws.String(v),
        })
    }

    if len(addTags) > 0 || len(removeKeys) > 0 {
        input.AddTags = addTags
        if len(removeKeys) > 0 {
            input.RemoveTagKeys = removeKeys
        }
        _, err = r.client.ChangeTagsForResource(ctx, input)
        return err
    }
    return nil
}
```

> **Atomic tag update**: Route 53's `ChangeTagsForResource` supports both additions
> and removals in a single call, unlike EC2 which requires separate `CreateTags` +
> `DeleteTags` calls.

#### `FindByManagedKey`

```go
func (r *realHostedZoneAPI) FindByManagedKey(ctx context.Context, managedKey string) (string, error) {
    // Route 53 does not support tag-based filtering on ListHostedZones.
    // We must list all zones and check tags individually.
    // This is expensive but only runs on first provision (no existing state).
    var zones []string

    paginator := route53.NewListHostedZonesPaginator(r.client, &route53.ListHostedZonesInput{})
    for paginator.HasMorePages() {
        page, err := paginator.NextPage(ctx)
        if err != nil {
            return "", err
        }
        for _, zone := range page.HostedZones {
            zoneId := extractHostedZoneId(aws.ToString(zone.Id))

            tagOut, err := r.client.ListTagsForResource(ctx, &route53.ListTagsForResourceInput{
                ResourceId:   aws.String(zoneId),
                ResourceType: route53types.TagResourceTypeHostedzone,
            })
            if err != nil {
                continue // skip zones we can't read tags for
            }
            if tagOut.ResourceTagSet != nil {
                for _, tag := range tagOut.ResourceTagSet.Tags {
                    if aws.ToString(tag.Key) == "praxis:managed-key" &&
                        aws.ToString(tag.Value) == managedKey {
                        zones = append(zones, zoneId)
                    }
                }
            }
        }
    }

    switch len(zones) {
    case 0:
        return "", nil
    case 1:
        return zones[0], nil
    default:
        return "", fmt.Errorf("ownership corruption: %d hosted zones tagged with praxis:managed-key=%s", len(zones), managedKey)
    }
}
```

> **Performance note**: `FindByManagedKey` is O(n) in the number of hosted zones
> because Route 53 does not support tag-based filtering on `ListHostedZones`. This
> is acceptable because it only runs on first provision (when the VO has no stored
> hosted zone ID). Subsequent provisions use the stored ID directly.

### Error Classification Helpers

```go
func IsNotFound(err error) bool {
    if err == nil {
        return false
    }
    var apiErr smithy.APIError
    if errors.As(err, &apiErr) {
        return apiErr.ErrorCode() == "NoSuchHostedZone"
    }
    return strings.Contains(err.Error(), "NoSuchHostedZone")
}

func IsAlreadyExists(err error) bool {
    if err == nil {
        return false
    }
    var apiErr smithy.APIError
    if errors.As(err, &apiErr) {
        code := apiErr.ErrorCode()
        return code == "HostedZoneAlreadyExists" || code == "ConflictingDomainExists"
    }
    return strings.Contains(err.Error(), "HostedZoneAlreadyExists") ||
        strings.Contains(err.Error(), "ConflictingDomainExists")
}

func IsNotEmpty(err error) bool {
    if err == nil {
        return false
    }
    var apiErr smithy.APIError
    if errors.As(err, &apiErr) {
        return apiErr.ErrorCode() == "HostedZoneNotEmpty"
    }
    return strings.Contains(err.Error(), "HostedZoneNotEmpty")
}

func IsVPCAssociationNotFound(err error) bool {
    if err == nil {
        return false
    }
    var apiErr smithy.APIError
    if errors.As(err, &apiErr) {
        return apiErr.ErrorCode() == "VPCAssociationNotFound"
    }
    return strings.Contains(err.Error(), "VPCAssociationNotFound")
}

func IsLastVPCAssociation(err error) bool {
    if err == nil {
        return false
    }
    var apiErr smithy.APIError
    if errors.As(err, &apiErr) {
        return apiErr.ErrorCode() == "LastVPCAssociation"
    }
    return strings.Contains(err.Error(), "LastVPCAssociation")
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
```

---

## Step 5 — Drift Detection

**File**: `internal/drivers/route53zone/drift.go`

### Core Functions

**`HasDrift(desired HostedZoneSpec, observed ObservedState) bool`**

```go
func HasDrift(desired HostedZoneSpec, observed ObservedState) bool {
    if desired.Comment != observed.Comment {
        return true
    }
    if desired.IsPrivate && !vpcAssociationsEqual(desired.VPCs, observed.VPCs) {
        return true
    }
    return !tagsMatch(desired.Tags, observed.Tags)
}
```

**`ComputeFieldDiffs(desired HostedZoneSpec, observed ObservedState) []FieldDiffEntry`**

Produces human-readable diffs:

- Immutable fields: `name`, `isPrivate` — reported with "(immutable, ignored)" suffix.
- Mutable scalar: `comment`.
- VPC associations (private zones): set-based diff (added, removed).
- Tags: per-key diffs (added, changed, removed).

### VPC Association Comparison

```go
func vpcAssociationsEqual(desired, observed []VPCAssociation) bool {
    if len(desired) != len(observed) {
        return false
    }
    dSet := make(map[string]bool, len(desired))
    for _, vpc := range desired {
        dSet[vpc.VpcId+"~"+vpc.VpcRegion] = true
    }
    for _, vpc := range observed {
        if !dSet[vpc.VpcId+"~"+vpc.VpcRegion] {
            return false
        }
    }
    return true
}
```

---

## Step 6 — Driver Implementation

**File**: `internal/drivers/route53zone/driver.go`

### Constructor Pattern

```go
func NewHostedZoneDriver(accounts *auth.Registry) *HostedZoneDriver
func NewHostedZoneDriverWithFactory(accounts *auth.Registry, factory func(aws.Config) HostedZoneAPI) *HostedZoneDriver
```

### Provision Handler

1. **Input validation**: `name` must be non-empty. If `isPrivate` is true, at least
   one VPC must be specified. Returns `TerminalError(400)` on failure.

2. **Load current state**: Reads `HostedZoneState` from Restate K/V. Sets status to
   `Provisioning`, increments generation.

3. **Re-provision check**: If `state.Outputs.HostedZoneId` is non-empty, describes
   the zone. If deleted externally (404), clears ID and falls through to creation.

4. **Conflict check**: On first provision, calls `FindByManagedKey` to check for
   ownership conflicts. Returns `TerminalError(409)` if conflict found.

5. **Create zone**: Calls `api.CreateHostedZone` inside `restate.Run`. Classifies:
   - `IsAlreadyExists` → `TerminalError(409)`
   - `IsPriorRequestNotComplete` → retryable (non-terminal)

6. **Tag the zone**: Applies `praxis:managed-key` tag + user tags via `UpdateTags`.

7. **Associate additional VPCs**: If private zone with >1 VPC, calls `AssociateVPC`
   for VPCs[1:] (first VPC was associated at creation).

8. **Re-provision path — converge mutable attributes**:
   - Comment: compare, update if changed.
   - VPCs (private): add-before-remove reconciliation.
   - Tags: update if changed.

9. **Describe final state**: Calls `api.DescribeHostedZone`.

10. **Commit state**: Sets status to `Ready`, saves atomically, schedules reconcile.

### Import Handler

1. Describes the zone by `ref.ResourceID` (the hosted zone ID).
2. Synthesizes a `HostedZoneSpec` from the observed state via `specFromObserved()`.
3. Commits state with observed as both desired baseline and snapshot.
4. Sets mode to `ModeObserved` (DNS is critical infrastructure).
5. Schedules reconciliation.

### Delete Handler

1. Sets status to `Deleting`.
2. **Pre-deletion check**: Describes zone to check record count. If record count > 2
   (NS + SOA), returns terminal error directing user to delete records first.
3. **Disassociate VPCs** (private zones): Removes all VPC associations except the
   last one (Route 53 requires at least one during the zone's lifetime; deleting
   the zone removes the final association).
4. **Delete zone**: Calls `api.DeleteHostedZone`. Classifies:
   - `IsNotEmpty` → `TerminalError(409)` with message about remaining records.
   - `IsNotFound` → silent success (already gone).
5. On success, sets status to `StatusDeleted`.

### Reconcile Handler

Standard 5-minute timer pattern:

1. Describes current AWS state (zone + tags + VPC associations).
2. **Managed + drift**: Corrects comment, VPC associations, tags.
3. **Observed + drift**: Reports only.
4. Re-schedules.

---

## Step 7 — Provider Adapter

**File**: `internal/core/provider/route53zone_adapter.go`

```go
type Route53HostedZoneAdapter struct {
    accounts *auth.Registry
}

func (a *Route53HostedZoneAdapter) Kind() string            { return "Route53HostedZone" }
func (a *Route53HostedZoneAdapter) ServiceName() string     { return route53zone.ServiceName }
func (a *Route53HostedZoneAdapter) Scope() KeyScope          { return KeyScopeGlobal }

func (a *Route53HostedZoneAdapter) BuildKey(doc json.RawMessage) (string, error) {
    return doc.Metadata.Name
}

func (a *Route53HostedZoneAdapter) BuildImportKey(region, resourceID string) (string, error) {
    return resourceID, nil
}

func (a *Route53HostedZoneAdapter) DecodeSpec(doc json.RawMessage) (any, error) {
    spec := route53zone.HostedZoneSpec{
        Name:       doc.Metadata.Name,
        ManagedKey: doc.Metadata.Name,
    }
    // Extract fields from doc.Spec map...
    return spec, nil
}
```

### Plan Method

The adapter's `Plan()` method reads the VO's stored outputs via `GetOutputs`. If a
hosted zone ID exists, it describes the zone by ID and computes field diffs. If no
outputs exist (new resource), it reports `OpCreate`.

---

## Step 8 — Registry Integration

**File**: `internal/core/provider/registry.go`

```go
// Added to NewRegistryWithAdapters() call:
NewRoute53HostedZoneAdapterWithRegistry(accounts),
```

---

## Step 9 — DNS Driver Pack Entry Point

See [ROUTE53_DRIVER_PACK_OVERVIEW.md](ROUTE53_DRIVER_PACK_OVERVIEW.md) §3 for the
full `cmd/praxis-dns/main.go`.

---

## Step 10 — Docker Compose & Justfile

See [ROUTE53_DRIVER_PACK_OVERVIEW.md](ROUTE53_DRIVER_PACK_OVERVIEW.md) §7 and §8.

---

## Step 11 — Unit Tests

**File**: `internal/drivers/route53zone/driver_test.go`

### Test Categories

| Category | Tests |
|---|---|
| Provision — create public zone | Happy path, returns hosted zone ID + name servers |
| Provision — create private zone | With VPC association, validates VPC required |
| Provision — create private zone multi-VPC | Creates with first VPC, associates remaining |
| Provision — idempotent retry | Same caller reference returns existing zone |
| Provision — conflict detection | FindByManagedKey returns existing → 409 |
| Provision — re-provision (update comment) | Detects drift and updates |
| Provision — re-provision (add/remove VPCs) | Add-before-remove VPC reconciliation |
| Provision — re-provision (update tags) | Tag diff and reconcile |
| Provision — externally deleted | Detects 404, recreates zone |
| Import — by zone ID | Describes zone, synthesizes spec, sets Observed mode |
| Delete — empty zone | Deletes successfully |
| Delete — non-empty zone | Returns terminal error: records must be deleted first |
| Delete — private zone with VPCs | Disassociates VPCs before deletion |
| Delete — already gone | Silent success on 404 |
| Reconcile — no drift | Observes no changes, re-schedules |
| Reconcile — drift detected (managed) | Corrects comment, VPCs, tags |
| Reconcile — drift detected (observed) | Reports only, does not mutate |
| GetStatus / GetOutputs | Returns stored state |

**File**: `internal/drivers/route53zone/drift_test.go`

| Test | Behavior |
|---|---|
| No drift | Identical desired and observed |
| Comment changed | Reports comment diff |
| VPC added externally | Reports unexpected VPC association |
| VPC removed externally | Reports missing VPC association |
| Tag added/changed/removed | Reports per-key tag diffs |

---

## Step 12 — Integration Tests

**File**: `tests/integration/route53_hosted_zone_driver_test.go`

Uses Testcontainers + LocalStack. LocalStack supports Route 53 in the free tier.

### Test Scenarios

| Test | Flow |
|---|---|
| `TestRoute53HostedZone_ProvisionPublic` | Create public zone → verify name servers → verify tags → delete |
| `TestRoute53HostedZone_ProvisionPrivate` | Create VPC → create private zone with VPC → verify association → delete |
| `TestRoute53HostedZone_ProvisionPrivateMultiVPC` | Create 2 VPCs → create zone with both → verify associations → delete |
| `TestRoute53HostedZone_Import` | Create zone externally → import → verify observed state → delete |
| `TestRoute53HostedZone_UpdateComment` | Create zone → update comment → verify drift correction |
| `TestRoute53HostedZone_UpdateVPCs` | Create private zone → add/remove VPC → verify convergence |
| `TestRoute53HostedZone_UpdateTags` | Create zone → modify tags → verify drift correction |
| `TestRoute53HostedZone_Reconcile` | Create zone → modify externally → reconcile → verify correction |
| `TestRoute53HostedZone_DeleteNonEmpty` | Create zone → add record → attempt delete → verify error |

---

## Hosted-Zone-Specific Design Decisions

### 1. Domain Name Normalization

Route 53 requires a trailing dot on domain names (e.g., `example.com.`). The driver
accepts both `example.com` and `example.com.` from user templates and normalizes to
trailing dot before calling AWS APIs. Observed state strips the trailing dot for
cleaner output. This prevents false drift detection from trailing dot inconsistencies.

### 2. Private Zone VPC Ordering

`CreateHostedZone` accepts only one VPC. For multi-VPC private zones, the driver
creates with VPC[0] and then associates VPC[1..n] in subsequent calls. This is
consistent with AWS's API design. VPC convergence uses add-before-remove to ensure
the zone always has at least one association.

### 3. CallerReference Reuse

If a provision attempt creates a zone but the process crashes before committing
state, the retry will use the same caller reference. AWS returns the existing zone
instead of creating a duplicate. The driver checks the returned zone's domain name
matches the spec — if it doesn't (caller reference collision from a different
previous zone), it returns a terminal error.

### 4. Delegation Sets

Reusable delegation sets allow consistent name server assignments across zones.
This is valuable for white-label DNS. Deferred to a future enhancement — the
current driver uses AWS's default delegation set assignment.

### 5. DNSSEC

DNSSEC signing and key management add significant complexity. Deferred to a future
enhancement. The driver does not create or manage KMS keys for DNSSEC.

---

## Design Decisions (Resolved)

| # | Decision | Resolution |
|---|---|---|
| 1 | Key format | `zoneName` (domain name) — global, no region prefix |
| 2 | Import key | Hosted zone ID — different VO from template management |
| 3 | Conflict detection | CallerReference + managed-key tag |
| 4 | VPC add-before-remove | Yes — ensures at least one VPC association at all times |
| 5 | Zone deletion with records | Terminal error — DAG must order record deletion first |
| 6 | Delegation sets | Deferred — use AWS default |
| 7 | DNSSEC | Deferred |
| 8 | Trailing dot normalization | Driver handles internally; user can use either form |

---

## Checklist

### Schema
- [x] `schemas/aws/route53/hosted_zone.cue`

### Driver
- [x] `internal/drivers/route53zone/types.go`
- [x] `internal/drivers/route53zone/aws.go`
- [x] `internal/drivers/route53zone/drift.go`
- [x] `internal/drivers/route53zone/driver.go`
- [x] `internal/drivers/route53zone/driver_test.go`
- [x] `internal/drivers/route53zone/aws_test.go`
- [x] `internal/drivers/route53zone/drift_test.go`

### Adapter
- [x] `internal/core/provider/route53zone_adapter.go`
- [x] `internal/core/provider/route53zone_adapter_test.go`

### Registry
- [x] Adapter registered in `NewRegistry()`

### Integration Tests
- [x] `tests/integration/route53_hosted_zone_driver_test.go`

### Infrastructure
- [x] `internal/infra/awsclient/client.go` — `NewRoute53Client`
- [x] `cmd/praxis-dns/main.go` — `.Bind()` call
- [x] `cmd/praxis-dns/Dockerfile`
