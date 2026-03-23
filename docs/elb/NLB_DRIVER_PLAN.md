# NLB Driver — Implementation Plan

> Target: A Restate Virtual Object driver that manages Network Load Balancers
> (NLBs), providing full lifecycle management including creation, import, deletion,
> drift detection, and drift correction for load balancer attributes, subnet
> mappings, cross-zone load balancing, and tags.
>
> Key scope: `KeyScopeRegion` — key format is `region~nlbName`, permanent and
> immutable for the lifetime of the Virtual Object. The AWS-assigned load balancer
> ARN lives only in state/outputs.

---

## Table of Contents

1. [Overview & Scope](#1-overview--scope)
2. [Key Strategy](#2-key-strategy)
3. [File Inventory](#3-file-inventory)
4. [Step 1 — CUE Schema](#step-1--cue-schema)
5. [Step 2 — Driver Types](#step-2--driver-types)
6. [Step 3 — AWS API Abstraction Layer](#step-3--aws-api-abstraction-layer)
7. [Step 4 — Drift Detection](#step-4--drift-detection)
8. [Step 5 — Driver Implementation](#step-5--driver-implementation)
9. [Step 6 — Provider Adapter](#step-6--provider-adapter)
10. [Step 7 — Registry Integration](#step-7--registry-integration)
11. [Step 8 — Unit Tests](#step-8--unit-tests)
12. [Step 9 — Integration Tests](#step-9--integration-tests)
13. [NLB-Specific Design Decisions](#nlb-specific-design-decisions)
14. [Checklist](#checklist)

---

## 1. Overview & Scope

The NLB driver manages the lifecycle of **Network Load Balancers** only. It creates,
imports, updates, and deletes NLBs along with their subnet mappings, cross-zone load
balancing configuration, and tags.

NLBs operate at Layer 4 (TCP/UDP/TLS) and are optimized for extremely high
throughput and low latency. They support static IP addresses via Elastic IP
allocation per subnet. In compound templates, NLBs serve the same structural role
as ALBs: downstream of VPC/Subnet resources and upstream of Listener and Target
Group resources.

**Out of scope**: Application Load Balancers (separate driver), Listeners (separate
driver), Listener Rules (separate driver), Target Groups (separate driver).

### Driver Contract

| Handler | Context | Purpose |
|---|---|---|
| `Provision` | `ObjectContext` (exclusive) | Create or converge an NLB |
| `Import` | `ObjectContext` (exclusive) | Adopt an existing NLB |
| `Delete` | `ObjectContext` (exclusive) | Remove an NLB |
| `Reconcile` | `ObjectContext` (exclusive) | Detect/correct drift (report-only for Observed mode) |
| `GetStatus` | `ObjectSharedContext` (shared) | Return current status |
| `GetOutputs` | `ObjectSharedContext` (shared) | Return NLB outputs |

### Mutable vs Immutable Attributes

| Attribute | Mutability | Notes |
|---|---|---|
| `name` | Immutable | Part of the Virtual Object key; cannot change after creation |
| `scheme` | Immutable | `internet-facing` or `internal`; requires delete + recreate to change |
| `ipAddressType` | Mutable | Can switch between `ipv4` and `dualstack` via `SetIpAddressType` |
| `subnets` | Mutable | Updated via `SetSubnets`; minimum 1 AZ required |
| `subnetMappings` | Mutable | Updated via `SetSubnets`; allows EIP allocation per subnet |
| `crossZoneLoadBalancing` | Mutable | Updated via `ModifyLoadBalancerAttributes` |
| `deletionProtection` | Mutable | Updated via `ModifyLoadBalancerAttributes` |
| `tags` | Mutable | Full replace via `RemoveTags` + `AddTags` |

### Downstream Consumers

```
${resources.my-nlb.outputs.loadBalancerArn}   → Listener's loadBalancerArn
${resources.my-nlb.outputs.dnsName}            → Route 53 alias target, application config
${resources.my-nlb.outputs.hostedZoneId}       → Route 53 alias hosted zone ID
```

---

## 2. Key Strategy

### Key Scope: `KeyScopeRegion`

NLBs are regional resources. Load balancer names are unique within a region and
account.

```
region~nlbName
```

### BuildKey vs BuildImportKey

- **`BuildKey(resourceDoc)`**: Extracts `metadata.name` from the resource document.
  Prepends the region. Returns `region~name`.

- **`BuildImportKey(region, resourceID)`**: Returns `region~resourceID`. The
  `resourceID` is the NLB name. Import and template management produce the same key.

### No Ownership Tags

NLB names are unique within a region and account (AWS-enforced).
`CreateLoadBalancer` returns `DuplicateLoadBalancerName` if the name already exists.

---

## 3. File Inventory

```text
✦ schemas/aws/elb/nlb.cue                         — CUE schema for NLB resource
✦ internal/drivers/nlb/types.go                    — Spec, Outputs, ObservedState, State structs
✦ internal/drivers/nlb/aws.go                      — NLBAPI interface + realNLBAPI implementation
✦ internal/drivers/nlb/drift.go                    — HasDrift(), ComputeFieldDiffs()
✦ internal/drivers/nlb/driver.go                   — NLBDriver Virtual Object
✦ internal/drivers/nlb/driver_test.go              — Unit tests for driver (mocked AWS)
✦ internal/drivers/nlb/aws_test.go                 — Unit tests for error classification
✦ internal/drivers/nlb/drift_test.go               — Unit tests for drift detection
✦ internal/core/provider/nlb_adapter.go            — NLBAdapter implementing provider.Adapter
✦ internal/core/provider/nlb_adapter_test.go       — Unit tests for adapter
✦ tests/integration/nlb_driver_test.go             — Integration tests
✎ internal/core/provider/registry.go               — Add NewNLBAdapter to NewRegistry()
```

---

## Step 1 — CUE Schema

**File**: `schemas/aws/elb/nlb.cue`

```cue
package elb

#NLB: {
    apiVersion: "praxis.io/v1"
    kind:       "NLB"

    metadata: {
        name: string & =~"^[a-zA-Z0-9]([a-zA-Z0-9-]{0,30}[a-zA-Z0-9])?$"
        labels: [string]: string
    }

    spec: {
        // name is the load balancer name in AWS.
        name: string & =~"^[a-zA-Z0-9]([a-zA-Z0-9-]{0,30}[a-zA-Z0-9])?$"

        // account is the target AWS account alias (optional).
        account?: string

        // scheme is "internet-facing" or "internal". Immutable after creation.
        scheme: "internet-facing" | "internal" | *"internet-facing"

        // ipAddressType is "ipv4" or "dualstack".
        ipAddressType: "ipv4" | "dualstack" | *"ipv4"

        // subnets is a list of subnet IDs.
        // Mutually exclusive with subnetMappings.
        subnets?: [...string] & [_, ...]

        // subnetMappings allows per-subnet EIP allocation (static IPs).
        // Mutually exclusive with subnets.
        subnetMappings?: [...#SubnetMapping] & [_, ...]

        // crossZoneLoadBalancing enables cross-zone load balancing.
        // NLBs default to disabled (unlike ALBs where it's always on).
        crossZoneLoadBalancing: bool | *false

        // deletionProtection prevents accidental deletion.
        deletionProtection: bool | *false

        // tags applied to the NLB.
        tags: [string]: string
    }

    outputs?: {
        loadBalancerArn: string
        dnsName:         string
        hostedZoneId:    string
        vpcId:           string
        canonicalHostedZoneId: string
    }
}
```

### Key Design Decisions

- **No `securityGroups`**: NLBs do not support security group associations (traffic
  flows directly to targets). This is the primary structural difference from ALBs.

- **No `idleTimeout` or `accessLogs`**: These are ALB-only attributes. NLBs have
  different timeout behavior (connection idle timeout is fixed at 350s for TCP).

- **`crossZoneLoadBalancing` defaults to false**: Unlike ALBs where cross-zone is
  always enabled, NLBs allow per-target-group or per-LB cross-zone configuration.
  Default off matches AWS default.

- **`subnetMappings` with EIP**: The primary reason to use NLBs is static IP
  addresses. `subnetMappings` allows allocating an Elastic IP per subnet for
  stable ingress IPs.

---

## Step 2 — Driver Types

**File**: `internal/drivers/nlb/types.go`

```go
package nlb

import "github.com/shirvan/praxis/pkg/types"

const ServiceName = "NLB"

type NLBSpec struct {
    Account                string            `json:"account,omitempty"`
    Name                   string            `json:"name"`
    Scheme                 string            `json:"scheme"`
    IpAddressType          string            `json:"ipAddressType"`
    Subnets                []string          `json:"subnets,omitempty"`
    SubnetMappings         []SubnetMapping   `json:"subnetMappings,omitempty"`
    CrossZoneLoadBalancing bool              `json:"crossZoneLoadBalancing"`
    DeletionProtection     bool              `json:"deletionProtection"`
    Tags                   map[string]string `json:"tags,omitempty"`
}

type SubnetMapping struct {
    SubnetId     string `json:"subnetId"`
    AllocationId string `json:"allocationId,omitempty"`
}

type NLBOutputs struct {
    LoadBalancerArn       string `json:"loadBalancerArn"`
    DnsName               string `json:"dnsName"`
    HostedZoneId          string `json:"hostedZoneId"`
    VpcId                 string `json:"vpcId"`
    CanonicalHostedZoneId string `json:"canonicalHostedZoneId"`
}

type ObservedState struct {
    LoadBalancerArn       string            `json:"loadBalancerArn"`
    DnsName               string            `json:"dnsName"`
    HostedZoneId          string            `json:"hostedZoneId"`
    Name                  string            `json:"name"`
    Scheme                string            `json:"scheme"`
    VpcId                 string            `json:"vpcId"`
    IpAddressType         string            `json:"ipAddressType"`
    Subnets               []string          `json:"subnets"`
    SubnetMappings        []SubnetMapping   `json:"subnetMappings"`
    CrossZoneLoadBalancing bool             `json:"crossZoneLoadBalancing"`
    DeletionProtection    bool              `json:"deletionProtection"`
    State                 string            `json:"state"`
    Tags                  map[string]string `json:"tags"`
}

type NLBState struct {
    Desired            NLBSpec              `json:"desired"`
    Observed           ObservedState        `json:"observed"`
    Outputs            NLBOutputs           `json:"outputs"`
    Status             types.ResourceStatus `json:"status"`
    Mode               types.Mode           `json:"mode"`
    Error              string               `json:"error,omitempty"`
    Generation         int64                `json:"generation"`
    LastReconcile      string               `json:"lastReconcile,omitempty"`
    ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
```

---

## Step 3 — AWS API Abstraction Layer

**File**: `internal/drivers/nlb/aws.go`

### NLBAPI Interface

```go
type NLBAPI interface {
    CreateNLB(ctx context.Context, spec NLBSpec) (arn, dnsName, hostedZoneId, vpcId string, err error)
    DescribeNLB(ctx context.Context, arn string) (ObservedState, error)
    FindNLB(ctx context.Context, name string) (ObservedState, error)
    DeleteNLB(ctx context.Context, arn string) error
    SetSubnets(ctx context.Context, arn string, subnets []SubnetMapping) error
    SetIpAddressType(ctx context.Context, arn string, ipAddressType string) error
    ModifyAttributes(ctx context.Context, arn string, attrs map[string]string) error
    UpdateTags(ctx context.Context, arn string, desired map[string]string) error
}
```

### Key Differences from ALB

- **No `SetSecurityGroups`**: NLBs don't support security group associations.
- **Subnet mappings with EIP**: `SetSubnets` must handle `AllocationId` for Elastic
  IP bindings. Note: changing subnet EIP mappings after creation may require
  delete+recreate for the affected subnet zone.
- **Cross-zone attribute**: Modified via `ModifyLoadBalancerAttributes` with key
  `load_balancing.cross_zone.enabled`.

### Error Classification

Same error codes as ALB (both use the ELBv2 API):

| Function | AWS Error Code(s) | Semantics |
|---|---|---|
| `IsNotFound` | `LoadBalancerNotFound` | NLB doesn't exist |
| `IsDuplicate` | `DuplicateLoadBalancerName` | NLB name already exists |
| `IsTooMany` | `TooManyLoadBalancers` | Account quota exceeded |

---

## Step 4 — Drift Detection

**File**: `internal/drivers/nlb/drift.go`

### Drift Comparison Fields

| Field | Comparison Strategy |
|---|---|
| `ipAddressType` | String equality |
| `subnets` | Sorted set comparison |
| `crossZoneLoadBalancing` | Bool equality |
| `deletionProtection` | Bool equality |
| `tags` | Map equality |

Immutable fields (`name`, `scheme`) are not compared for drift.

---

## Step 5 — Driver Implementation

**File**: `internal/drivers/nlb/driver.go`

### NLBDriver Struct

```go
type NLBDriver struct {
    accounts *auth.Registry
}

func NewNLBDriver(accounts *auth.Registry) *NLBDriver {
    return &NLBDriver{accounts: accounts}
}

func (d *NLBDriver) ServiceName() string { return ServiceName }
```

### Provision Flow

Identical structure to ALB:
1. Load existing state
2. If NLB exists → check for spec changes → converge
3. If NLB doesn't exist → `CreateLoadBalancer` → wait for `active` state → set attributes
4. Save state, schedule reconciliation, return outputs

### Convergence

When the spec changes on an existing NLB:

1. **IP address type** → `SetIpAddressType`
2. **Subnets** → `SetSubnets`
3. **Attributes** → `ModifyLoadBalancerAttributes` (cross-zone, deletion protection)
4. **Tags** → `RemoveTags` + `AddTags`

### Delete Flow

1. Disable deletion protection if enabled
2. Call `DeleteLoadBalancer`
3. Clear all state

---

## Step 6 — Provider Adapter

**File**: `internal/core/provider/nlb_adapter.go`

```go
type NLBAdapter struct {
    accounts *auth.Registry
}

func NewNLBAdapterWithRegistry(accounts *auth.Registry) *NLBAdapter {
    return &NLBAdapter{accounts: accounts}
}

func (a *NLBAdapter) Kind() string             { return "NLB" }
func (a *NLBAdapter) ServiceName() string      { return "NLB" }
func (a *NLBAdapter) Scope() KeyScope          { return KeyScopeRegion }
```

---

## Step 7 — Registry Integration

Add `NewNLBAdapterWithRegistry` to `internal/core/provider/registry.go`.

---

## Step 8 — Unit Tests

**File**: `internal/drivers/nlb/driver_test.go`

| Test | Description |
|---|---|
| `TestServiceName` | Verify `ServiceName()` returns `"NLB"` |
| `TestSpecFromObserved` | Verify building a spec from observed state (import) |

**File**: `internal/drivers/nlb/drift_test.go`

| Test | Description |
|---|---|
| `TestNoDrift` | Identical desired and observed → no drift |
| `TestSubnetDrift` | Different subnet sets → drift detected |
| `TestCrossZoneDrift` | Changed cross-zone setting → drift detected |
| `TestTagDrift` | Changed tags → drift detected |

---

## Step 9 — Integration Tests

**File**: `tests/integration/nlb_driver_test.go`

| Test | Description |
|---|---|
| `TestNLBProvision` | Create NLB, verify outputs, verify Ready status |
| `TestNLBProvisionWithEIP` | Create NLB with subnet mappings + EIPs |
| `TestNLBProvisionIdempotent` | Provision twice → no-op on second call |
| `TestNLBImport` | Import existing NLB |
| `TestNLBUpdate` | Change cross-zone → verify convergence |
| `TestNLBDelete` | Delete NLB, verify Deleted status |
| `TestNLBReconcile` | External modification → reconcile corrects drift |
| `TestNLBDuplicateName` | Provision with existing name → terminal 409 |

---

## NLB-Specific Design Decisions

### 1. Static IP Management

NLBs are the primary use case for static IP addresses on load balancers. The
`subnetMappings` field allows specifying an Elastic IP (`allocationId`) per subnet.
The driver tracks EIP associations in observed state but does NOT manage the EIP
lifecycle — that's the Elastic IP driver's responsibility. Changing an EIP association
after creation typically requires deleting and recreating the NLB zone mapping.

### 2. Cross-Zone Load Balancing

NLB cross-zone load balancing can be configured at both the LB level and the target
group level. The NLB driver manages only the LB-level setting. Target-group-level
cross-zone settings are managed by the Target Group driver.

### 3. No Security Groups

NLBs traditionally did not support security groups. AWS added SG support for NLBs
in late 2023, but it's opt-in at creation time and cannot be changed after. For
simplicity, the initial NLB driver does NOT support security groups — traffic flows
directly to targets. Security group support can be added as a future enhancement
with an immutable `enableSecurityGroups` flag.

### 4. Connection Draining

NLB connection draining (deregistration delay) is a target group attribute, not an
NLB attribute. The NLB driver does not manage it.

---

## Checklist

- [x] `schemas/aws/elb/nlb.cue` created
- [x] `internal/drivers/nlb/types.go` created
- [x] `internal/drivers/nlb/aws.go` created
- [x] `internal/drivers/nlb/drift.go` created
- [x] `internal/drivers/nlb/driver.go` created
- [x] `internal/drivers/nlb/driver_test.go` created
- [x] `internal/drivers/nlb/aws_test.go` created
- [x] `internal/drivers/nlb/drift_test.go` created
- [x] `internal/core/provider/nlb_adapter.go` created
- [x] `internal/core/provider/registry.go` updated
- [x] `tests/integration/nlb_driver_test.go` created
