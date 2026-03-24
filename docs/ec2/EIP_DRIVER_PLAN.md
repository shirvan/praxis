# Elastic IP Driver — Implementation Plan

> Target: A Restate Virtual Object driver that manages Elastic IP addresses,
> following the exact patterns established by the S3, Security Group, EC2, VPC,
> and EBS drivers.
>
> Key scope: `KeyScopeRegion` — key format is `region~metadata.name`, permanent and
> immutable for the lifetime of the Virtual Object. The AWS-assigned allocation ID
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
12. [Step 9 — Network Driver Pack Entry Point](#step-9--network-driver-pack-entry-point)
13. [Step 10 — Docker Compose & Justfile](#step-10--docker-compose--justfile)
14. [Step 11 — Unit Tests](#step-11--unit-tests)
15. [Step 12 — Integration Tests](#step-12--integration-tests)
16. [EIP-Specific Design Decisions](#eip-specific-design-decisions)
17. [Design Decisions (Resolved)](#design-decisions-resolved)
18. [Checklist](#checklist)

---

## 1. Overview & Scope

The Elastic IP driver manages the lifecycle of Elastic IP **allocations** only.
EIP-to-instance or EIP-to-ENI **associations** are out of scope for this driver —
association is an operational concern handled via compound templates that compose
EIP + EC2 resources, or by a future association driver. This plan focuses
exclusively on allocating, tagging, importing, releasing, and drift-reconciling
Elastic IP addresses.

Elastic IPs are independent AWS resources with their own lifecycle:

- They persist independently of any instance.
- They incur charges when NOT associated with a running instance.
- They can be moved between instances.
- They provide a stable public IP that survives instance stop/start/replacement.

### Driver Contract

| Handler | Context | Purpose |
|---|---|---|
| `Provision` | `ObjectContext` (exclusive) | Allocate or converge an Elastic IP |
| `Import` | `ObjectContext` (exclusive) | Adopt an existing allocation |
| `Delete` | `ObjectContext` (exclusive) | Release an allocation (must be disassociated) |
| `Reconcile` | `ObjectContext` (exclusive) | Detect/correct drift (report-only for Observed mode) |
| `GetStatus` | `ObjectSharedContext` (shared) | Return current status |
| `GetOutputs` | `ObjectSharedContext` (shared) | Return EIP outputs |

### Mutable vs Immutable Attributes

| Attribute | Mutability | Notes |
|---|---|---|
| `domain` | Immutable | Always `"vpc"` (EC2-Classic is deprecated) |
| `publicIp` | Immutable | Assigned by AWS at allocation time |
| `allocationId` | Immutable | Assigned by AWS at allocation time |
| `networkBorderGroup` | Immutable | Set at allocation time (defaults to region) |
| `tags` | Mutable | Full replace via `CreateTags` / `DeleteTags` |

The Elastic IP resource is intentionally simple — the only mutable attribute is
tags. Allocation and release are the primary lifecycle operations.

### Downstream Consumers

```text
${resources.my-eip.outputs.publicIp}       → DNS records, config files, security group rules
${resources.my-eip.outputs.allocationId}   → EC2 instance association, ENI attachment
${resources.my-eip.outputs.arn}            → IAM policies
```

---

## 2. Key Strategy

### Key Format: `region~metadata.name`

The Virtual Object key is always `region~metadata.name`. The allocation ID
(`eipalloc-0abc123...`) is AWS-assigned at allocation time and unavailable at
plan/dispatch time.

1. **BuildKey** (adapter, plan-time): returns `region~metadata.name`.
2. **Provision** (pipeline → workflow → driver): dispatched to same key.
3. **Delete** (pipeline → workflow → driver): dispatched to same key.
4. **Plan** (adapter → describe by allocation ID from state): uses the key to reach
   the Virtual Object, reads the stored allocation ID from state, describes by ID.
5. **Import** (handlers_resource.go): `BuildImportKey(region, resourceID)` returns
   `region~resourceID` where `resourceID` is the allocation ID — **this targets a
   different Virtual Object** intentionally.

### Constraint: metadata.name Must Be Unique Within a Region

Elastic IPs have no AWS-native name. Praxis requires `metadata.name` to be
region-unique for managed EIP resources, consistent with EC2, VPC, and EBS
drivers.

### Conflict Enforcement via Ownership Tags

Following the established pattern:

- **Tag written at allocation**: every `AllocateAddress` call adds the tag
  `praxis:managed-key = <region~metadata.name>`.

- **Pre-flight conflict check**: `Provision` calls `FindByManagedKey` to search
  for addresses already tagged with this key. Returns terminal 409 if found.

- **`FindByManagedKey(ctx, managedKey) (string, error)`** queries by tag filter
  on allocated addresses.

### Import Semantics: Separate Lifecycle Track

- `praxis import --kind ElasticIP --region us-east-1 --resource-id eipalloc-0abc123`:
  Creates VO key `us-east-1~eipalloc-0abc123`.

- Template with `metadata.name: web-eip` in `us-east-1`:
  Creates VO key `us-east-1~web-eip`.

Import defaults to `ModeObserved`. Releasing an EIP that is associated with a
running instance causes immediate loss of the public IP — a disruptive action.

---

## 3. File Inventory

Create or modify these files (✦ = new file, ✎ = modify existing):

```text
✦ internal/drivers/eip/types.go             — Spec, Outputs, ObservedState, State structs
✦ internal/drivers/eip/aws.go               — EIPAPI interface + realEIPAPI implementation
✦ internal/drivers/eip/drift.go             — HasDrift(), ComputeFieldDiffs()
✦ internal/drivers/eip/driver.go            — ElasticIPDriver Virtual Object
✦ internal/drivers/eip/driver_test.go       — Unit tests for driver (mocked AWS)
✦ internal/drivers/eip/aws_test.go          — Unit tests for error classification helpers
✦ internal/drivers/eip/drift_test.go        — Unit tests for drift detection
✦ internal/core/provider/eip_adapter.go     — EIPAdapter implementing provider.Adapter
✦ internal/core/provider/eip_adapter_test.go — Unit tests for EIP adapter
✦ schemas/aws/ec2/eip.cue                   — CUE schema for ElasticIP resource
✦ tests/integration/eip_driver_test.go      — Integration tests (Testcontainers + LocalStack)
✎ cmd/praxis-network/main.go               — Add EIP driver `.Bind()` to network pack
✎ internal/core/provider/registry.go        — Add NewEIPAdapter to NewRegistry()
```

---

## Step 1 — CUE Schema

**File**: `schemas/aws/ec2/eip.cue`

```cue
package ec2

#ElasticIP: {
    apiVersion: "praxis.io/v1"
    kind:       "ElasticIP"

    metadata: {
        name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9._-]{0,254}$"
        labels: [string]: string
    }

    spec: {
        // region is the AWS region to allocate the EIP in.
        region: string

        // domain is always "vpc" — EC2-Classic is fully deprecated.
        domain: "vpc" | *"vpc"

        // networkBorderGroup optionally restricts the EIP to a specific
        // Local Zone or Wavelength Zone. Defaults to the region.
        networkBorderGroup?: string

        // publicIpv4Pool optionally requests from a specific BYOIP pool.
        // Omit for standard AWS-owned IP pool.
        publicIpv4Pool?: string

        // Tags applied to the allocation.
        tags: [string]: string
    }

    outputs?: {
        allocationId:       string
        publicIp:           string
        arn:                string
        domain:             string
        networkBorderGroup: string
    }
}
```

**Key decisions**:

- `domain` defaults to and is constrained to `"vpc"` — EC2-Classic is fully
  deprecated as of August 2023. No reason to support the `"standard"` domain.
- `networkBorderGroup` is optional — most allocations use the default (region).
- `publicIpv4Pool` supports BYOIP scenarios but is optional for the common case.
- No `publicIp` in spec — the IP address is assigned by AWS and appears only
  in outputs. You cannot request a specific IP (except via BYOIP pool).

---

## Step 2 — AWS Client Factory

**File**: `internal/infra/awsclient/client.go` — **NO CHANGES NEEDED**

EIP operations (`AllocateAddress`, `DescribeAddresses`, `ReleaseAddress`) are
methods on the EC2 SDK client. `NewEC2Client` already exists.

---

## Step 3 — Driver Types

**File**: `internal/drivers/eip/types.go`

```go
package eip

import "github.com/shirvan/praxis/pkg/types"

const ServiceName = "ElasticIP"

type ElasticIPSpec struct {
    Account            string            `json:"account,omitempty"`
    Region             string            `json:"region"`
    Domain             string            `json:"domain"`
    NetworkBorderGroup string            `json:"networkBorderGroup,omitempty"`
    PublicIpv4Pool     string            `json:"publicIpv4Pool,omitempty"`
    Tags               map[string]string `json:"tags,omitempty"`
    ManagedKey         string            `json:"managedKey,omitempty"`
}

type ElasticIPOutputs struct {
    AllocationId       string `json:"allocationId"`
    PublicIp           string `json:"publicIp"`
    ARN                string `json:"arn"`
    Domain             string `json:"domain"`
    NetworkBorderGroup string `json:"networkBorderGroup"`
}

type ObservedState struct {
    AllocationId       string            `json:"allocationId"`
    PublicIp           string            `json:"publicIp"`
    Domain             string            `json:"domain"`
    NetworkBorderGroup string            `json:"networkBorderGroup"`
    AssociationId      string            `json:"associationId,omitempty"` // non-empty if associated
    InstanceId         string            `json:"instanceId,omitempty"`   // associated instance
    Tags               map[string]string `json:"tags"`
}

type ElasticIPState struct {
    Desired            ElasticIPSpec        `json:"desired"`
    Observed           ObservedState        `json:"observed"`
    Outputs            ElasticIPOutputs     `json:"outputs"`
    Status             types.ResourceStatus `json:"status"`
    Mode               types.Mode           `json:"mode"`
    Error              string               `json:"error,omitempty"`
    Generation         int64                `json:"generation"`
    LastReconcile      string               `json:"lastReconcile,omitempty"`
    ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
```

**Why `AssociationId` and `InstanceId` in ObservedState**: These are read-only
observations. The driver does not manage associations, but recording them allows
Reconcile to report whether the EIP is in use and the Delete handler to provide
a meaningful error message when release fails due to an active association.

---

## Step 4 — AWS API Abstraction Layer

**File**: `internal/drivers/eip/aws.go`

### EIPAPI Interface

```go
type EIPAPI interface {
    // AllocateAddress allocates a new Elastic IP.
    // Returns the allocation ID and public IP.
    AllocateAddress(ctx context.Context, spec ElasticIPSpec) (allocationId, publicIp string, err error)

    // DescribeAddress returns the observed state of an allocation.
    DescribeAddress(ctx context.Context, allocationId string) (ObservedState, error)

    // ReleaseAddress releases an Elastic IP allocation.
    // Fails if the EIP is currently associated with an instance/ENI.
    ReleaseAddress(ctx context.Context, allocationId string) error

    // UpdateTags replaces all user tags on the allocation.
    // Preserves praxis:* system tags.
    UpdateTags(ctx context.Context, allocationId string, tags map[string]string) error

    // FindByManagedKey searches for allocations tagged with
    // praxis:managed-key=managedKey.
    FindByManagedKey(ctx context.Context, managedKey string) (string, error)
}
```

### realEIPAPI Implementation

```go
type realEIPAPI struct {
    client  *ec2sdk.Client
    limiter *ratelimit.Limiter
}

func NewEIPAPI(client *ec2sdk.Client) EIPAPI {
    return &realEIPAPI{
        client:  client,
        limiter: ratelimit.New("elastic-ip", 20, 10),
    }
}
```

### Key Implementation Details

#### `AllocateAddress`

```go
func (r *realEIPAPI) AllocateAddress(ctx context.Context, spec ElasticIPSpec) (string, string, error) {
    input := &ec2sdk.AllocateAddressInput{
        Domain: ec2types.DomainTypeVpc,
    }
    if spec.NetworkBorderGroup != "" {
        input.NetworkBorderGroup = aws.String(spec.NetworkBorderGroup)
    }
    if spec.PublicIpv4Pool != "" {
        input.PublicIpv4Pool = aws.String(spec.PublicIpv4Pool)
    }

    // Apply tags at allocation time
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
        ResourceType: ec2types.ResourceTypeElasticIp,
        Tags:         ec2Tags,
    }}

    out, err := r.client.AllocateAddress(ctx, input)
    if err != nil {
        return "", "", err
    }
    return aws.ToString(out.AllocationId), aws.ToString(out.PublicIp), nil
}
```

#### `DescribeAddress`

```go
func (r *realEIPAPI) DescribeAddress(ctx context.Context, allocationId string) (ObservedState, error) {
    out, err := r.client.DescribeAddresses(ctx, &ec2sdk.DescribeAddressesInput{
        AllocationIds: []string{allocationId},
    })
    if err != nil {
        return ObservedState{}, err
    }
    if len(out.Addresses) == 0 {
        return ObservedState{}, fmt.Errorf("elastic IP %s not found", allocationId)
    }
    addr := out.Addresses[0]

    obs := ObservedState{
        AllocationId:       aws.ToString(addr.AllocationId),
        PublicIp:           aws.ToString(addr.PublicIp),
        Domain:             string(addr.Domain),
        NetworkBorderGroup: aws.ToString(addr.NetworkBorderGroup),
        AssociationId:      aws.ToString(addr.AssociationId),
        InstanceId:         aws.ToString(addr.InstanceId),
        Tags:               make(map[string]string, len(addr.Tags)),
    }
    for _, tag := range addr.Tags {
        obs.Tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
    }
    return obs, nil
}
```

#### `ReleaseAddress`

```go
func (r *realEIPAPI) ReleaseAddress(ctx context.Context, allocationId string) error {
    _, err := r.client.ReleaseAddress(ctx, &ec2sdk.ReleaseAddressInput{
        AllocationId: aws.String(allocationId),
    })
    return err
}
```

#### `FindByManagedKey`

```go
func (r *realEIPAPI) FindByManagedKey(ctx context.Context, managedKey string) (string, error) {
    out, err := r.client.DescribeAddresses(ctx, &ec2sdk.DescribeAddressesInput{
        Filters: []ec2types.Filter{
            {Name: aws.String("tag:praxis:managed-key"), Values: []string{managedKey}},
        },
    })
    if err != nil {
        return "", err
    }

    var matches []string
    for _, addr := range out.Addresses {
        if id := aws.ToString(addr.AllocationId); id != "" {
            matches = append(matches, id)
        }
    }

    switch len(matches) {
    case 0:
        return "", nil
    case 1:
        return matches[0], nil
    default:
        return "", fmt.Errorf(
            "ownership corruption: %d allocations claim managed-key %q: %v; "+
                "manual intervention required",
            len(matches), managedKey, matches,
        )
    }
}
```

### Error Classification Helpers

```go
func IsNotFound(err error) bool {
    if err == nil {
        return false
    }
    var apiErr smithy.APIError
    if errors.As(err, &apiErr) {
        code := apiErr.ErrorCode()
        return code == "InvalidAllocationID.NotFound" ||
               code == "InvalidAddressID.NotFound"
    }
    errText := err.Error()
    return strings.Contains(errText, "InvalidAllocationID.NotFound")
}

func IsAssociationExists(err error) bool {
    if err == nil {
        return false
    }
    var apiErr smithy.APIError
    if errors.As(err, &apiErr) {
        return apiErr.ErrorCode() == "InvalidIPAddress.InUse"
    }
    errText := err.Error()
    return strings.Contains(errText, "InvalidIPAddress.InUse") ||
           strings.Contains(errText, "is already associated")
}

func IsAddressLimitExceeded(err error) bool {
    if err == nil {
        return false
    }
    var apiErr smithy.APIError
    if errors.As(err, &apiErr) {
        return apiErr.ErrorCode() == "AddressLimitExceeded"
    }
    return false
}
```

---

## Step 5 — Drift Detection

**File**: `internal/drivers/eip/drift.go`

EIPs have minimal mutable state — only tags can drift. All other attributes
(publicIp, domain, networkBorderGroup) are immutable after allocation.

```go
package eip

func HasDrift(desired ElasticIPSpec, observed ObservedState) bool {
    return !tagsMatch(desired.Tags, observed.Tags)
}

func ComputeFieldDiffs(desired ElasticIPSpec, observed ObservedState) []FieldDiffEntry {
    var diffs []FieldDiffEntry
    diffs = append(diffs, computeTagDiffs(desired.Tags, observed.Tags)...)
    return diffs
}

type FieldDiffEntry struct {
    Path string
    Old  string
    New  string
}
```

---

## Step 6 — Driver Implementation

**File**: `internal/drivers/eip/driver.go`

### Struct & Constructor

```go
type ElasticIPDriver struct {
    auth       *auth.Registry
    apiFactory func(aws.Config) EIPAPI
}

func NewElasticIPDriver(accounts *auth.Registry) *ElasticIPDriver {
    return NewElasticIPDriverWithFactory(accounts, func(cfg aws.Config) EIPAPI {
        return NewEIPAPI(awsclient.NewEC2Client(cfg))
    })
}

func NewElasticIPDriverWithFactory(accounts *auth.Registry, factory func(aws.Config) EIPAPI) *ElasticIPDriver {
    if accounts == nil {
        accounts = auth.LoadFromEnv()
    }
    if factory == nil {
        factory = func(cfg aws.Config) EIPAPI {
            return NewEIPAPI(awsclient.NewEC2Client(cfg))
        }
    }
    return &ElasticIPDriver{auth: accounts, apiFactory: factory}
}

func (d *ElasticIPDriver) ServiceName() string {
    return ServiceName
}
```

### Provision Handler

```go
func (d *ElasticIPDriver) Provision(ctx restate.ObjectContext, spec ElasticIPSpec) (ElasticIPOutputs, error) {
    api, _, err := d.apiForAccount(spec.Account)
    if err != nil {
        return ElasticIPOutputs{}, restate.TerminalError(err, 400)
    }

    if spec.Region == "" {
        return ElasticIPOutputs{}, restate.TerminalError(fmt.Errorf("region is required"), 400)
    }

    state, err := restate.Get[ElasticIPState](ctx, drivers.StateKey)
    if err != nil {
        return ElasticIPOutputs{}, err
    }

    state.Desired = spec
    state.Status = types.StatusProvisioning
    state.Mode = types.ModeManaged
    state.Error = ""
    state.Generation++

    allocationId := state.Outputs.AllocationId

    // Check if allocation already exists (re-provision path)
    if allocationId != "" {
        _, descErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
            obs, err := api.DescribeAddress(rc, allocationId)
            if err != nil {
                if IsNotFound(err) {
                    return ObservedState{}, restate.TerminalError(err, 404)
                }
                return ObservedState{}, err
            }
            return obs, nil
        })
        if descErr != nil {
            allocationId = "" // Allocation gone, reallocate
        }
    }

    // Pre-flight ownership conflict check
    if allocationId == "" && spec.ManagedKey != "" {
        conflictId, conflictErr := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
            return api.FindByManagedKey(rc, spec.ManagedKey)
        })
        if conflictErr != nil {
            return ElasticIPOutputs{}, conflictErr
        }
        if conflictId != "" {
            return ElasticIPOutputs{}, restate.TerminalError(
                fmt.Errorf("elastic IP name %q in this region is already managed by Praxis (allocationId: %s); "+
                    "remove the existing resource or use a different metadata.name", spec.ManagedKey, conflictId),
                409,
            )
        }
    }

    // Allocate if it doesn't exist
    if allocationId == "" {
        result, err := restate.Run(ctx, func(rc restate.RunContext) (allocResult, error) {
            allocId, publicIp, err := api.AllocateAddress(rc, spec)
            if err != nil {
                if IsAddressLimitExceeded(err) {
                    return allocResult{}, restate.TerminalError(err, 503)
                }
                return allocResult{}, err
            }
            return allocResult{allocationId: allocId, publicIp: publicIp}, nil
        })
        if err != nil {
            state.Status = types.StatusError
            state.Error = err.Error()
            restate.Set(ctx, drivers.StateKey, state)
            return ElasticIPOutputs{}, err
        }
        allocationId = result.allocationId
    } else {
        // Re-provision: only tags can change
        if !tagsMatch(spec.Tags, state.Observed.Tags) {
            _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
                return restate.Void{}, api.UpdateTags(rc, allocationId, spec.Tags)
            })
            if err != nil {
                state.Status = types.StatusError
                state.Error = err.Error()
                restate.Set(ctx, drivers.StateKey, state)
                return ElasticIPOutputs{}, err
            }
        }
    }

    // Describe final state
    observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
        return api.DescribeAddress(rc, allocationId)
    })
    if err != nil {
        state.Status = types.StatusError
        state.Error = err.Error()
        restate.Set(ctx, drivers.StateKey, state)
        return ElasticIPOutputs{}, err
    }

    outputs := outputsFromObserved(observed)
    state.Observed = observed
    state.Outputs = outputs
    state.Status = types.StatusReady
    restate.Set(ctx, drivers.StateKey, state)
    d.scheduleReconcile(ctx, &state)
    return outputs, nil
}
```

### Delete Handler

```go
func (d *ElasticIPDriver) Delete(ctx restate.ObjectContext) error {
    state, err := restate.Get[ElasticIPState](ctx, drivers.StateKey)
    if err != nil {
        return err
    }
    if state.Status == types.StatusDeleted {
        return nil
    }

    // Mode guard
    if state.Mode == types.ModeObserved {
        return restate.TerminalError(
            fmt.Errorf("cannot release Elastic IP in Observed mode; change to Managed mode first"), 409)
    }

    allocationId := state.Outputs.AllocationId
    if allocationId == "" {
        state.Status = types.StatusDeleted
        restate.Set(ctx, drivers.StateKey, state)
        return nil
    }

    state.Status = types.StatusDeleting
    restate.Set(ctx, drivers.StateKey, state)

    api, _, err := d.apiForAccount(state.Desired.Account)
    if err != nil {
        return restate.TerminalError(err, 400)
    }

    _, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
        err := api.ReleaseAddress(rc, allocationId)
        if err != nil {
            if IsNotFound(err) {
                return restate.Void{}, nil // already gone
            }
            if IsAssociationExists(err) {
                return restate.Void{}, restate.TerminalError(
                    fmt.Errorf("elastic IP %s is still associated with an instance; disassociate before releasing", allocationId), 409)
            }
            return restate.Void{}, err
        }
        return restate.Void{}, nil
    })
    if err != nil {
        state.Status = types.StatusError
        state.Error = err.Error()
        restate.Set(ctx, drivers.StateKey, state)
        return err
    }

    state.Status = types.StatusDeleted
    restate.Set(ctx, drivers.StateKey, state)
    return nil
}
```

### Import, Reconcile, GetStatus, GetOutputs

Follow the established pattern. Import defaults to `ModeObserved`. Reconcile
detects tag drift only — EIPs have no other mutable attributes.

---

## Step 7 — Provider Adapter

**File**: `internal/core/provider/eip_adapter.go`

```go
func (a *EIPAdapter) Kind() string        { return eip.ServiceName }
func (a *EIPAdapter) ServiceName() string  { return eip.ServiceName }
func (a *EIPAdapter) Scope() KeyScope      { return KeyScopeRegion }

func (a *EIPAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
    // region~metadata.name
}

func (a *EIPAdapter) BuildImportKey(region, resourceID string) (string, error) {
    // region~allocationId
}
```

Plan follows the state-driven pattern (GetOutputs → DescribeAddress by allocation ID).

---

## Step 8 — Registry Integration

Add `NewEIPAdapterWithRegistry(accounts)` to `NewRegistry()`.

---

## Step 9 — Network Driver Pack Entry Point

**File**: `cmd/praxis-network/main.go`

Add `.Bind(restate.Reflect(eipDriver))` alongside the existing SG and VPC driver
bindings. Elastic IPs are networking resources — they provide stable public
addressing for VPC-based instances.

---

## Step 10 — Docker Compose & Justfile

No Docker Compose changes needed — EIP joins the existing `praxis-network` service.
Add `test-eip` and `ls-eip` targets to the justfile.

---

## Step 11 — Unit Tests

### `internal/drivers/eip/drift_test.go`

1. `TestHasDrift_NoDrift` — identical tags.
2. `TestHasDrift_TagAdded` — new tag in desired.
3. `TestHasDrift_TagRemoved` — tag removed in desired.
4. `TestHasDrift_TagChanged` — tag value changed.
5. `TestComputeFieldDiffs_Tags` — correct field diff entries for tag changes.

### `internal/drivers/eip/aws_test.go`

1. `TestIsNotFound_True` — InvalidAllocationID.NotFound.
2. `TestIsNotFound_False` — other errors.
3. `TestIsAssociationExists_True` — InvalidIPAddress.InUse.
4. `TestIsAddressLimitExceeded_True` — AddressLimitExceeded.
5. `TestFindByManagedKey_Found` — single match.
6. `TestFindByManagedKey_NotFound` — no match.
7. `TestFindByManagedKey_MultipleMatchesError` — ownership corruption.

### `internal/drivers/eip/driver_test.go`

1. `TestSpecFromObserved_RoundTrip` — import creates matching spec.
2. `TestServiceName` — returns "ElasticIP".
3. `TestOutputsFromObserved` — correct output mapping.

### `internal/core/provider/eip_adapter_test.go`

1. `TestEIPAdapter_DecodeSpecAndBuildKey` — parses JSON doc, returns `region~name` key.
2. `TestEIPAdapter_BuildImportKey` — returns `region~allocationId` key.
3. `TestEIPAdapter_Kind` — returns "ElasticIP".
4. `TestEIPAdapter_Scope` — returns `KeyScopeRegion`.
5. `TestEIPAdapter_NormalizeOutputs` — converts struct to map.

---

## Step 12 — Integration Tests

**File**: `tests/integration/eip_driver_test.go`

1. **TestEIPProvision_AllocatesAddress** — Allocates an EIP, verifies in DescribeAddresses.
2. **TestEIPProvision_Idempotent** — Two provisions produce same allocation.
3. **TestEIPImport_ExistingAllocation** — Allocates via SDK, imports via driver.
4. **TestEIPDelete_ReleasesAddress** — Provisions, deletes, verifies allocation gone.
5. **TestEIPReconcile_DetectsAndFixesTagDrift** — Tag drift correction.
6. **TestEIPGetStatus_ReturnsReady** — Ready + ModeManaged after provision.

---

## EIP-Specific Design Decisions

### 1. Minimal Mutable State

Elastic IPs are one of the simplest AWS resources — the only mutable attribute is
tags. This makes the driver straightforward: Provision allocates (or converges tags),
Delete releases, Reconcile checks tags. There is no equivalent of `ModifyVolume` or
`ModifyInstanceAttribute`.

### 2. Association Is Out of Scope

EIP-to-instance association (`AssociateAddress` / `DisassociateAddress`) is not
managed by this driver. Association is treated as an operational concern:

- Compound templates should use the EC2 driver's outputs + EIP driver's outputs to
  coordinate association (either via a future association driver or user data scripts).
- The EIP driver records `AssociationId` and `InstanceId` in `ObservedState` for
  informational purposes (e.g., Reconcile can report "EIP associated with i-0abc123").
- The Delete handler checks for active associations and returns 409 if the EIP is
  still in use, rather than auto-disassociating.

### 3. Address Limit: 503

The default AWS limit is 5 Elastic IPs per region. Exceeding this returns
`AddressLimitExceeded`. The driver surfaces this as a terminal error (503) — it's a
provider-side constraint, not a user input error, consistent with how EC2
`IsInsufficientCapacity` is handled.

### 4. Driver Pack Placement: praxis-network

Elastic IPs are networking resources — they provide stable public IPv4 addresses.
They belong alongside Security Groups and VPCs in the `praxis-network` driver pack.

### 5. Schema Placement: schemas/aws/ec2/

The CUE schema file is placed in `schemas/aws/ec2/eip.cue` (not a separate
`schemas/aws/eip/` directory) because Elastic IPs are part of the EC2 API namespace.
This matches the Security Group schema at `schemas/aws/ec2/sg.cue`.

### 6. Import Defaults to ModeObserved

Releasing an EIP that is associated with a running instance causes immediate loss of
the public IP. The Observed default prevents accidental release via the Delete mode
guard (409).

---

## Design Decisions (Resolved)

1. **Should the driver auto-disassociate before releasing?**
   No. Auto-disassociation is destructive to the associated instance — it would lose
   its public IP immediately. The driver returns a terminal 409 error explaining that
   the EIP must be disassociated first. This is consistent with EBS (no auto-detach
   before delete) and SG (dependency violation → terminal error).

2. **Should Provision re-allocate if the existing allocation is associated?**
   No. Re-provision on an existing allocation only updates tags. The association
   state is orthogonal to the allocation lifecycle. Provision never allocates a
   second EIP.

3. **BYOIP pool support:**
   The `publicIpv4Pool` field allows specifying a Bring Your Own IP pool. If omitted,
   AWS uses the standard Amazon IP pool. This field is immutable after allocation
   (you can't move an IP between pools). It's not checked in drift detection.

4. **Should Observed mode block both Reconcile corrections and Delete?**
   Yes. Same contract as EC2, VPC, and EBS.

---

## Checklist

- [x] **Schema**: `schemas/aws/ec2/eip.cue` created
- [x] **Types**: `internal/drivers/eip/types.go` created
- [x] **AWS API**: `internal/drivers/eip/aws.go` created
- [x] **Drift**: `internal/drivers/eip/drift.go` created
- [x] **Driver**: `internal/drivers/eip/driver.go` created with all 6 handlers
- [x] **Adapter**: `internal/core/provider/eip_adapter.go` created
- [x] **Registry**: `internal/core/provider/registry.go` updated
- [x] **Entry point**: EIP driver bound in `cmd/praxis-network/main.go`
- [x] **Justfile**: Updated with eip targets
- [x] **Unit tests (drift)**: `internal/drivers/eip/drift_test.go`
- [x] **Unit tests (aws helpers)**: `internal/drivers/eip/aws_test.go`
- [x] **Unit tests (driver)**: `internal/drivers/eip/driver_test.go`
- [x] **Unit tests (adapter)**: `internal/core/provider/eip_adapter_test.go`
- [x] **Integration tests**: `tests/integration/eip_driver_test.go`
- [x] **Conflict check**: `FindByManagedKey` in EIPAPI interface
- [x] **Ownership tag**: `praxis:managed-key` written by `AllocateAddress`
- [x] **Import default mode**: `ModeObserved` when unspecified
- [x] **Delete mode guard**: Delete handler blocks release for ModeObserved (409)
- [x] **Delete association guard**: Delete handler returns 409 for associated EIPs
- [x] **Build passes**: `go build ./...` succeeds
- [x] **Unit tests pass**: `go test ./internal/drivers/eip/... -race`
- [x] **Integration tests pass**: `go test ./tests/integration/ -run TestEIP -tags=integration`
