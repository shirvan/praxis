# VPC Driver — Implementation Plan

> **Status: NOT STARTED** — This plan is a comprehensive blueprint for implementing
> the VPC driver. All code snippets are reference implementations; the actual code
> should follow these patterns precisely.

> Target: A Restate Virtual Object driver that manages AWS VPCs, following the
> exact patterns established by the S3 Bucket, Security Group, and EC2 Instance
> drivers.
>
> Key scope: `KeyScopeRegion` — key format is `region~metadata.name`, permanent and
> immutable for the lifetime of the Virtual Object. The AWS-assigned VPC ID
> lives only in state/outputs.

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
12. [Step 9 — Binary Entry Point & Dockerfile](#step-9--binary-entry-point--dockerfile)
13. [Step 10 — Docker Compose & Justfile](#step-10--docker-compose--justfile)
14. [Step 11 — Unit Tests](#step-11--unit-tests)
15. [Step 12 — Integration Tests](#step-12--integration-tests)
16. [VPC-Specific Design Decisions](#vpc-specific-design-decisions)
17. [Design Decisions (Resolved)](#design-decisions-resolved)
18. [Checklist](#checklist)

---

## 1. Overview & Scope

The VPC driver manages the lifecycle of AWS **VPCs** only. Subnets, route tables,
internet gateways, NAT gateways, VPC peering connections, and network ACLs are
listed in the FUTURE.md roster under the VPC driver service umbrella but should be
implemented as **separate drivers** (separate Virtual Objects, separate binaries,
separate CUE schemas) — not rolled into this plan. This plan focuses exclusively on
the VPC resource itself.

### Why VPCs First

VPC is the foundational networking primitive in AWS. Almost every other resource
depends on a VPC — EC2 instances launch into VPC subnets, security groups are
scoped to a VPC, RDS instances require VPC subnet groups, ELBs target VPC subnets.
No compound template can provision a realistic application stack without a VPC.
Implementing the VPC driver unblocks the entire networking layer and makes it
possible to compose end-to-end stacks that don't rely on pre-existing VPC
infrastructure.

### Resource Scope for This Plan

| In Scope | Out of Scope (Separate Drivers) |
|---|---|
| VPC creation, update, deletion | Subnets |
| DNS settings (enableDnsHostnames, enableDnsSupport) | Route tables |
| Instance tenancy (at creation) | Internet gateways |
| Primary CIDR block | NAT gateways |
| Tags | VPC peering connections |
| Import and drift detection | Network ACLs |
| Ownership tag enforcement | Secondary CIDR blocks |
| | IPv6 CIDR association |

### VPC Driver Contract

The driver follows the established Virtual Object contract:

| Handler | Context | Purpose |
|---|---|---|
| `Provision` | `ObjectContext` (exclusive) | Create or converge a VPC |
| `Import` | `ObjectContext` (exclusive) | Adopt an existing VPC |
| `Delete` | `ObjectContext` (exclusive) | Delete a VPC (blocked for Observed mode) |
| `Reconcile` | `ObjectContext` (exclusive) | Detect/correct drift (report-only for Observed mode) |
| `GetStatus` | `ObjectSharedContext` (shared) | Return current status |
| `GetOutputs` | `ObjectSharedContext` (shared) | Return VPC outputs |

### Downstream Consumers

The VPC driver's outputs are the primary integration point for dependent resources:

```
${resources.my-vpc.outputs.vpcId}       → Subnets, Security Groups, ELB, RDS
${resources.my-vpc.outputs.cidrBlock}   → Subnet CIDR planning, Network ACLs
${resources.my-vpc.outputs.arn}         → IAM policies
```

---

## 2. Key Strategy

### Key Format: `region~metadata.name`

The Virtual Object key is always `region~metadata.name`. This matches the pattern
established by the EC2 driver and the way the pipeline wiring works:

1. **BuildKey** (adapter, plan-time): returns `region~metadata.name`.
2. **Provision** (pipeline → workflow → driver): dispatched to same key.
3. **Delete** (pipeline → workflow → driver): dispatched to same key.
4. **Plan** (adapter → describe by VPC ID from state): uses the key to reach
   the Virtual Object, reads the stored VPC ID from state, describes by ID.
5. **Import** (handlers_resource.go): `BuildImportKey(region, resourceID)` returns
   `region~resourceID` where `resourceID` is the VPC ID — **this targets a
   different Virtual Object** intentionally (same as EC2).

The AWS VPC ID (`vpc-0abc123...`) is stored only in `VPCState.Outputs.VpcId` and
`VPCState.Observed.VpcId`. It is the AWS API handle, not the Praxis identity handle.

### Constraint: metadata.name Must Be Unique Within a Region

VPC names (Name tags) are not unique in AWS — multiple VPCs can share the same
Name tag within a region. Praxis requires `metadata.name` to be region-unique for
managed VPC resources. This is consistent with the EC2 driver's naming constraint
and simpler than requiring users to embed VPC IDs (which are unknown at plan time)
in their template names.

If a user wants two VPCs with similar purposes in the same region, they should
use different template resource names (e.g., "production-vpc", "staging-vpc").

### Conflict Enforcement via Ownership Tags

The driver enforces ownership at provisioning time using an AWS resource tag,
following the EC2 driver pattern:

- **Tag written at creation**: every `CreateVpc` call adds the tag
  `praxis:managed-key = <region~metadata.name>` to the VPC in addition
  to any user-declared tags. This tag is immutable from Praxis's perspective
  (never removed, never overwritten by drift correction).

- **Pre-flight conflict check**: when `Provision` runs with no existing VO
  state (first provision), it calls `FindByManagedKey` to search for any
  VPC already tagged with `praxis:managed-key = <this key>`. If found,
  `Provision` returns a terminal error (status 409):
  `"vpc name 'X' in region Y is already managed by Praxis (vpc-0abc123)"`.

- **`FindByManagedKey(ctx, managedKey) (string, error)`** is added to the
  `VPCAPI` interface. It queries by tag filter and returns:
  - `("", nil)` if no VPCs match (safe to create),
  - `(vpcId, nil)` if exactly one matches (conflict/recovery target),
  - `("", error)` if more than one matches (ownership corruption — terminal error).

### Import Semantics: Separate Lifecycle Track

Import and template-based management produce **separate Virtual Objects** for the
same AWS VPC when the VPC was not originally provisioned by Praxis:

- `praxis import --kind VPC --region us-east-1 --resource-id vpc-0abc123`:
  Creates VO key `us-east-1~vpc-0abc123`. The import handler describes the VPC,
  synthesizes a spec, and manages it under that key going forward.

- Template with `metadata.name: production-vpc` in `us-east-1`:
  Creates VO key `us-east-1~production-vpc`. A brand-new lifecycle.

Import defaults to `ModeObserved` for VPCs. While VPC deletion is less immediately
destructive than EC2 termination (no data loss), deleting a VPC that has active
subnets and instances causes cascading failures. The Observed default prevents the
import VO from participating in destructive lifecycle actions unless the operator
explicitly opts in with `--mode managed`.

### Plan-Time VPC Resolution

The plan-time resolution follows the EC2 adapter's state-driven pattern:

1. **Preferred path**: The adapter's `Plan()` method reads the Virtual Object's
   stored state via `GetOutputs` (a shared handler, non-blocking). If the VO has
   outputs with a `vpcId`, describe that specific VPC by ID.

2. **Fallback for new resources**: If `GetOutputs` returns empty (no VPC
   provisioned yet), the plan reports `OpCreate`. No AWS describe needed.

3. **No `FindVpcByName`**: VPC Name tags are mutable and non-unique, just like
   EC2. Plan-time resolution uses the stored VPC ID, not a name-tag search.

---

## 3. File Inventory

Create or modify these files (✦ = new file, ✎ = modify existing):

```
✦ internal/drivers/vpc/types.go             — Spec, Outputs, ObservedState, State structs
✦ internal/drivers/vpc/aws.go               — VPCAPI interface + realVPCAPI implementation
✦ internal/drivers/vpc/drift.go             — HasDrift(), ComputeFieldDiffs()
✦ internal/drivers/vpc/driver.go            — VPCDriver Virtual Object
✦ internal/drivers/vpc/driver_test.go       — Unit tests for driver (mocked AWS)
✦ internal/drivers/vpc/aws_test.go          — Unit tests for error classification helpers
✦ internal/drivers/vpc/drift_test.go        — Unit tests for drift detection
✦ internal/core/provider/vpc_adapter.go     — VPCAdapter implementing provider.Adapter
✦ internal/core/provider/vpc_adapter_test.go — Unit tests for VPC adapter
✦ schemas/aws/vpc/vpc.cue                   — CUE schema for VPC resource
✦ tests/integration/vpc_driver_test.go      — Integration tests (Testcontainers + LocalStack)
✎ cmd/praxis-network/main.go               — Add VPC driver `.Bind()` to network pack
✎ internal/core/provider/registry.go        — Add NewVPCAdapter to NewRegistry()
✎ docker-compose.yaml                       — No change needed (VPC joins existing praxis-network service)
✎ justfile                                  — Add vpc test targets
```

---

## Step 1 — CUE Schema

**File**: `schemas/aws/vpc/vpc.cue`

This defines the shape of a `VPC` resource document. The template engine
validates user templates against this schema before dispatch.

```cue
package vpc

#VPC: {
    apiVersion: "praxis.io/v1"
    kind:       "VPC"

    metadata: {
        name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9._-]{0,254}$"
        labels: [string]: string
    }

    spec: {
        // region is the AWS region to create the VPC in.
        region: string

        // cidrBlock is the primary IPv4 CIDR block for the VPC.
        // Must be a valid CIDR in the range /16 to /28.
        // Immutable after creation — changing this requires VPC replacement.
        cidrBlock: string & =~"^([0-9]{1,3}\\.){3}[0-9]{1,3}/([0-9]|[12][0-9]|3[0-2])$"

        // enableDnsHostnames controls whether instances in the VPC receive
        // public DNS hostnames. Requires enableDnsSupport to be true.
        // Default: false for non-default VPCs.
        enableDnsHostnames: bool | *false

        // enableDnsSupport controls whether DNS resolution is supported in the VPC.
        // Default: true.
        enableDnsSupport: bool | *true

        // instanceTenancy defines the default tenancy for instances launched
        // into this VPC. Once set to "dedicated", it cannot be changed back
        // to "default" (AWS restriction).
        // - "default": instances launch on shared hardware (most common).
        // - "dedicated": instances launch on single-tenant hardware (higher cost).
        // Immutable after creation.
        instanceTenancy: "default" | "dedicated" | *"default"

        // tags applied to the VPC resource.
        tags: [string]: string
    }

    outputs?: {
        vpcId:              string
        arn:                string
        cidrBlock:          string
        state:              string
        enableDnsHostnames: bool
        enableDnsSupport:   bool
        instanceTenancy:    string
        ownerId:            string
        dhcpOptionsId:      string
        isDefault:          bool
    }
}
```

**Key decisions**:
- `cidrBlock` uses a regex to validate CIDR format — catches typos before hitting AWS.
  AWS enforces the /16–/28 range server-side; the regex validates format only.
- `instanceTenancy` only exposes `default` and `dedicated`. The `host` tenancy option
  is intentionally omitted — it requires Dedicated Hosts to be pre-configured, which
  adds complexity beyond the scope of a VPC driver. It can be added later if needed.
- `enableDnsHostnames` defaults to `false` and `enableDnsSupport` defaults to `true`,
  matching AWS non-default VPC behavior.
- No `secondaryCidrBlocks` field — see Design Decisions §4.
- No IPv6 fields — see Design Decisions §5.

---

## Step 2 — AWS Client Factory

**File**: `internal/infra/awsclient/client.go` — **NO CHANGES NEEDED**

The existing `NewEC2Client(cfg aws.Config) *ec2.Client` function already exists and
is used by both the Security Group and EC2 Instance drivers. VPC API calls use the
same EC2 SDK client. The VPC driver will reuse `NewEC2Client`.

---

## Step 3 — Driver Types

**File**: `internal/drivers/vpc/types.go`

Define all the data structures the driver uses. Follow the S3/SG/EC2 pattern exactly:
one package-level constant for `ServiceName`, typed spec/outputs/observed/state structs.

```go
package vpc

import "github.com/praxiscloud/praxis/pkg/types"

// ServiceName is the Restate Virtual Object name for VPCs.
// This becomes the URL path component (e.g., /VPC/<key>/Provision).
const ServiceName = "VPC"

// VPCSpec is the desired state for a VPC.
// Fields map to the #VPC CUE schema in schemas/aws/vpc/vpc.cue.
type VPCSpec struct {
    Account            string            `json:"account,omitempty"`
    Region             string            `json:"region"`
    CidrBlock          string            `json:"cidrBlock"`
    EnableDnsHostnames bool              `json:"enableDnsHostnames"`
    EnableDnsSupport   bool              `json:"enableDnsSupport"`
    InstanceTenancy    string            `json:"instanceTenancy,omitempty"`
    Tags               map[string]string `json:"tags,omitempty"`
    // ManagedKey is the Restate Virtual Object key (region~metadata.name).
    // Set by the adapter before dispatch; written as praxis:managed-key tag at creation.
    // Never stored in user-facing YAML, never validated by CUE.
    ManagedKey         string            `json:"managedKey,omitempty"`
}

// VPCOutputs is produced after provisioning and stored in Restate K/V.
// Dependent resources reference these via CEL (e.g., "${ resources.my-vpc.outputs.vpcId }").
type VPCOutputs struct {
    VpcId              string `json:"vpcId"`
    ARN                string `json:"arn,omitempty"`
    CidrBlock          string `json:"cidrBlock"`
    State              string `json:"state"`
    EnableDnsHostnames bool   `json:"enableDnsHostnames"`
    EnableDnsSupport   bool   `json:"enableDnsSupport"`
    InstanceTenancy    string `json:"instanceTenancy"`
    OwnerId            string `json:"ownerId"`
    DhcpOptionsId      string `json:"dhcpOptionsId"`
    IsDefault          bool   `json:"isDefault"`
}

// ObservedState captures the actual configuration of a VPC from AWS Describe calls.
type ObservedState struct {
    VpcId              string            `json:"vpcId"`
    CidrBlock          string            `json:"cidrBlock"`
    State              string            `json:"state"` // "pending", "available"
    EnableDnsHostnames bool              `json:"enableDnsHostnames"`
    EnableDnsSupport   bool              `json:"enableDnsSupport"`
    InstanceTenancy    string            `json:"instanceTenancy"`
    OwnerId            string            `json:"ownerId"`
    DhcpOptionsId      string            `json:"dhcpOptionsId"`
    IsDefault          bool              `json:"isDefault"`
    Tags               map[string]string `json:"tags"`
}

// VPCState is the single atomic state object stored under drivers.StateKey.
// All fields written together in one restate.Set() call.
type VPCState struct {
    Desired            VPCSpec              `json:"desired"`
    Observed           ObservedState        `json:"observed"`
    Outputs            VPCOutputs           `json:"outputs"`
    Status             types.ResourceStatus `json:"status"`
    Mode               types.Mode           `json:"mode"`
    Error              string               `json:"error,omitempty"`
    Generation         int64                `json:"generation"`
    LastReconcile      string               `json:"lastReconcile,omitempty"`
    ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
```

**Why these fields**:
- `Account` is passed through from the adapter for credential resolution (same as all other drivers).
- `InstanceTenancy` uses `omitempty` because the default value `"default"` is the common case —
  empty means "default" tenancy.
- `ObservedState.State` tracks the VPC state machine (`pending`, `available`).
- `ObservedState.IsDefault` distinguishes the default VPC (one per region) — the driver should
  protect against accidental deletion of the default VPC.
- `OwnerId` is the AWS account ID owning the VPC — useful for cross-account visibility.
- `DhcpOptionsId` is included for completeness but not managed by this driver.

---

## Step 4 — AWS API Abstraction Layer

**File**: `internal/drivers/vpc/aws.go`

This file defines the `VPCAPI` interface and its real implementation (`realVPCAPI`).
The interface is what gets mocked in unit tests. All methods take `context.Context`
(not `restate.RunContext`) — the driver wraps calls in `restate.Run()`.

### VPCAPI Interface

```go
package vpc

import (
    "context"
)

// VPCAPI abstracts the AWS EC2 SDK operations that the VPC driver uses.
// All methods receive a plain context.Context, NOT a restate.RunContext.
// The caller in driver.go wraps these calls inside restate.Run().
type VPCAPI interface {
    // CreateVpc creates a new VPC with the given spec.
    // Returns the VPC ID assigned by AWS.
    CreateVpc(ctx context.Context, spec VPCSpec) (string, error)

    // DescribeVpc returns the full observed state of a VPC.
    DescribeVpc(ctx context.Context, vpcId string) (ObservedState, error)

    // DeleteVpc deletes a VPC.
    // Fails if the VPC has dependent resources (subnets, IGWs, etc.).
    DeleteVpc(ctx context.Context, vpcId string) error

    // WaitUntilAvailable blocks until the VPC reaches "available" state.
    WaitUntilAvailable(ctx context.Context, vpcId string) error

    // ModifyDnsHostnames enables or disables DNS hostnames for the VPC.
    ModifyDnsHostnames(ctx context.Context, vpcId string, enabled bool) error

    // ModifyDnsSupport enables or disables DNS support for the VPC.
    ModifyDnsSupport(ctx context.Context, vpcId string, enabled bool) error

    // UpdateTags replaces all user-managed tags on the VPC.
    // Does NOT modify praxis:* system tags.
    UpdateTags(ctx context.Context, vpcId string, tags map[string]string) error

    // FindByManagedKey searches for VPCs tagged with
    // praxis:managed-key=managedKey.
    //
    // Return semantics:
    //   - ("", nil):       no match — safe to create.
    //   - (vpcId, nil):    exactly one match — conflict or recovery target.
    //   - ("", error):     more than one match → terminal ownership-corruption
    //                      error, or an AWS API failure.
    FindByManagedKey(ctx context.Context, managedKey string) (string, error)
}
```

### realVPCAPI Implementation

```go
type realVPCAPI struct {
    client  *ec2sdk.Client
    limiter *ratelimit.Limiter
}

func NewVPCAPI(client *ec2sdk.Client) VPCAPI {
    return &realVPCAPI{
        client:  client,
        limiter: ratelimit.New("vpc", 20, 10),
    }
}
```

### Key Implementation Details for Each Method

#### `CreateVpc`

```go
func (r *realVPCAPI) CreateVpc(ctx context.Context, spec VPCSpec) (string, error) {
    input := &ec2sdk.CreateVpcInput{
        CidrBlock: aws.String(spec.CidrBlock),
    }

    // Set instance tenancy if not the default.
    if spec.InstanceTenancy != "" && spec.InstanceTenancy != "default" {
        input.InstanceTenancy = ec2types.Tenancy(spec.InstanceTenancy)
    }

    // Apply tags at creation to the VPC.
    // Always include the praxis:managed-key ownership tag (see Design Decisions §1).
    ec2Tags := []ec2types.Tag{{
        Key:   aws.String("praxis:managed-key"),
        Value: aws.String(spec.ManagedKey),
    }}
    for k, v := range spec.Tags {
        ec2Tags = append(ec2Tags, ec2types.Tag{
            Key: aws.String(k), Value: aws.String(v),
        })
    }
    input.TagSpecifications = []ec2types.TagSpecification{{
        ResourceType: ec2types.ResourceTypeVpc,
        Tags:         ec2Tags,
    }}

    out, err := r.client.CreateVpc(ctx, input)
    if err != nil {
        return "", err
    }
    if out.Vpc == nil {
        return "", fmt.Errorf("CreateVpc returned nil VPC")
    }
    return aws.ToString(out.Vpc.VpcId), nil
}
```

#### `DescribeVpc`

```go
func (r *realVPCAPI) DescribeVpc(ctx context.Context, vpcId string) (ObservedState, error) {
    out, err := r.client.DescribeVpcs(ctx, &ec2sdk.DescribeVpcsInput{
        VpcIds: []string{vpcId},
    })
    if err != nil {
        return ObservedState{}, err
    }
    if len(out.Vpcs) == 0 {
        return ObservedState{}, fmt.Errorf("VPC %s not found", vpcId)
    }

    v := out.Vpcs[0]

    obs := ObservedState{
        VpcId:           aws.ToString(v.VpcId),
        CidrBlock:       aws.ToString(v.CidrBlock),
        State:           string(v.State),
        InstanceTenancy: string(v.InstanceTenancy),
        OwnerId:         aws.ToString(v.OwnerId),
        IsDefault:       aws.ToBool(v.IsDefault),
        Tags:            make(map[string]string, len(v.Tags)),
    }

    for _, tag := range v.Tags {
        obs.Tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
    }

    // DNS settings require separate DescribeVpcAttribute calls.
    // AWS does not return DNS settings in DescribeVpcs.
    dnsHostnames, err := r.client.DescribeVpcAttribute(ctx, &ec2sdk.DescribeVpcAttributeInput{
        VpcId:     aws.String(vpcId),
        Attribute: ec2types.VpcAttributeNameEnableDnsHostnames,
    })
    if err != nil {
        return ObservedState{}, fmt.Errorf("describe DNS hostnames for VPC %s: %w", vpcId, err)
    }
    if dnsHostnames.EnableDnsHostnames != nil {
        obs.EnableDnsHostnames = aws.ToBool(dnsHostnames.EnableDnsHostnames.Value)
    }

    dnsSupport, err := r.client.DescribeVpcAttribute(ctx, &ec2sdk.DescribeVpcAttributeInput{
        VpcId:     aws.String(vpcId),
        Attribute: ec2types.VpcAttributeNameEnableDnsSupport,
    })
    if err != nil {
        return ObservedState{}, fmt.Errorf("describe DNS support for VPC %s: %w", vpcId, err)
    }
    if dnsSupport.EnableDnsSupport != nil {
        obs.EnableDnsSupport = aws.ToBool(dnsSupport.EnableDnsSupport.Value)
    }

    // DHCP options ID from the VPC object.
    obs.DhcpOptionsId = aws.ToString(v.DhcpOptionsId)

    return obs, nil
}
```

> **API call count**: `DescribeVpc` makes 3 API calls per invocation:
> `DescribeVpcs` + 2x `DescribeVpcAttribute`. This is unavoidable — AWS does not
> return DNS settings in the main `DescribeVpcs` response. The rate limiter handles
> burst management.

#### `DeleteVpc`

```go
func (r *realVPCAPI) DeleteVpc(ctx context.Context, vpcId string) error {
    _, err := r.client.DeleteVpc(ctx, &ec2sdk.DeleteVpcInput{
        VpcId: aws.String(vpcId),
    })
    return err
}
```

> **Dependency errors**: `DeleteVpc` will fail with a `DependencyViolation` error
> if the VPC still has subnets, internet gateways, NAT gateways, security groups
> (beyond the default SG), route tables (beyond the main RT), network interfaces,
> or VPN connections attached. The driver surfaces this as a terminal error (see
> Design Decisions §3). Automatic cascade deletion is deliberately out of scope —
> the DAG scheduler in the orchestrator should order deletions so dependent
> resources are removed before the VPC.

#### `WaitUntilAvailable`

```go
func (r *realVPCAPI) WaitUntilAvailable(ctx context.Context, vpcId string) error {
    waiter := ec2sdk.NewVpcAvailableWaiter(r.client)
    return waiter.Wait(ctx, &ec2sdk.DescribeVpcsInput{
        VpcIds: []string{vpcId},
    }, 2*time.Minute)
}
```

> **Wait duration**: VPCs typically become available within seconds (unlike EC2
> instances which can take minutes). A 2-minute timeout is generous and accounts
> for transient AWS delays. If the service crashes mid-wait, the same
> crash-recovery note from the EC2 plan applies: Restate replays the entire wait
> from scratch. The waiter is idempotent and the wait time is short, so replay
> overhead is negligible.

#### `ModifyDnsHostnames`

```go
func (r *realVPCAPI) ModifyDnsHostnames(ctx context.Context, vpcId string, enabled bool) error {
    _, err := r.client.ModifyVpcAttribute(ctx, &ec2sdk.ModifyVpcAttributeInput{
        VpcId: aws.String(vpcId),
        EnableDnsHostnames: &ec2types.AttributeBooleanValue{
            Value: aws.Bool(enabled),
        },
    })
    return err
}
```

#### `ModifyDnsSupport`

```go
func (r *realVPCAPI) ModifyDnsSupport(ctx context.Context, vpcId string, enabled bool) error {
    _, err := r.client.ModifyVpcAttribute(ctx, &ec2sdk.ModifyVpcAttributeInput{
        VpcId: aws.String(vpcId),
        EnableDnsSupport: &ec2types.AttributeBooleanValue{
            Value: aws.Bool(enabled),
        },
    })
    return err
}
```

> **DNS constraint**: AWS requires `enableDnsSupport` to be `true` when
> `enableDnsHostnames` is `true`. If a user submits a spec with
> `enableDnsHostnames: true` and `enableDnsSupport: false`, the
> `ModifyDnsHostnames` call will fail at the AWS level with an
> `InvalidParameterCombination` error. The driver classifies this as a terminal
> error (status 400 — bad input). CUE schema validation cannot enforce this
> cross-field constraint (CUE supports it syntactically, but the existing
> validation pipeline does not evaluate cross-field dependencies). The driver's
> Provision handler adds an explicit pre-flight check before calling
> `ModifyDnsHostnames`.

#### `UpdateTags`

Follow the same pattern as the EC2 driver: delete all existing non-praxis tags,
then create new ones.

```go
func (r *realVPCAPI) UpdateTags(ctx context.Context, vpcId string, tags map[string]string) error {
    // Get current tags to delete old ones — but preserve praxis:* system tags.
    out, err := r.client.DescribeVpcs(ctx, &ec2sdk.DescribeVpcsInput{
        VpcIds: []string{vpcId},
    })
    if err != nil {
        return err
    }
    if len(out.Vpcs) > 0 {
        vpc := out.Vpcs[0]
        if len(vpc.Tags) > 0 {
            var oldTags []ec2types.Tag
            for _, t := range vpc.Tags {
                key := aws.ToString(t.Key)
                // Skip praxis:* system tags — these are driver-managed and must
                // survive tag updates.
                if strings.HasPrefix(key, "praxis:") {
                    continue
                }
                oldTags = append(oldTags, ec2types.Tag{Key: t.Key})
            }
            if len(oldTags) > 0 {
                _, _ = r.client.DeleteTags(ctx, &ec2sdk.DeleteTagsInput{
                    Resources: []string{vpcId},
                    Tags:      oldTags,
                })
            }
        }
    }

    // Set new tags — skip any praxis:* keys the caller may have passed.
    if len(tags) > 0 {
        var ec2Tags []ec2types.Tag
        for k, v := range tags {
            if strings.HasPrefix(k, "praxis:") {
                continue
            }
            ec2Tags = append(ec2Tags, ec2types.Tag{
                Key: aws.String(k), Value: aws.String(v),
            })
        }
        if len(ec2Tags) > 0 {
            _, err = r.client.CreateTags(ctx, &ec2sdk.CreateTagsInput{
                Resources: []string{vpcId},
                Tags:      ec2Tags,
            })
            return err
        }
    }
    return nil
}
```

#### `FindByManagedKey`

```go
func (r *realVPCAPI) FindByManagedKey(ctx context.Context, managedKey string) (string, error) {
    out, err := r.client.DescribeVpcs(ctx, &ec2sdk.DescribeVpcsInput{
        Filters: []ec2types.Filter{
            {Name: aws.String("tag:praxis:managed-key"), Values: []string{managedKey}},
        },
    })
    if err != nil {
        return "", err
    }

    var matches []string
    for _, v := range out.Vpcs {
        if id := aws.ToString(v.VpcId); id != "" {
            matches = append(matches, id)
        }
    }

    switch len(matches) {
    case 0:
        return "", nil // no conflict — safe to create
    case 1:
        return matches[0], nil // exactly one match — conflict or recovery target
    default:
        return "", fmt.Errorf(
            "ownership corruption: %d VPCs claim managed-key %q: %v; "+
                "manual intervention required",
            len(matches), managedKey, matches,
        )
    }
}
```

### Error Classification Helpers

Add these at the bottom of `aws.go`. Follow the exact pattern from the S3/SG/EC2
drivers.

```go
// IsNotFound returns true if the AWS error indicates the VPC does not exist.
func IsNotFound(err error) bool {
    if err == nil {
        return false
    }
    var apiErr smithy.APIError
    if errors.As(err, &apiErr) {
        code := apiErr.ErrorCode()
        return code == "InvalidVpcID.NotFound" ||
               code == "InvalidVpcID.Malformed"
    }
    errText := err.Error()
    return strings.Contains(errText, "InvalidVpcID.NotFound")
}

// IsDependencyViolation returns true if the VPC cannot be deleted because it
// has dependent resources (subnets, IGWs, NAT GWs, security groups, etc.).
func IsDependencyViolation(err error) bool {
    if err == nil {
        return false
    }
    var apiErr smithy.APIError
    if errors.As(err, &apiErr) {
        return apiErr.ErrorCode() == "DependencyViolation"
    }
    return false
}

// IsInvalidParam returns true if the error indicates an invalid parameter.
// These are surfaced as terminal errors with status 400 (bad request).
func IsInvalidParam(err error) bool {
    if err == nil {
        return false
    }
    var apiErr smithy.APIError
    if errors.As(err, &apiErr) {
        code := apiErr.ErrorCode()
        return code == "InvalidParameterValue" ||
               code == "InvalidParameterCombination" ||
               code == "InvalidVpcRange" ||
               code == "VpcLimitExceeded"
    }
    return false
}

// IsDefaultVpc returns true if the error indicates an attempt to delete
// the default VPC, which requires special handling.
func IsDefaultVpc(err error) bool {
    if err == nil {
        return false
    }
    var apiErr smithy.APIError
    if errors.As(err, &apiErr) {
        return apiErr.ErrorCode() == "DefaultVpcAlreadyExists"
    }
    return false
}

// IsCidrConflict returns true if the requested CIDR block conflicts with an
// existing VPC or overlaps with reserved ranges.
func IsCidrConflict(err error) bool {
    if err == nil {
        return false
    }
    var apiErr smithy.APIError
    if errors.As(err, &apiErr) {
        code := apiErr.ErrorCode()
        return code == "CidrConflict" ||
               code == "InvalidVpc.Range"
    }
    return false
}
```

---

## Step 5 — Drift Detection

**File**: `internal/drivers/vpc/drift.go`

### HasDrift Function

```go
package vpc

import "strings"

// HasDrift returns true if the desired spec and observed state differ.
//
// VPC-specific drift rules:
// - cidrBlock is NOT checked — the primary CIDR block is immutable after creation.
//   Detecting CIDR drift would be misleading since correction requires VPC
//   replacement (delete + recreate), which is destructive and not supported by
//   the driver. See Design Decisions §2.
// - instanceTenancy is NOT checked — tenancy is immutable after creation.
//   "dedicated" cannot be changed back to "default" (AWS restriction).
//
// Fields that ARE checked (and can be corrected in-place):
// - enableDnsHostnames (can be toggled via ModifyVpcAttribute)
// - enableDnsSupport (can be toggled via ModifyVpcAttribute)
// - tags (can be changed via CreateTags/DeleteTags)
func HasDrift(desired VPCSpec, observed ObservedState) bool {
    // Skip drift check if VPC is not in available state
    if observed.State != "available" {
        return false
    }

    if desired.EnableDnsHostnames != observed.EnableDnsHostnames {
        return true
    }

    if desired.EnableDnsSupport != observed.EnableDnsSupport {
        return true
    }

    if !tagsMatch(desired.Tags, observed.Tags) {
        return true
    }

    return false
}
```

### ComputeFieldDiffs Function

```go
// ComputeFieldDiffs returns a list of field-level differences for plan output.
// Includes both mutable fields (which the driver will correct) and immutable fields
// (which are reported with a "(immutable, ignored)" suffix so the user sees the
// discrepancy without the driver attempting to fix it).
func ComputeFieldDiffs(desired VPCSpec, observed ObservedState) []FieldDiffEntry {
    var diffs []FieldDiffEntry

    // --- Mutable fields (driver will correct these) ---

    if desired.EnableDnsHostnames != observed.EnableDnsHostnames {
        diffs = append(diffs, FieldDiffEntry{
            Path:     "spec.enableDnsHostnames",
            OldValue: observed.EnableDnsHostnames,
            NewValue: desired.EnableDnsHostnames,
        })
    }

    if desired.EnableDnsSupport != observed.EnableDnsSupport {
        diffs = append(diffs, FieldDiffEntry{
            Path:     "spec.enableDnsSupport",
            OldValue: observed.EnableDnsSupport,
            NewValue: desired.EnableDnsSupport,
        })
    }

    // Tags: added, changed, removed.
    // Filter out praxis:* system tags from both sides to avoid false diffs
    // (observed always has praxis:managed-key, desired never does).
    desiredFiltered := filterPraxisTags(desired.Tags)
    observedFiltered := filterPraxisTags(observed.Tags)
    for k, v := range desiredFiltered {
        if ov, ok := observedFiltered[k]; !ok {
            diffs = append(diffs, FieldDiffEntry{Path: "tags." + k, OldValue: nil, NewValue: v})
        } else if ov != v {
            diffs = append(diffs, FieldDiffEntry{Path: "tags." + k, OldValue: ov, NewValue: v})
        }
    }
    for k, v := range observedFiltered {
        if _, ok := desiredFiltered[k]; !ok {
            diffs = append(diffs, FieldDiffEntry{Path: "tags." + k, OldValue: v, NewValue: nil})
        }
    }

    // --- Immutable fields (reported for visibility, not corrected) ---

    if desired.CidrBlock != observed.CidrBlock && observed.CidrBlock != "" {
        diffs = append(diffs, FieldDiffEntry{
            Path:     "spec.cidrBlock (immutable, requires replacement)",
            OldValue: observed.CidrBlock,
            NewValue: desired.CidrBlock,
        })
    }

    desiredTenancy := desired.InstanceTenancy
    if desiredTenancy == "" {
        desiredTenancy = "default"
    }
    if desiredTenancy != observed.InstanceTenancy && observed.InstanceTenancy != "" {
        diffs = append(diffs, FieldDiffEntry{
            Path:     "spec.instanceTenancy (immutable, ignored)",
            OldValue: observed.InstanceTenancy,
            NewValue: desiredTenancy,
        })
    }

    return diffs
}

// FieldDiffEntry is the driver-specific diff unit.
type FieldDiffEntry struct {
    Path     string
    OldValue any
    NewValue any
}
```

### Helper Functions

```go
// tagsMatch returns true when two tag maps are semantically equal,
// ignoring praxis:* system tags in both maps. The observed tags always
// contain praxis:managed-key (written at creation), but desired tags never
// do — comparing them directly would cause perpetual drift detection.
func tagsMatch(a, b map[string]string) bool {
    fa := filterPraxisTags(a)
    fb := filterPraxisTags(b)
    if len(fa) != len(fb) {
        return false
    }
    for k, v := range fa {
        if bv, ok := fb[k]; !ok || bv != v {
            return false
        }
    }
    return true
}

// filterPraxisTags returns a copy of the map with all praxis:* keys removed.
func filterPraxisTags(m map[string]string) map[string]string {
    out := make(map[string]string, len(m))
    for k, v := range m {
        if !strings.HasPrefix(k, "praxis:") {
            out[k] = v
        }
    }
    return out
}
```

---

## Step 6 — Driver Implementation

**File**: `internal/drivers/vpc/driver.go`

This is the heart of the driver. Follow the S3, SG, and EC2 patterns exactly.

> **⚠️ Critical Restate footgun — `restate.Run()` panics on non-terminal errors.**
> When the callback passed to `restate.Run()` returns a non-terminal (retryable)
> error, Restate aborts the invocation via panic — the error is never returned to
> the caller. This means error classification (terminal vs. retryable) **must happen
> inside the callback**, not after it. Every AWS API call wrapped in `restate.Run()`
> must check for terminal conditions (invalid input, not-found, dependency violation)
> inside the callback and return `restate.TerminalError(err, code)` for those. Only
> truly transient errors (network timeouts, throttling) should be returned as plain
> errors, which Restate will retry automatically.

### Struct & Constructor

```go
package vpc

import (
    "fmt"
    "time"

    "github.com/aws/aws-sdk-go-v2/aws"
    restate "github.com/restatedev/sdk-go"

    "github.com/praxiscloud/praxis/internal/core/auth"
    "github.com/praxiscloud/praxis/internal/drivers"
    "github.com/praxiscloud/praxis/internal/infra/awsclient"
    "github.com/praxiscloud/praxis/pkg/types"
)

type VPCDriver struct {
    auth       *auth.Registry
    apiFactory func(aws.Config) VPCAPI
}

func NewVPCDriver(accounts *auth.Registry) *VPCDriver {
    return NewVPCDriverWithFactory(accounts, func(cfg aws.Config) VPCAPI {
        return NewVPCAPI(awsclient.NewEC2Client(cfg))
    })
}

func NewVPCDriverWithFactory(accounts *auth.Registry, factory func(aws.Config) VPCAPI) *VPCDriver {
    if accounts == nil {
        accounts = auth.LoadFromEnv()
    }
    if factory == nil {
        factory = func(cfg aws.Config) VPCAPI {
            return NewVPCAPI(awsclient.NewEC2Client(cfg))
        }
    }
    return &VPCDriver{auth: accounts, apiFactory: factory}
}

func (d *VPCDriver) ServiceName() string {
    return ServiceName
}
```

### Provision Handler

```go
func (d *VPCDriver) Provision(ctx restate.ObjectContext, spec VPCSpec) (VPCOutputs, error) {
    ctx.Log().Info("provisioning VPC", "name", spec.Tags["Name"], "key", restate.Key(ctx))
    api, region, err := d.apiForAccount(spec.Account)
    if err != nil {
        return VPCOutputs{}, restate.TerminalError(err, 400)
    }

    // --- Input validation ---
    if spec.CidrBlock == "" {
        return VPCOutputs{}, restate.TerminalError(fmt.Errorf("cidrBlock is required"), 400)
    }
    if spec.Region == "" {
        return VPCOutputs{}, restate.TerminalError(fmt.Errorf("region is required"), 400)
    }
    // Cross-field validation: enableDnsHostnames requires enableDnsSupport
    if spec.EnableDnsHostnames && !spec.EnableDnsSupport {
        return VPCOutputs{}, restate.TerminalError(
            fmt.Errorf("enableDnsHostnames requires enableDnsSupport to be true"), 400,
        )
    }

    // --- Load current state ---
    state, err := restate.Get[VPCState](ctx, drivers.StateKey)
    if err != nil {
        return VPCOutputs{}, err
    }

    state.Desired = spec
    state.Status = types.StatusProvisioning
    state.Mode = types.ModeManaged
    state.Error = ""
    state.Generation++

    // --- Check if VPC already exists (re-provision path) ---
    vpcId := state.Outputs.VpcId
    if vpcId != "" {
        // Verify it still exists
        _, descErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
            obs, err := api.DescribeVpc(rc, vpcId)
            if err != nil {
                if IsNotFound(err) {
                    return ObservedState{}, restate.TerminalError(err, 404)
                }
                return ObservedState{}, err
            }
            return obs, nil
        })
        if descErr != nil {
            vpcId = "" // VPC gone, recreate
        }
    }

    // --- Pre-flight ownership conflict check (first provision only) ---
    if vpcId == "" && spec.ManagedKey != "" {
        conflictId, conflictErr := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
            return api.FindByManagedKey(rc, spec.ManagedKey)
        })
        if conflictErr != nil {
            return VPCOutputs{}, conflictErr
        }
        if conflictId != "" {
            return VPCOutputs{}, restate.TerminalError(
                fmt.Errorf("VPC name %q in this region is already managed by Praxis (vpcId: %s); "+
                    "remove the existing resource or use a different metadata.name", spec.ManagedKey, conflictId),
                409,
            )
        }
    }

    // --- Create VPC if it doesn't exist ---
    if vpcId == "" {
        newVpcId, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
            id, err := api.CreateVpc(rc, spec)
            if err != nil {
                if IsInvalidParam(err) {
                    return "", restate.TerminalError(err, 400)
                }
                if IsCidrConflict(err) {
                    return "", restate.TerminalError(err, 409)
                }
                return "", err // transient → Restate retries
            }
            return id, nil
        })
        if err != nil {
            state.Status = types.StatusError
            state.Error = err.Error()
            restate.Set(ctx, drivers.StateKey, state)
            return VPCOutputs{}, err
        }
        vpcId = newVpcId

        // Wait for the VPC to reach "available" state.
        // VPCs typically become available within seconds.
        _, waitErr := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
            if err := api.WaitUntilAvailable(rc, vpcId); err != nil {
                return restate.Void{}, err // transient — will retry
            }
            return restate.Void{}, nil
        })
        if waitErr != nil {
            state.Status = types.StatusError
            state.Error = fmt.Sprintf("VPC %s created but failed to reach available state: %v", vpcId, waitErr)
            state.Outputs = VPCOutputs{VpcId: vpcId}
            restate.Set(ctx, drivers.StateKey, state)
            return VPCOutputs{}, waitErr
        }

        // Apply DNS settings after VPC creation.
        // AWS defaults for non-default VPCs: enableDnsSupport=true, enableDnsHostnames=false.
        // Only call ModifyVpcAttribute if the desired value differs from defaults.
        if !spec.EnableDnsSupport {
            // User wants DNS support disabled — non-default, must explicitly set.
            _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
                return restate.Void{}, api.ModifyDnsSupport(rc, vpcId, false)
            })
            if err != nil {
                state.Status = types.StatusError
                state.Error = fmt.Sprintf("failed to disable DNS support: %v", err)
                state.Outputs = VPCOutputs{VpcId: vpcId}
                restate.Set(ctx, drivers.StateKey, state)
                return VPCOutputs{}, restate.TerminalError(err, 500)
            }
        }
        if spec.EnableDnsHostnames {
            // User wants DNS hostnames enabled — non-default, must explicitly set.
            _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
                return restate.Void{}, api.ModifyDnsHostnames(rc, vpcId, true)
            })
            if err != nil {
                state.Status = types.StatusError
                state.Error = fmt.Sprintf("failed to enable DNS hostnames: %v", err)
                state.Outputs = VPCOutputs{VpcId: vpcId}
                restate.Set(ctx, drivers.StateKey, state)
                return VPCOutputs{}, restate.TerminalError(err, 500)
            }
        }
    } else {
        // --- Re-provision path: converge mutable attributes ---

        // DNS hostnames
        if spec.EnableDnsHostnames != state.Observed.EnableDnsHostnames {
            _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
                return restate.Void{}, api.ModifyDnsHostnames(rc, vpcId, spec.EnableDnsHostnames)
            })
            if err != nil {
                state.Status = types.StatusError
                state.Error = fmt.Sprintf("failed to modify DNS hostnames: %v", err)
                restate.Set(ctx, drivers.StateKey, state)
                return VPCOutputs{}, restate.TerminalError(err, 500)
            }
        }

        // DNS support
        if spec.EnableDnsSupport != state.Observed.EnableDnsSupport {
            _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
                return restate.Void{}, api.ModifyDnsSupport(rc, vpcId, spec.EnableDnsSupport)
            })
            if err != nil {
                state.Status = types.StatusError
                state.Error = fmt.Sprintf("failed to modify DNS support: %v", err)
                restate.Set(ctx, drivers.StateKey, state)
                return VPCOutputs{}, restate.TerminalError(err, 500)
            }
        }

        // Tags
        if !tagsMatch(spec.Tags, state.Observed.Tags) {
            _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
                return restate.Void{}, api.UpdateTags(rc, vpcId, spec.Tags)
            })
            if err != nil {
                state.Status = types.StatusError
                state.Error = err.Error()
                restate.Set(ctx, drivers.StateKey, state)
                return VPCOutputs{}, restate.TerminalError(err, 500)
            }
        }
    }

    // --- Describe final state to populate outputs ---
    observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
        return api.DescribeVpc(rc, vpcId)
    })
    if err != nil {
        state.Status = types.StatusError
        state.Error = err.Error()
        state.Outputs = VPCOutputs{VpcId: vpcId}
        restate.Set(ctx, drivers.StateKey, state)
        return VPCOutputs{}, err
    }

    // --- Build outputs ---
    outputs := VPCOutputs{
        VpcId:              vpcId,
        CidrBlock:          observed.CidrBlock,
        State:              observed.State,
        EnableDnsHostnames: observed.EnableDnsHostnames,
        EnableDnsSupport:   observed.EnableDnsSupport,
        InstanceTenancy:    observed.InstanceTenancy,
        OwnerId:            observed.OwnerId,
        DhcpOptionsId:      observed.DhcpOptionsId,
        IsDefault:          observed.IsDefault,
        // ARN construction: arn:aws:ec2:<region>:<account-id>:vpc/<vpc-id>
        // OwnerId from DescribeVpcs gives us the account ID.
        ARN: fmt.Sprintf("arn:aws:ec2:%s:%s:vpc/%s", region, observed.OwnerId, vpcId),
    }

    // --- Commit state atomically ---
    state.Observed = observed
    state.Outputs = outputs
    state.Status = types.StatusReady
    state.Error = ""
    restate.Set(ctx, drivers.StateKey, state)

    d.scheduleReconcile(ctx, &state)
    return outputs, nil
}
```

> **ARN construction**: Unlike the EC2 driver (which does not populate the ARN
> because it requires an STS call for the account ID), the VPC driver constructs
> the ARN immediately because `DescribeVpcs` returns `OwnerId` (the AWS account
> ID). The ARN format for VPCs is deterministic:
> `arn:aws:ec2:<region>:<account-id>:vpc/<vpc-id>`. Downstream CEL expressions
> like `${resources.my-vpc.outputs.arn}` work from the first provision.

### Import Handler

```go
func (d *VPCDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (VPCOutputs, error) {
    ctx.Log().Info("importing VPC", "resourceId", ref.ResourceID, "mode", ref.Mode)
    api, region, err := d.apiForAccount(ref.Account)
    if err != nil {
        return VPCOutputs{}, restate.TerminalError(err, 400)
    }

    // VPC import defaults to ModeObserved (same rationale as EC2).
    // VPC deletion is disruptive — all dependent resources (subnets, instances,
    // security groups, etc.) become orphaned or fail. Defaulting to Observed
    // prevents the import VO from participating in destructive lifecycle actions.
    mode := defaultVPCImportMode(ref.Mode)

    state, err := restate.Get[VPCState](ctx, drivers.StateKey)
    if err != nil {
        return VPCOutputs{}, err
    }
    state.Generation++

    // Describe the existing VPC
    observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
        obs, err := api.DescribeVpc(rc, ref.ResourceID)
        if err != nil {
            if IsNotFound(err) {
                return ObservedState{}, restate.TerminalError(
                    fmt.Errorf("import failed: VPC %s does not exist", ref.ResourceID), 404,
                )
            }
            return ObservedState{}, err
        }
        return obs, nil
    })
    if err != nil {
        return VPCOutputs{}, err
    }

    // Synthesize spec from observed
    spec := specFromObserved(observed)
    spec.Account = ref.Account
    spec.Region = region

    outputs := VPCOutputs{
        VpcId:              observed.VpcId,
        CidrBlock:          observed.CidrBlock,
        State:              observed.State,
        EnableDnsHostnames: observed.EnableDnsHostnames,
        EnableDnsSupport:   observed.EnableDnsSupport,
        InstanceTenancy:    observed.InstanceTenancy,
        OwnerId:            observed.OwnerId,
        DhcpOptionsId:      observed.DhcpOptionsId,
        IsDefault:          observed.IsDefault,
        ARN:                fmt.Sprintf("arn:aws:ec2:%s:%s:vpc/%s", region, observed.OwnerId, observed.VpcId),
    }

    state.Desired = spec
    state.Observed = observed
    state.Outputs = outputs
    state.Status = types.StatusReady
    state.Mode = mode
    state.Error = ""
    restate.Set(ctx, drivers.StateKey, state)

    d.scheduleReconcile(ctx, &state)
    return outputs, nil
}

// specFromObserved creates a VPCSpec from observed state
// so the first reconciliation after import sees no drift.
func specFromObserved(obs ObservedState) VPCSpec {
    return VPCSpec{
        CidrBlock:          obs.CidrBlock,
        EnableDnsHostnames: obs.EnableDnsHostnames,
        EnableDnsSupport:   obs.EnableDnsSupport,
        InstanceTenancy:    obs.InstanceTenancy,
        Tags:               obs.Tags,
    }
}

// defaultVPCImportMode returns ModeObserved when no explicit mode is requested.
// VPC import defaults to Observed (not Managed) because VPC deletion is disruptive:
// all dependent resources (subnets, instances, security groups, load balancers, etc.)
// become orphaned or fail when their VPC is deleted. Operators must pass --mode
// managed to grant full lifecycle control to the import VO.
func defaultVPCImportMode(m types.Mode) types.Mode {
    if m == "" {
        return types.ModeObserved
    }
    return m
}
```

### Delete Handler

The Delete handler **blocks deletion for Observed-mode resources** and **blocks
deletion of default VPCs**, mirroring the EC2 driver's safety patterns.

```go
func (d *VPCDriver) Delete(ctx restate.ObjectContext) error {
    ctx.Log().Info("deleting VPC", "key", restate.Key(ctx))

    state, err := restate.Get[VPCState](ctx, drivers.StateKey)
    if err != nil {
        return err
    }

    // --- Observed-mode guard ---
    if state.Mode == types.ModeObserved {
        return restate.TerminalError(
            fmt.Errorf("cannot delete VPC %s: resource is in Observed mode; "+
                "re-import with --mode managed to allow deletion", state.Outputs.VpcId),
            409,
        )
    }

    // --- Default VPC guard ---
    // Prevent accidental deletion of the default VPC, which AWS creates
    // automatically and which many services depend on implicitly.
    if state.Observed.IsDefault {
        return restate.TerminalError(
            fmt.Errorf("cannot delete VPC %s: it is the default VPC for this region; "+
                "default VPC deletion must be done manually via the AWS console", state.Outputs.VpcId),
            409,
        )
    }

    api, _, err := d.apiForAccount(state.Desired.Account)
    if err != nil {
        return restate.TerminalError(err, 400)
    }

    vpcId := state.Outputs.VpcId
    if vpcId == "" {
        // No VPC was ever provisioned — set tombstone
        restate.Set(ctx, drivers.StateKey, VPCState{Status: types.StatusDeleted})
        return nil
    }

    state.Status = types.StatusDeleting
    state.Error = ""
    restate.Set(ctx, drivers.StateKey, state)

    _, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
        if err := api.DeleteVpc(rc, vpcId); err != nil {
            if IsNotFound(err) {
                return restate.Void{}, nil // already gone
            }
            if IsDependencyViolation(err) {
                return restate.Void{}, restate.TerminalError(
                    fmt.Errorf("cannot delete VPC %s: dependent resources exist (subnets, "+
                        "internet gateways, NAT gateways, security groups, etc.); "+
                        "remove all dependent resources first", vpcId),
                    409,
                )
            }
            if IsInvalidParam(err) {
                return restate.Void{}, restate.TerminalError(err, 400)
            }
            return restate.Void{}, err // transient → Restate retries
        }
        return restate.Void{}, nil
    })
    if err != nil {
        state.Status = types.StatusError
        state.Error = err.Error()
        restate.Set(ctx, drivers.StateKey, state)
        return err
    }

    restate.Set(ctx, drivers.StateKey, VPCState{
        Status: types.StatusDeleted,
    })
    return nil
}
```

> **Dependency violation handling**: Unlike EC2 (which terminates a standalone
> instance), VPC deletion can fail because the VPC still has dependent resources.
> The driver does NOT attempt cascade deletion — it surfaces a terminal error
> (409) listing the dependency constraint. The DAG scheduler in the orchestrator
> should order deletions so dependent resources (subnets, IGWs, security groups)
> are removed before the VPC. If a user manually added resources to the VPC
> outside of Praxis, those resources block deletion and the user must clean them
> up manually.

### Reconcile Handler

```go
func (d *VPCDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
    state, err := restate.Get[VPCState](ctx, drivers.StateKey)
    if err != nil {
        return types.ReconcileResult{}, err
    }
    api, _, err := d.apiForAccount(state.Desired.Account)
    if err != nil {
        return types.ReconcileResult{}, restate.TerminalError(err, 400)
    }

    state.ReconcileScheduled = false

    if state.Status != types.StatusReady && state.Status != types.StatusError {
        restate.Set(ctx, drivers.StateKey, state)
        return types.ReconcileResult{}, nil
    }

    vpcId := state.Outputs.VpcId
    if vpcId == "" {
        restate.Set(ctx, drivers.StateKey, state)
        return types.ReconcileResult{}, nil
    }

    // Capture a replay-stable timestamp for this reconcile cycle.
    now, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
        return time.Now().UTC().Format(time.RFC3339), nil
    })
    if err != nil {
        return types.ReconcileResult{}, err
    }

    // Describe current state
    observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
        obs, err := api.DescribeVpc(rc, vpcId)
        if err != nil {
            if IsNotFound(err) {
                return ObservedState{}, restate.TerminalError(err, 404)
            }
            return ObservedState{}, err
        }
        return obs, nil
    })
    if err != nil {
        if IsNotFound(err) {
            state.Status = types.StatusError
            state.Error = fmt.Sprintf("VPC %s was deleted externally", vpcId)
            state.LastReconcile = now
            restate.Set(ctx, drivers.StateKey, state)
            d.scheduleReconcile(ctx, &state)
            return types.ReconcileResult{Error: state.Error}, nil
        }
        state.LastReconcile = now
        restate.Set(ctx, drivers.StateKey, state)
        d.scheduleReconcile(ctx, &state)
        return types.ReconcileResult{Error: err.Error()}, nil
    }

    state.Observed = observed
    state.LastReconcile = now

    drift := HasDrift(state.Desired, observed)

    // Error status: read-only, no correction
    if state.Status == types.StatusError {
        restate.Set(ctx, drivers.StateKey, state)
        d.scheduleReconcile(ctx, &state)
        return types.ReconcileResult{Drift: drift, Correcting: false}, nil
    }

    // Ready + Managed + drift: correct
    if drift && state.Mode == types.ModeManaged {
        ctx.Log().Info("drift detected, correcting", "vpcId", vpcId)
        correctionErr := d.correctDrift(ctx, api, vpcId, state.Desired, observed)
        if correctionErr != nil {
            restate.Set(ctx, drivers.StateKey, state)
            d.scheduleReconcile(ctx, &state)
            return types.ReconcileResult{Drift: true, Correcting: true, Error: correctionErr.Error()}, nil
        }
        restate.Set(ctx, drivers.StateKey, state)
        d.scheduleReconcile(ctx, &state)
        return types.ReconcileResult{Drift: true, Correcting: true}, nil
    }

    // Ready + Observed + drift: report only
    if drift && state.Mode == types.ModeObserved {
        ctx.Log().Info("drift detected (observed mode, not correcting)", "vpcId", vpcId)
        restate.Set(ctx, drivers.StateKey, state)
        d.scheduleReconcile(ctx, &state)
        return types.ReconcileResult{Drift: true, Correcting: false}, nil
    }

    // No drift
    restate.Set(ctx, drivers.StateKey, state)
    d.scheduleReconcile(ctx, &state)
    return types.ReconcileResult{}, nil
}

// correctDrift applies corrections for mutable VPC attributes.
func (d *VPCDriver) correctDrift(ctx restate.ObjectContext, api VPCAPI, vpcId string, desired VPCSpec, observed ObservedState) error {
    // DNS support must be corrected before DNS hostnames (dependency).
    // If disabling DNS support, must disable DNS hostnames first.
    // If enabling DNS hostnames, must enable DNS support first.

    if desired.EnableDnsSupport != observed.EnableDnsSupport {
        if desired.EnableDnsSupport {
            // Enabling DNS support — do this before enabling DNS hostnames
            _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
                return restate.Void{}, api.ModifyDnsSupport(rc, vpcId, desired.EnableDnsSupport)
            })
            if err != nil {
                return fmt.Errorf("modify DNS support: %w", err)
            }
        }
    }

    if desired.EnableDnsHostnames != observed.EnableDnsHostnames {
        _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
            return restate.Void{}, api.ModifyDnsHostnames(rc, vpcId, desired.EnableDnsHostnames)
        })
        if err != nil {
            return fmt.Errorf("modify DNS hostnames: %w", err)
        }
    }

    if desired.EnableDnsSupport != observed.EnableDnsSupport {
        if !desired.EnableDnsSupport {
            // Disabling DNS support — do this after disabling DNS hostnames
            _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
                return restate.Void{}, api.ModifyDnsSupport(rc, vpcId, desired.EnableDnsSupport)
            })
            if err != nil {
                return fmt.Errorf("modify DNS support: %w", err)
            }
        }
    }

    // Tags
    if !tagsMatch(desired.Tags, observed.Tags) {
        _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
            return restate.Void{}, api.UpdateTags(rc, vpcId, desired.Tags)
        })
        if err != nil {
            return fmt.Errorf("update tags: %w", err)
        }
    }

    return nil
}
```

> **DNS correction ordering**: The `correctDrift` function respects the AWS
> constraint that `enableDnsHostnames=true` requires `enableDnsSupport=true`.
> When both settings need to change:
> - **Enabling hostnames**: enable support first, then hostnames.
> - **Disabling support**: disable hostnames first, then support.
> The ordering logic handles all four state transitions correctly.

### Shared Handlers

```go
func (d *VPCDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
    state, err := restate.Get[VPCState](ctx, drivers.StateKey)
    if err != nil {
        return types.StatusResponse{}, err
    }
    return types.StatusResponse{
        Status:     state.Status,
        Mode:       state.Mode,
        Generation: state.Generation,
        Error:      state.Error,
    }, nil
}

func (d *VPCDriver) GetOutputs(ctx restate.ObjectSharedContext) (VPCOutputs, error) {
    state, err := restate.Get[VPCState](ctx, drivers.StateKey)
    if err != nil {
        return VPCOutputs{}, err
    }
    return state.Outputs, nil
}
```

### Private Helpers

```go
func (d *VPCDriver) scheduleReconcile(ctx restate.ObjectContext, state *VPCState) {
    if state.ReconcileScheduled {
        return
    }
    state.ReconcileScheduled = true
    restate.Set(ctx, drivers.StateKey, *state)
    restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").
        Send(restate.Void{}, restate.WithDelay(drivers.ReconcileInterval))
}

func (d *VPCDriver) apiForAccount(account string) (VPCAPI, string, error) {
    if d == nil || d.auth == nil || d.apiFactory == nil {
        return nil, "", fmt.Errorf("VPCDriver is not configured with an auth registry")
    }
    awsCfg, err := d.auth.Resolve(account)
    if err != nil {
        return nil, "", fmt.Errorf("resolve VPC account %q: %w", account, err)
    }
    return d.apiFactory(awsCfg), awsCfg.Region, nil
}
```

---

## Step 7 — Provider Adapter

**File**: `internal/core/provider/vpc_adapter.go`

This is the type bridge between generic JSON resource documents and the typed
VPC driver. Follows the exact pattern of `ec2_adapter.go`.

### Key Scope

VPCs use `KeyScopeRegion`. The key format is `region~metadata.name`.

- `BuildKey`: `region~metadata.name` — template declares both values.
- `BuildImportKey`: `region~resourceID` — import uses VPC ID as the name
  component, producing a separate Virtual Object from any template-managed one.

### Plan: VO-to-VO Call Pattern

The VPC adapter's `Plan()` method follows the EC2 adapter's state-driven pattern:
read `GetOutputs` from the Virtual Object, then describe by stored VPC ID for drift
comparison. Same rules about VO-to-VO calls being invoked directly on the Restate
context (NOT inside `restate.Run()`).

### Complete Adapter

```go
package provider

import (
    "encoding/json"
    "fmt"
    "strings"

    "github.com/aws/aws-sdk-go-v2/aws"
    restate "github.com/restatedev/sdk-go"

    "github.com/praxiscloud/praxis/internal/core/auth"
    "github.com/praxiscloud/praxis/internal/drivers/vpc"
    "github.com/praxiscloud/praxis/internal/infra/awsclient"
    "github.com/praxiscloud/praxis/pkg/types"
)

// VPCAdapter adapts generic resource documents to the strongly typed VPC driver.
type VPCAdapter struct {
    auth              *auth.Registry
    staticPlanningAPI vpc.VPCAPI
    apiFactory        func(aws.Config) vpc.VPCAPI
}

func NewVPCAdapter() *VPCAdapter {
    return NewVPCAdapterWithRegistry(auth.LoadFromEnv())
}

func NewVPCAdapterWithRegistry(accounts *auth.Registry) *VPCAdapter {
    if accounts == nil {
        accounts = auth.LoadFromEnv()
    }
    return &VPCAdapter{
        auth: accounts,
        apiFactory: func(cfg aws.Config) vpc.VPCAPI {
            return vpc.NewVPCAPI(awsclient.NewEC2Client(cfg))
        },
    }
}

func NewVPCAdapterWithAPI(api vpc.VPCAPI) *VPCAdapter {
    return &VPCAdapter{staticPlanningAPI: api}
}

func (a *VPCAdapter) Kind() string {
    return vpc.ServiceName
}

func (a *VPCAdapter) ServiceName() string {
    return vpc.ServiceName
}

func (a *VPCAdapter) Scope() KeyScope {
    return KeyScopeRegion
}

func (a *VPCAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
    doc, err := decodeResourceDocument(resourceDoc)
    if err != nil {
        return "", err
    }
    spec, err := a.decodeSpec(doc)
    if err != nil {
        return "", err
    }
    if err := ValidateKeyPart("region", spec.Region); err != nil {
        return "", err
    }
    name := strings.TrimSpace(doc.Metadata.Name)
    if err := ValidateKeyPart("VPC name", name); err != nil {
        return "", err
    }
    return JoinKey(spec.Region, name), nil
}

func (a *VPCAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
    doc, err := decodeResourceDocument(resourceDoc)
    if err != nil {
        return nil, err
    }
    return a.decodeSpec(doc)
}

func (a *VPCAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
    typedSpec, err := castSpec[vpc.VPCSpec](spec)
    if err != nil {
        return nil, err
    }
    typedSpec.Account = account
    typedSpec.ManagedKey = key

    fut := restate.WithRequestType[vpc.VPCSpec, vpc.VPCOutputs](
        restate.Object[vpc.VPCOutputs](ctx, a.ServiceName(), key, "Provision"),
    ).RequestFuture(typedSpec)

    return &provisionHandle[vpc.VPCOutputs]{
        id:        fut.GetInvocationId(),
        raw:       fut,
        normalize: a.NormalizeOutputs,
    }, nil
}

func (a *VPCAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
    fut := restate.WithRequestType[restate.Void, restate.Void](
        restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete"),
    ).RequestFuture(restate.Void{})

    return &deleteHandle{
        id:  fut.GetInvocationId(),
        raw: fut,
    }, nil
}

func (a *VPCAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
    out, err := castOutput[vpc.VPCOutputs](raw)
    if err != nil {
        return nil, err
    }
    result := map[string]any{
        "vpcId":              out.VpcId,
        "cidrBlock":          out.CidrBlock,
        "state":              out.State,
        "enableDnsHostnames": out.EnableDnsHostnames,
        "enableDnsSupport":   out.EnableDnsSupport,
        "instanceTenancy":    out.InstanceTenancy,
        "ownerId":            out.OwnerId,
        "dhcpOptionsId":      out.DhcpOptionsId,
        "isDefault":          out.IsDefault,
    }
    if out.ARN != "" {
        result["arn"] = out.ARN
    }
    return result, nil
}

func (a *VPCAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
    desired, err := castSpec[vpc.VPCSpec](desiredSpec)
    if err != nil {
        return "", nil, err
    }

    // Read stored outputs via VO-to-VO call (NOT inside restate.Run()).
    outputs, getErr := restate.Object[vpc.VPCOutputs](ctx, a.ServiceName(), key, "GetOutputs").
        Request(restate.Void{})
    if getErr != nil {
        return "", nil, fmt.Errorf("VPC Plan: failed to read outputs for key %q: %w", key, getErr)
    }
    if outputs.VpcId == "" {
        fields, fieldErr := createFieldDiffsFromSpec(desired)
        if fieldErr != nil {
            return "", nil, fieldErr
        }
        return types.OpCreate, fields, nil
    }

    // VPC exists — describe it by stored ID for drift comparison.
    planningAPI, err := a.planningAPI(account)
    if err != nil {
        return "", nil, err
    }

    type describePlanResult struct {
        State vpc.ObservedState
        Found bool
    }
    result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
        obs, descErr := planningAPI.DescribeVpc(runCtx, outputs.VpcId)
        if descErr != nil {
            if vpc.IsNotFound(descErr) {
                return describePlanResult{Found: false}, nil
            }
            return describePlanResult{}, restate.TerminalError(descErr, 500)
        }
        return describePlanResult{State: obs, Found: true}, nil
    })
    if err != nil {
        return "", nil, err
    }

    if !result.Found {
        // VPC was removed externally — plan shows re-create.
        fields, fieldErr := createFieldDiffsFromSpec(desired)
        if fieldErr != nil {
            return "", nil, fieldErr
        }
        return types.OpCreate, fields, nil
    }

    rawDiffs := vpc.ComputeFieldDiffs(desired, result.State)
    if len(rawDiffs) == 0 {
        return types.OpNoOp, nil, nil
    }

    fields := make([]types.FieldDiff, 0, len(rawDiffs))
    for _, diff := range rawDiffs {
        fields = append(fields, types.FieldDiff{
            Path:     diff.Path,
            OldValue: diff.OldValue,
            NewValue: diff.NewValue,
        })
    }
    return types.OpUpdate, fields, nil
}

func (a *VPCAdapter) BuildImportKey(region, resourceID string) (string, error) {
    if err := ValidateKeyPart("region", region); err != nil {
        return "", err
    }
    if err := ValidateKeyPart("resource ID", resourceID); err != nil {
        return "", err
    }
    return JoinKey(region, resourceID), nil
}

func (a *VPCAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
    ref.Account = account
    output, err := restate.WithRequestType[types.ImportRef, vpc.VPCOutputs](
        restate.Object[vpc.VPCOutputs](ctx, a.ServiceName(), key, "Import"),
    ).Request(ref)
    if err != nil {
        return "", nil, err
    }
    outputs, err := a.NormalizeOutputs(output)
    if err != nil {
        return "", nil, err
    }
    return types.StatusReady, outputs, nil
}

func (a *VPCAdapter) decodeSpec(doc resourceDocument) (vpc.VPCSpec, error) {
    var spec vpc.VPCSpec
    if err := json.Unmarshal(doc.Spec, &spec); err != nil {
        return vpc.VPCSpec{}, fmt.Errorf("decode VPC spec: %w", err)
    }
    name := strings.TrimSpace(doc.Metadata.Name)
    if name == "" {
        return vpc.VPCSpec{}, fmt.Errorf("VPC metadata.name is required")
    }
    if strings.TrimSpace(spec.Region) == "" {
        return vpc.VPCSpec{}, fmt.Errorf("VPC spec.region is required")
    }
    if strings.TrimSpace(spec.CidrBlock) == "" {
        return vpc.VPCSpec{}, fmt.Errorf("VPC spec.cidrBlock is required")
    }
    // Set Name tag from metadata.name if not already set
    if spec.Tags == nil {
        spec.Tags = make(map[string]string)
    }
    if spec.Tags["Name"] == "" {
        spec.Tags["Name"] = name
    }
    // Apply default tenancy if not specified
    if spec.InstanceTenancy == "" {
        spec.InstanceTenancy = "default"
    }
    spec.Account = ""
    return spec, nil
}

func (a *VPCAdapter) planningAPI(account string) (vpc.VPCAPI, error) {
    if a.staticPlanningAPI != nil {
        return a.staticPlanningAPI, nil
    }
    if a.auth == nil || a.apiFactory == nil {
        return nil, fmt.Errorf("VPC adapter planning API is not configured")
    }
    awsCfg, err := a.auth.Resolve(account)
    if err != nil {
        return nil, fmt.Errorf("resolve VPC planning account %q: %w", account, err)
    }
    return a.apiFactory(awsCfg), nil
}
```

---

## Step 8 — Registry Integration

**File**: `internal/core/provider/registry.go` — **MODIFY**

Add `NewVPCAdapter` to the hardcoded adapter set in `NewRegistry()`.

### Change

In the `NewRegistry()` function, add one line:

```go
// Before:
func NewRegistry() *Registry {
    accounts := auth.LoadFromEnv()
    return NewRegistryWithAdapters(
        NewS3AdapterWithRegistry(accounts),
        NewEC2AdapterWithRegistry(accounts),
        NewSecurityGroupAdapterWithRegistry(accounts),
    )
}

// After:
func NewRegistry() *Registry {
    accounts := auth.LoadFromEnv()
    return NewRegistryWithAdapters(
        NewS3AdapterWithRegistry(accounts),
        NewEC2AdapterWithRegistry(accounts),
        NewSecurityGroupAdapterWithRegistry(accounts),
        NewVPCAdapterWithRegistry(accounts),
    )
}
```

---

## Step 9 — Add VPC to Network Driver Pack

### Entry Point

**File**: `cmd/praxis-network/main.go` — **MODIFY**

The VPC driver joins the existing **network** driver pack alongside SecurityGroup. The Restate SDK supports binding multiple Virtual Objects to one server via chained `.Bind()` calls:

```go
package main

import (
    "context"
    "log/slog"
    "os"

    restate "github.com/restatedev/sdk-go"
    "github.com/restatedev/sdk-go/server"

    "github.com/praxiscloud/praxis/internal/core/config"
    "github.com/praxiscloud/praxis/internal/drivers/sg"
    "github.com/praxiscloud/praxis/internal/drivers/vpc"
)

func main() {
    cfg := config.Load()

    srv := server.NewRestate().
        Bind(restate.Reflect(sg.NewSecurityGroupDriver(cfg.Auth()))).
        Bind(restate.Reflect(vpc.NewVPCDriver(cfg.Auth())))

    slog.Info("starting network driver pack", "addr", cfg.ListenAddr)
    if err := srv.Start(context.Background(), cfg.ListenAddr); err != nil {
        slog.Error("network driver pack exited", "err", err.Error())
        os.Exit(1)
    }
}
```

No new Dockerfile needed — the existing `cmd/praxis-network/Dockerfile` already builds the entire network pack binary.

---

## Step 10 — Justfile

### justfile — **MODIFY**

No Docker Compose changes needed — the VPC driver automatically registers with Restate as part of the existing `praxis-network` service when the network pack is registered.

Add VPC-specific test targets:

```
# Run VPC driver unit tests only
test-vpc:
    go test ./internal/drivers/vpc/... -v -count=1 -race
```

The existing `logs-network`, `register`, and `up` recipes already cover the network pack. No new entries needed since VPC is co-hosted in the same container.

---

## Step 11 — Unit Tests

### `internal/drivers/vpc/driver_test.go`

Create a mock `VPCAPI` using testify/mock. Test each handler with mocked AWS responses.

**Test cases to implement:**

#### Provision Tests
1. `TestProvision_CreatesNewVpc` — happy path: CreateVpc succeeds, WaitUntilAvailable
   succeeds, DescribeVpc returns outputs with DNS settings.
2. `TestProvision_MissingCidrBlockFails` — returns terminal error 400.
3. `TestProvision_MissingRegionFails` — returns terminal error 400.
4. `TestProvision_InvalidCidrFails` — `IsInvalidParam` triggers terminal error 400.
5. `TestProvision_CidrConflictFails` — `IsCidrConflict` triggers terminal error 409.
6. `TestProvision_DnsHostnamesWithoutDnsSupportFails` — cross-field validation
   returns terminal error 400 before calling AWS.
7. `TestProvision_IdempotentReprovision` — second call with same spec converges,
   doesn't create new VPC.
8. `TestProvision_DnsHostnamesChange` — modification of DNS hostnames calls
   ModifyDnsHostnames.
9. `TestProvision_DnsSupportChange` — modification of DNS support calls
   ModifyDnsSupport.
10. `TestProvision_TagUpdate` — tag change calls UpdateTags.
11. `TestProvision_ConflictTaggedVpcFails` — `FindByManagedKey` returns a non-empty
    ID → terminal error 409.
12. `TestProvision_NoConflictWhenTagNotPresent` — `FindByManagedKey` returns `""`
    → proceeds to CreateVpc.
13. `TestProvision_MultipleConflictsFails` — `FindByManagedKey` returns ownership
    corruption error → terminal error 500.
14. `TestProvision_DedicatedTenancy` — VPC created with dedicated tenancy, outputs
    reflect the setting.
15. `TestProvision_EnableDnsHostnamesOnCreate` — new VPC with enableDnsHostnames=true
    calls ModifyDnsHostnames after creation.
16. `TestProvision_DisableDnsSupportOnCreate` — new VPC with enableDnsSupport=false
    calls ModifyDnsSupport after creation.

#### Import Tests
17. `TestImport_ExistingVpc` — describes VPC, synthesizes spec, returns outputs.
18. `TestImport_NotFoundFails` — returns terminal error 404.
19. `TestImport_DefaultsToObservedMode` — import with no explicit mode sets ModeObserved.
20. `TestImport_ExplicitManagedMode` — import with `--mode managed` sets ModeManaged.
21. `TestImport_DefaultVpc` — importing the default VPC works, sets IsDefault=true.

#### Delete Tests
22. `TestDelete_DeletesVpc` — calls DeleteVpc, sets tombstone state (ModeManaged).
23. `TestDelete_AlreadyGone` — IsNotFound returns success (idempotent).
24. `TestDelete_NoVpcProvisioned` — sets tombstone without API call.
25. `TestDelete_ObservedModeBlocked` — Delete returns terminal error 409 for
    ModeObserved resources.
26. `TestDelete_DefaultVpcBlocked` — Delete returns terminal error 409 for the
    default VPC.
27. `TestDelete_DependencyViolationFails` — VPC with dependent resources returns
    terminal error 409 with helpful message.

#### Reconcile Tests
28. `TestReconcile_NoDrift` — no changes when spec matches observed.
29. `TestReconcile_DetectsDnsHostnamesDrift` — drift=true, correcting=true in
    managed mode.
30. `TestReconcile_DetectsDnsSupportDrift` — drift=true, correcting=true in
    managed mode.
31. `TestReconcile_DetectsTagDrift` — drift=true, tag correction applied.
32. `TestReconcile_ObservedModeReportsOnly` — drift=true, correcting=false.
33. `TestReconcile_ExternalDeletion` — transitions to Error status when VPC
    is deleted externally.
34. `TestReconcile_SkipsNonReadyStatus` — no-op for Pending/Provisioning/Deleting.
35. `TestReconcile_DnsCorrectionOrdering` — verifies that enableDnsSupport is
    enabled before enableDnsHostnames when both need correction.

#### Shared Handler Tests
36. `TestGetStatus_ReturnsCurrentState` — reads state from K/V.
37. `TestGetOutputs_ReturnsOutputs` — reads outputs from K/V.

### `internal/drivers/vpc/drift_test.go`

Test cases:
1. `TestHasDrift_NoDrift` — identical desired and observed returns false.
2. `TestHasDrift_DnsHostnamesChanged` — returns true.
3. `TestHasDrift_DnsSupportChanged` — returns true.
4. `TestHasDrift_TagAdded` — returns true.
5. `TestHasDrift_TagRemoved` — returns true.
6. `TestHasDrift_TagValueChanged` — returns true.
7. `TestHasDrift_PendingVpcNoDrift` — returns false (skip non-available).
8. `TestHasDrift_CidrChangedNoDrift` — returns false (CIDR is immutable, not checked).
9. `TestHasDrift_TenancyChangedNoDrift` — returns false (tenancy is immutable, not checked).
10. `TestComputeFieldDiffs_DnsHostnames` — returns field-level diff for DNS hostnames.
11. `TestComputeFieldDiffs_DnsSupport` — returns field-level diff for DNS support.
12. `TestComputeFieldDiffs_MultipleDiffs` — returns list of specific diffs.
13. `TestTagsMatch_NilAndEmpty` — treats nil and empty as equivalent.
14. `TestTagsMatch_IgnoresPraxisTags` — praxis:managed-key in observed does not
    cause mismatch.
15. `TestComputeFieldDiffs_IgnoresPraxisTags` — praxis:* entries excluded from
    tag diffs.
16. `TestComputeFieldDiffs_ImmutableCidrBlock` — reports diff with
    "(immutable, requires replacement)" suffix.
17. `TestComputeFieldDiffs_ImmutableTenancy` — reports diff with
    "(immutable, ignored)" suffix.

### `internal/drivers/vpc/aws_test.go`

Test error classification helpers:
1. `TestIsNotFound_True` — various NotFound error shapes (`InvalidVpcID.NotFound`,
   `InvalidVpcID.Malformed`).
2. `TestIsNotFound_False` — other error types.
3. `TestIsDependencyViolation_True` — `DependencyViolation` error code.
4. `TestIsDependencyViolation_False` — other error types.
5. `TestIsInvalidParam_True` — `InvalidParameterValue`, `InvalidParameterCombination`,
   `InvalidVpcRange`, `VpcLimitExceeded`.
6. `TestIsInvalidParam_False` — other error types.
7. `TestIsCidrConflict_True` — `CidrConflict`, `InvalidVpc.Range`.
8. `TestIsCidrConflict_False` — other error types.
9. `TestFindByManagedKey_Found` — returns VPC ID when exactly one tag filter match.
10. `TestFindByManagedKey_NotFound` — returns `""` when no match.
11. `TestFindByManagedKey_MultipleMatchesReturnsError` — returns ownership
    corruption error when >1 VPCs match.

### `internal/core/provider/vpc_adapter_test.go`

Test cases (follow `ec2_adapter_test.go` patterns):
1. `TestVPCAdapter_DecodeSpecAndBuildKey` — parses JSON doc, returns `region~name` key.
2. `TestVPCAdapter_BuildImportKey` — returns `region~vpcId` key.
3. `TestVPCAdapter_Kind` — returns `VPC`.
4. `TestVPCAdapter_Scope` — returns `KeyScopeRegion`.
5. `TestVPCAdapter_NormalizeOutputs` — converts struct to map.
6. `TestVPCAdapter_DecodeSpec_MissingRegion` — returns error.
7. `TestVPCAdapter_DecodeSpec_MissingCidrBlock` — returns error.
8. `TestVPCAdapter_DecodeSpec_SetsNameTag` — auto-sets Name tag from metadata.name.
9. `TestVPCAdapter_DecodeSpec_DefaultsTenancy` — sets instanceTenancy to "default"
   when not specified.
10. `TestVPCAdapter_Plan_NoState_ReturnsCreate` — OpCreate when GetOutputs is empty.
11. `TestVPCAdapter_Plan_ExistingVpc_ReturnsDiffs` — OpUpdate with field diffs.
12. `TestVPCAdapter_Plan_ExistingVpc_NoChanges` — OpNoOp when spec matches.

---

## Step 12 — Integration Tests

**File**: `tests/integration/vpc_driver_test.go`

These tests use Testcontainers (Restate) + LocalStack (VPC emulation) — same
pattern as the S3, SG, and EC2 integration tests.

### Setup Helper

```go
func setupVPCDriver(t *testing.T) (*ingress.Client, *ec2sdk.Client) {
    t.Helper()
    configureLocalAccount(t)

    awsCfg := localstackAWSConfig(t)
    ec2Client := awsclient.NewEC2Client(awsCfg)
    driver := vpc.NewVPCDriver(nil)

    env := restatetest.Start(t, restate.Reflect(driver))
    return env.Ingress(), ec2Client
}

// uniqueVpcName generates a unique VPC name for each test.
func uniqueVpcName(t *testing.T) string {
    t.Helper()
    name := strings.ReplaceAll(t.Name(), "/", "-")
    name = strings.ReplaceAll(name, "_", "-")
    if len(name) > 50 {
        name = name[:50]
    }
    return fmt.Sprintf("%s-%d", name, time.Now().UnixNano()%100000)
}

// uniqueCidr generates a unique /24 CIDR block for each test to avoid
// conflicts. Uses 10.x.y.0/24 with random x and y values.
func uniqueCidr(t *testing.T) string {
    t.Helper()
    x := time.Now().UnixNano() % 256
    y := (time.Now().UnixNano() / 256) % 256
    return fmt.Sprintf("10.%d.%d.0/24", x, y)
}
```

### Test Cases

1. **TestVPCProvision_CreatesRealVpc** — Provisions a VPC with a /24 CIDR block,
   verifies it appears in DescribeVpcs. Checks DNS settings are applied correctly.

2. **TestVPCProvision_Idempotent** — Two provisions with same spec, same outputs
   returned. Second provision does not create a new VPC.

3. **TestVPCProvision_WithDnsHostnames** — Provisions VPC with
   enableDnsHostnames=true, verifies DNS hostnames are enabled via
   DescribeVpcAttribute.

4. **TestVPCProvision_WithDedicatedTenancy** — Provisions VPC with dedicated
   tenancy, verifies InstanceTenancy in outputs.

5. **TestVPCImport_ExistingVpc** — Creates VPC directly via EC2 SDK, imports via
   driver, verifies outputs match.

6. **TestVPCDelete_DeletesVpc** — Provisions, deletes, verifies VPC no longer
   exists in DescribeVpcs.

7. **TestVPCDelete_DependencyViolation** — Provisions VPC, creates a subnet in
   it via EC2 SDK, attempts delete, verifies dependency violation error.

8. **TestVPCReconcile_DetectsTagDrift** — Provisions, manually changes tags via
   EC2 API, triggers reconcile, verifies tags are corrected.

9. **TestVPCReconcile_DetectsDnsHostnamesDrift** — Provisions with DNS hostnames
   enabled, manually disables via EC2 API, triggers reconcile, verifies correction.

10. **TestVPCGetStatus_ReturnsReady** — Provisions, calls GetStatus, verifies
    Ready + ModeManaged.

11. **TestVPCOutputs_VpcIdAvailableForDownstream** — Provisions VPC, reads
    GetOutputs, verifies vpcId is non-empty and matches DescribeVpcs.

### LocalStack VPC Compatibility Note

LocalStack's VPC emulation is comprehensive for basic operations: CreateVpc,
DescribeVpcs, DeleteVpc, ModifyVpcAttribute, CreateTags, DescribeVpcAttribute.
VPC operations are among the best-emulated services in LocalStack. DNS attribute
modification is supported. Default VPC handling may differ from real AWS.
Integration tests should focus on the full CRUD lifecycle, DNS attribute management,
and tag drift correction.

---

## VPC-Specific Design Decisions

### 1. Key Strategy: `region~metadata.name`

See [Key Strategy §2](#2-key-strategy) for the full analysis. Summary:

- **The Virtual Object key is `region~metadata.name`**, always and permanently.
- Template authors **must** give each VPC a unique name within its region.
- The `Name` tag is automatically set from `metadata.name` by the adapter.
- After provisioning, the AWS VPC ID is stored in `state.Outputs.VpcId`.
- **Import uses a separate key**: `region~vpcId`, creating a separate VO.

**Why not `region~vpcId`**: Same reasoning as EC2 — Restate VOs are immutable once
keyed. The VPC ID is assigned by AWS at creation time and is unavailable when
`BuildKey` runs at plan/dispatch time.

**Why not `region~cidrBlock`**: CIDR blocks are not unique within a region — multiple
VPCs can use the same CIDR. Using CIDR as a key component would cause accidental
merging of unrelated VPC lifecycles. Additionally, CIDR contains `/` characters
which are problematic in URL path components.

### 2. Immutable vs. Mutable Attributes

| Attribute | Mutable? | How? | Drift Checked? |
|---|---|---|---|
| `enableDnsHostnames` | Yes | ModifyVpcAttribute (live, no downtime) | **Yes** |
| `enableDnsSupport` | Yes | ModifyVpcAttribute (live, no downtime) | **Yes** |
| `tags` | Yes | CreateTags / DeleteTags (live) | **Yes** |
| `cidrBlock` | **No** | Requires VPC replacement (destroy + recreate) | **No** |
| `instanceTenancy` | **No** | Cannot change "dedicated" → "default" (AWS restriction) | **No** |

The drift engine only checks **mutable attributes** to avoid false positives.

**Immutable field changes in plan output**: When an immutable field changes between
the desired spec and observed state, `ComputeFieldDiffs` reports it as a diff entry
with the path suffixed with `(immutable, requires replacement)` for CIDR block
or `(immutable, ignored)` for tenancy. This makes the change visible in
`praxis plan` output without triggering correction.

**CIDR block vs. tenancy distinction**: CIDR block changes are surfaced as
"requires replacement" because in practice a user who changes the CIDR wants
a new VPC with a different IP range. Tenancy changes are surfaced as "ignored"
because the user likely doesn't intend to recreate the VPC just for tenancy.
Neither triggers automatic action — both are informational only. Replacement
semantics (automatic delete + recreate) are not implemented.

### 3. Deletion: Dependency Violation Handling

VPC deletion is conditional on all dependent resources being removed first. The
AWS API returns a `DependencyViolation` error if any of the following still exist:

- Subnets (non-default)
- Internet gateways (attached)
- NAT gateways
- VPN gateways (attached)
- Security groups (non-default)
- Network interfaces
- Route tables (non-main)
- VPC peering connections
- VPC endpoints

**Driver behavior**: The Delete handler surfaces `DependencyViolation` as a terminal
error (409) with a descriptive message. The driver does NOT attempt automatic cascade
deletion. This is intentional:

1. **DAG ordering should handle it**: In a compound template, the orchestrator's DAG
   scheduler processes deletions in reverse topological order. Subnets, IGWs, and
   security groups should be deleted before the VPC. If the DAG is correct, the
   dependency violation never fires.

2. **Manual resources are the user's problem**: If a user manually created subnets
   or other resources inside a Praxis-managed VPC, those resources are outside the
   DAG and block deletion. The error message tells the user to clean up manually.

3. **Cascade deletion is dangerous**: Automatically deleting all resources inside a
   VPC is a destructive operation that could terminate running instances, drop
   database connections, and break network paths. This should never happen silently.

### 4. Secondary CIDR Blocks: Not Supported

AWS supports adding up to 4 secondary CIDR blocks to an existing VPC. This is a
common pattern for expanding IP address space when the primary CIDR is exhausted.

**Rationale for exclusion**:
- Adds complexity to the spec (array of secondary CIDRs), drift detection (set
  comparison), and correction logic (add/remove operations).
- The primary CIDR covers the majority of use cases.
- Secondary CIDRs can be managed out-of-band if needed.

Adding support would require a `secondaryCidrBlocks: [...string]` field in the spec,
sorted-list drift detection, and `AssociateVpcCidrBlock` / `DisassociateVpcCidrBlock`
correction calls.

### 5. IPv6: Not Supported

AWS supports associating IPv6 CIDR blocks with VPCs (Amazon-provided or BYOIP).
IPv6 adds significant complexity:

- Amazon-provided IPv6 CIDRs are assigned by AWS (not user-specified)
- BYOIP requires pre-allocated address pools
- IPv6 CIDR association is mutable but has different semantics than IPv4
- Subnet-level IPv6 association is a separate concern

IPv6 support targets a narrow set of use cases. The vast majority of VPC
deployments use IPv4 only.

### 6. Default VPC Protection

Every AWS region has one default VPC created automatically by AWS. The default VPC:
- Has a default subnet in every AZ
- Has an internet gateway
- Has a main route table with a route to the internet gateway
- Uses CIDR 172.31.0.0/16

Destroying the default VPC can break services that implicitly depend on it (EC2
instances launched without specifying a subnet, Lambda functions, etc.). While AWS
provides `CreateDefaultVpc` to recreate it, the operation is disruptive.

**Driver behavior**: The Delete handler checks `state.Observed.IsDefault`. If true,
it returns a terminal error (409) refusing to delete. The user must delete the
default VPC manually via the AWS console if they truly intend to do so.

**Import behavior**: Importing the default VPC is allowed (for read-only monitoring
via `ModeObserved`). The default VPC guard only activates on Delete.

### 7. DNS Settings Cross-Field Dependency

AWS enforces a constraint: `enableDnsHostnames = true` requires
`enableDnsSupport = true`. The reverse is not true — DNS support can be enabled
without DNS hostnames.

**Validation layers**:
1. **CUE schema**: Cannot express cross-field constraints in the current validation
   pipeline. The schema validates each field independently.
2. **Adapter `decodeSpec`**: Does not validate cross-field constraints — this is
   delegated to the driver for consistency with the EC2 pattern.
3. **Driver `Provision` handler**: Performs the cross-field check before calling any
   AWS APIs. Returns terminal error 400 if `enableDnsHostnames=true` and
   `enableDnsSupport=false`.
4. **Drift correction**: Applies DNS changes in the correct order — enable support
   before enabling hostnames, disable hostnames before disabling support.

### 8. ARN Construction: Available Immediately

Unlike the EC2 driver (which does not populate the ARN because it requires the AWS
account ID via STS), the VPC driver constructs the ARN at provision time because
`DescribeVpcs` returns the `OwnerId` field (which is the AWS account ID).

**ARN format**: `arn:aws:ec2:<region>:<account-id>:vpc/<vpc-id>`

This means `${resources.my-vpc.outputs.arn}` is available for downstream CEL
expressions from the first provision. No STS `GetCallerIdentity` call needed.

### 9. VPC State Machine

VPC states are simpler than EC2:

| State | Meaning | Drift Checked? |
|---|---|---|
| `pending` | VPC is being created | **No** (skip until available) |
| `available` | VPC is ready for use | **Yes** |

There is no "terminated", "stopping", or "shutting-down" equivalent for VPCs.
A deleted VPC simply disappears from DescribeVpcs (returns `InvalidVpcID.NotFound`).

The drift engine skips VPCs in `pending` state (creation in progress). All other
operations (Provision, Delete, Reconcile) handle the `pending` → `available`
transition via `WaitUntilAvailable`.

### 10. Ownership Tags and Conflict Detection

Follows the EC2 driver's ownership tag pattern exactly:

| What | Value |
|---|---|
| Tag key | `praxis:managed-key` |
| Tag value | `<region>~<metadata.name>` (the Restate VO key) |
| Written by | `CreateVpc` → `TagSpecifications` (always) |
| Never removed by | drift correction (`UpdateTags` skips this key) |
| Checked by | `Provision` pre-flight via `FindByManagedKey` (only when VO state is empty) |

Same scope of protection and limitations as EC2 — see EC2 Design Decisions §10.

### 11. VPC as a DAG Root Node

In compound templates, the VPC is typically the root node in the dependency graph —
subnets depend on the VPC, security groups depend on the VPC, instances depend on
subnets and security groups, etc. This means:

- **During apply**: VPC is provisioned first. All downstream resources wait for
  `${resources.my-vpc.outputs.vpcId}` to resolve.
- **During delete**: VPC is deleted last. The DAG scheduler reverses the topological
  order, deleting instances → security groups → subnets → VPC.

If the DAG ordering is correct, the VPC's `DependencyViolation` guard should never
fire during normal orchestrated deletions. It serves as a safety net for manual
operations or broken DAGs.

### 12. DescribeVpc API Call Count

A full `DescribeVpc` observation requires 3 AWS API calls:

1. `DescribeVpcs` — returns VPC metadata, CIDR, tenancy, tags, owner, state
2. `DescribeVpcAttribute(EnableDnsHostnames)` — returns DNS hostname setting
3. `DescribeVpcAttribute(EnableDnsSupport)` — returns DNS support setting

This is an unavoidable AWS API design constraint — DNS settings are not included
in the `DescribeVpcs` response. The rate limiter manages burst. At the default
5-minute reconcile interval, this adds 3 API calls per VPC per 5 minutes, which
is negligible.

**Batch optimization**: For environments managing hundreds of VPCs, batch-describing
by tag filter (all VPCs with `praxis:managed-key` present) instead of one-by-one
would reduce API call volume.

---

## Design Decisions (Resolved)

1. **Should `FindByManagedKey` filter by VPC state?**
   No. Unlike EC2 instances (which can be "terminated" but still appear in
   DescribeInstances for up to an hour), deleted VPCs immediately disappear from
   DescribeVpcs. There is no equivalent of `instance-state-name` filter needed.
   All VPCs returned by the tag filter query are "live" (pending or available).

2. **Should the driver manage the default route table or default security group?**
   No. The default route table and default security group are created automatically
   by AWS when a VPC is created. They have special behavior (cannot be deleted,
   always exist). Managing them is the responsibility of the Route Table and
   Security Group drivers respectively. The VPC driver's scope is the VPC resource
   itself.

3. **Should the driver enable VPC Flow Logs?**
   No. VPC Flow Logs are a monitoring/logging feature, not a core VPC configuration.
   They would be better modeled as a separate resource type (or as a CloudWatch
   driver feature). Including flow logs in the VPC driver would expand its scope
   beyond infrastructure provisioning.

4. **Should import of the default VPC set `IsDefault` in the spec?**
   No. `IsDefault` is an output-only property — it reflects AWS's designation of
   the VPC, not a user-configurable setting. A user cannot create "a default VPC"
   via CreateVpc (that requires `CreateDefaultVpc`, a separate API). The spec
   intentionally omits `isDefault`. The Import handler captures it in
   `ObservedState` and `Outputs` only.

5. **When a template-managed VO and an imported VO point at the same VPC, which is authoritative?**
   Same answer as EC2: operator responsibility. Use import for read-only
   observability (`ModeObserved`, the default) and templates for active lifecycle
   management. Never both simultaneously. The import default of `ModeObserved`
   reduces the risk of accidental deletion via the Delete-mode guard.

6. **VPC CIDR validation: should the driver validate RFC 1918 ranges?**
   No. AWS allows non-RFC 1918 CIDR blocks for VPCs (e.g., 100.64.0.0/10 CGNAT
   range). The driver validates CIDR format via regex in the CUE schema and
   delegates range validation to AWS, which rejects invalid ranges with a clear
   error message. Restricting to RFC 1918 would be overly opinionated.

7. **LocalStack VPC integration test scope:**
   Integration tests cover: create, idempotent re-provision, import, delete,
   dependency violation handling, and DNS attribute drift correction. Tag drift
   correction is covered in integration tests. DNS attribute changes via
   `ModifyVpcAttribute` are well-supported by LocalStack.

---

## Example Template

A complete VPC template demonstrating the resource document format:

```cue
import "praxis.io/schemas/aws/vpc"

resources: {
    "production-vpc": vpc.#VPC & {
        apiVersion: "praxis.io/v1"
        kind:       "VPC"

        metadata: {
            name: "production-vpc"
            labels: {
                environment: "production"
                team:        "platform"
            }
        }

        spec: {
            region:             "${cel:variables.region}"
            cidrBlock:          "10.0.0.0/16"
            enableDnsHostnames: true
            enableDnsSupport:   true
            instanceTenancy:    "default"
            tags: {
                Environment: "production"
                ManagedBy:   "praxis"
            }
        }
    }
}

variables: {
    region: string | *"us-east-1"
}
```

A compound template showing the VPC as a DAG root with downstream dependencies:

```cue
import (
    "praxis.io/schemas/aws/vpc"
    "praxis.io/schemas/aws/sg"
)

resources: {
    "my-vpc": vpc.#VPC & {
        apiVersion: "praxis.io/v1"
        kind:       "VPC"
        metadata: name: "web-vpc"
        spec: {
            region:             "us-east-1"
            cidrBlock:          "10.0.0.0/16"
            enableDnsHostnames: true
            enableDnsSupport:   true
            tags: { Name: "web-vpc" }
        }
    }

    "web-sg": sg.#SecurityGroup & {
        apiVersion: "praxis.io/v1"
        kind:       "SecurityGroup"
        metadata: name: "web-sg"
        spec: {
            // VPC ID resolved from the VPC resource outputs via CEL
            vpcId:       "${cel:resources.my-vpc.outputs.vpcId}"
            groupName:   "web-sg"
            description: "Web server security group"
            ingressRules: [{
                protocol: "tcp"
                fromPort: 443
                toPort:   443
                cidrBlocks: ["0.0.0.0/0"]
            }]
            tags: { Name: "web-sg" }
        }
    }
}
```

---

## Checklist

Use this to track implementation progress:

- [ ] **Schema**: `schemas/aws/vpc/vpc.cue` created
- [ ] **Types**: `internal/drivers/vpc/types.go` created with Spec, Outputs, ObservedState, State
- [ ] **AWS API**: `internal/drivers/vpc/aws.go` created with VPCAPI interface + realVPCAPI
- [ ] **Drift**: `internal/drivers/vpc/drift.go` created with HasDrift, ComputeFieldDiffs
- [ ] **Driver**: `internal/drivers/vpc/driver.go` created with all 6 handlers
- [ ] **Adapter**: `internal/core/provider/vpc_adapter.go` created
- [ ] **Registry**: `internal/core/provider/registry.go` updated with VPC adapter
- [ ] **Entry point**: VPC driver `.Bind()` added to `cmd/praxis-network/main.go`
- [ ] **Docker Compose**: No change needed (VPC joins existing praxis-network service)
- [ ] **Justfile**: Updated with vpc test targets
- [ ] **Unit tests (drift)**: `internal/drivers/vpc/drift_test.go` created
- [ ] **Unit tests (aws helpers)**: `internal/drivers/vpc/aws_test.go` created (includes FindByManagedKey)
- [ ] **Unit tests (driver)**: `internal/drivers/vpc/driver_test.go` created with mocked AWS (includes conflict+import-mode tests)
- [ ] **Unit tests (adapter)**: `internal/core/provider/vpc_adapter_test.go` created
- [ ] **Integration tests**: `tests/integration/vpc_driver_test.go` created
- [ ] **Conflict check**: `FindByManagedKey` in VPCAPI interface + realVPCAPI implementation (with multi-match corruption error)
- [ ] **Ownership tag**: `praxis:managed-key` written by `CreateVpc`; preserved by `UpdateTags`
- [ ] **Import default mode**: `defaultVPCImportMode` returns ModeObserved when unspecified
- [ ] **Delete mode guard**: Delete handler blocks deletion for ModeObserved resources (409)
- [ ] **Default VPC guard**: Delete handler blocks deletion of default VPCs (409)
- [ ] **DNS cross-field validation**: Provision handler validates enableDnsHostnames requires enableDnsSupport
- [ ] **DNS correction ordering**: correctDrift respects enable-support-before-hostnames ordering
- [ ] **ARN construction**: Built from DescribeVpcs OwnerId + region + vpcId (no STS call needed)
- [ ] **Dependency violation handling**: Delete surfaces DependencyViolation as terminal error 409
- [ ] **Build passes**: `go build ./...` succeeds
- [ ] **Unit tests pass**: `go test ./internal/drivers/vpc/... ./internal/core/provider/... -race`
- [ ] **Integration tests pass**: `go test ./tests/integration/ -run TestVPC -tags=integration`
- [ ] **Docker stack runs**: `just up` registers network driver pack (including VPC) with Restate
