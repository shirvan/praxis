# Internet Gateway Driver — Implementation Specification

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
16. [IGW-Specific Design Decisions](#igw-specific-design-decisions)
17. [Design Decisions (Resolved)](#design-decisions-resolved)
18. [Checklist](#checklist)

---

## 1. Overview & Scope

The Internet Gateway (IGW) driver manages the lifecycle of AWS **Internet Gateways**
and their **VPC attachment**. An IGW is only useful when attached to a VPC — the
attachment is tightly coupled to the IGW lifecycle and is managed as part of this
driver rather than as a separate resource.

### Why Internet Gateways

Internet Gateways are the bridge between a VPC and the public internet. Without an
IGW, instances in public subnets cannot reach the internet and external traffic
cannot reach instances. Every VPC that needs internet connectivity requires an IGW.
The IGW driver, combined with VPC, Subnet, and Route Table drivers, completes the
minimum viable public networking stack.

### Resource Scope

| In Scope | Out of Scope |
|---|---|
| IGW creation, deletion | Egress-Only Internet Gateways (IPv6) |
| VPC attachment/detachment | Route table configuration (Route Table driver) |
| Tags | NAT gateways |
| Import and drift detection | VPN gateways |
| Ownership tag enforcement | |

### IGW Driver Contract

| Handler | Context | Purpose |
|---|---|---|
| `Provision` | `ObjectContext` (exclusive) | Create IGW and attach to VPC |
| `Import` | `ObjectContext` (exclusive) | Adopt an existing IGW |
| `Delete` | `ObjectContext` (exclusive) | Detach and delete IGW (blocked for Observed mode) |
| `Reconcile` | `ObjectContext` (exclusive) | Detect/correct drift (report-only for Observed mode) |
| `GetStatus` | `ObjectSharedContext` (shared) | Return current status |
| `GetOutputs` | `ObjectSharedContext` (shared) | Return IGW outputs |

### Downstream Consumers

```text
${resources.my-igw.outputs.internetGatewayId}  → Route table routes (0.0.0.0/0 → igw-xxx)
```

---

## 2. Key Strategy

### Key Format: `region~metadata.name`

An IGW is a regional resource. While it attaches to a VPC, the VPC can be determined
from the spec — using `region~metadata.name` keeps the key format consistent with
VPC and EC2 drivers.

**Why not `vpcId~metadata.name`**: An IGW has a 1:1 relationship with a VPC, but
the VPC ID is often a template expression resolved at provision time. Using region
as the scope is simpler and consistent with the VPC driver's own key scheme.

1. **BuildKey**: returns `region~metadata.name`.
2. **BuildImportKey**: returns `region~igwId`.
3. **Import**: `ModeObserved` by default — detaching an IGW disrupts all internet
   connectivity for the VPC.

### Constraint: One IGW per VPC

AWS enforces a limit of one IGW per VPC. The driver validates this at provision
time: if the target VPC already has an IGW attached (and it's not the one this
driver manages), the provision fails with a terminal error.

### Conflict Enforcement via Ownership Tags

Same pattern: `praxis:managed-key = <region~metadata.name>` written at creation,
checked by `FindByManagedKey` pre-flight.

---

## 3. File Inventory

```text
✓ internal/drivers/igw/types.go             — Spec, Outputs, ObservedState, State
✓ internal/drivers/igw/aws.go               — IGWAPI interface + realIGWAPI
✓ internal/drivers/igw/drift.go             — HasDrift(), ComputeFieldDiffs()
✓ internal/drivers/igw/driver.go            — IGWDriver Virtual Object
✓ internal/drivers/igw/driver_test.go       — Unit tests for driver
✓ internal/drivers/igw/aws_test.go          — Unit tests for error classification
✓ internal/drivers/igw/drift_test.go        — Unit tests for drift detection
✓ internal/core/provider/igw_adapter.go     — IGWAdapter
✓ internal/core/provider/igw_adapter_test.go — Unit tests for adapter
✓ schemas/aws/igw/igw.cue                   — CUE schema
✓ tests/integration/igw_driver_test.go      — Integration tests
✓ cmd/praxis-network/main.go               — Add IGW driver .Bind()
✓ internal/core/provider/registry.go        — Add NewIGWAdapter
✓ justfile                                  — Add igw test targets
```

---

## Step 1 — CUE Schema

**File**: `schemas/aws/igw/igw.cue`

```cue
package igw

#InternetGateway: {
    apiVersion: "praxis.io/v1"
    kind:       "InternetGateway"

    metadata: {
        name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9._-]{0,254}$"
        labels: [string]: string
    }

    spec: {
        // region is the AWS region.
        region: string

        // vpcId is the VPC to attach the IGW to.
        // Typically: "${resources.my-vpc.outputs.vpcId}"
        vpcId: string

        // tags applied to the IGW resource.
        tags: [string]: string
    }

    outputs?: {
        internetGatewayId: string
        vpcId:             string
        ownerId:           string
        state:             string  // "available", "detached"
    }
}
```

**Key decisions**:

- IGW spec is intentionally minimal — IGWs have almost no configurable properties
  beyond the VPC attachment and tags.
- `vpcId` is the only required infrastructure field. The IGW itself is a stateless
  router — all packet forwarding behavior is defined by route table entries, not
  the IGW configuration.
- No `egressOnly` field — Egress-Only IGWs are a separate AWS resource type
  (`EgressOnlyInternetGateway`) used for IPv6 and are out of scope.

---

## Step 2 — AWS Client Factory

**NO CHANGES NEEDED** — uses `NewEC2Client`.

---

## Step 3 — Driver Types

**File**: `internal/drivers/igw/types.go`

```go
package igw

import "github.com/shirvan/praxis/pkg/types"

const ServiceName = "InternetGateway"

type IGWSpec struct {
    Account    string            `json:"account,omitempty"`
    Region     string            `json:"region"`
    VpcId      string            `json:"vpcId"`
    Tags       map[string]string `json:"tags,omitempty"`
    ManagedKey string            `json:"managedKey,omitempty"`
}

type IGWOutputs struct {
    InternetGatewayId string `json:"internetGatewayId"`
    VpcId             string `json:"vpcId"`
    OwnerId           string `json:"ownerId"`
    State             string `json:"state"` // "available" when attached
}

type ObservedState struct {
    InternetGatewayId string            `json:"internetGatewayId"`
    AttachedVpcId     string            `json:"attachedVpcId"` // empty if detached
    OwnerId           string            `json:"ownerId"`
    Tags              map[string]string `json:"tags"`
}

type IGWState struct {
    Desired            IGWSpec              `json:"desired"`
    Observed           ObservedState        `json:"observed"`
    Outputs            IGWOutputs           `json:"outputs"`
    Status             types.ResourceStatus `json:"status"`
    Mode               types.Mode           `json:"mode"`
    Error              string               `json:"error,omitempty"`
    Generation         int64                `json:"generation"`
    LastReconcile      string               `json:"lastReconcile,omitempty"`
    ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
```

---

## Step 4 — AWS API Abstraction Layer

**File**: `internal/drivers/igw/aws.go`

### IGWAPI Interface

```go
type IGWAPI interface {
    // CreateInternetGateway creates a new IGW with tags.
    // Returns the IGW ID. Does NOT attach to a VPC.
    CreateInternetGateway(ctx context.Context, spec IGWSpec) (string, error)

    // DescribeInternetGateway returns the full observed state.
    DescribeInternetGateway(ctx context.Context, igwId string) (ObservedState, error)

    // DeleteInternetGateway deletes an IGW. Must be detached first.
    DeleteInternetGateway(ctx context.Context, igwId string) error

    // AttachToVpc attaches an IGW to a VPC.
    // AWS limit: one IGW per VPC.
    AttachToVpc(ctx context.Context, igwId string, vpcId string) error

    // DetachFromVpc detaches an IGW from a VPC.
    DetachFromVpc(ctx context.Context, igwId string, vpcId string) error

    // UpdateTags replaces user-managed tags.
    UpdateTags(ctx context.Context, igwId string, tags map[string]string) error

    // FindByManagedKey searches for IGWs tagged with praxis:managed-key=managedKey.
    FindByManagedKey(ctx context.Context, managedKey string) (string, error)
}
```

### Implementation Notes

- `CreateInternetGateway`: Uses `CreateInternetGateway` API with `TagSpecifications`.
  IGWs are created in a "detached" state — attachment is a separate call.
- `DescribeInternetGateway`: Uses `DescribeInternetGateways`. The attachment status
  is in the `Attachments` field: an attached IGW has one entry with `State: "available"`.
  A detached IGW has an empty `Attachments` list.
- `DeleteInternetGateway`: Must be detached first. If still attached, AWS returns
  `DependencyViolation`.
- `AttachToVpc`: Uses `AttachInternetGateway`. Fails with
  `Resource.AlreadyAssociated` if the VPC already has an IGW.
- `DetachFromVpc`: Uses `DetachInternetGateway`.

### Error Classification

```go
func IsNotFound(err error) bool         // "InvalidInternetGatewayID.NotFound"
func IsDependencyViolation(err error) bool  // "DependencyViolation" (still attached)
func IsAlreadyAttached(err error) bool  // "Resource.AlreadyAssociated" (VPC already has IGW)
func IsNotAttached(err error) bool      // "Gateway.NotAttached" (detach when not attached)
func IsInvalidParam(err error) bool     // "InvalidParameterValue"
```

---

## Step 5 — Drift Detection

**File**: `internal/drivers/igw/drift.go`

### Drift Rules

| Field | Mutable? | Drift Checked? | How Corrected |
|---|---|---|---|
| `vpcId` (attachment) | Yes* | **Yes** | DetachFromVpc + AttachToVpc |
| `tags` | Yes | **Yes** | CreateTags / DeleteTags |

*The VPC attachment is mutable in the sense that you can detach from one VPC and
attach to another. However, changing the VPC in a re-provision is unusual and
potentially destructive (all routes through this IGW stop working during the switch).
The driver supports it for completeness but logs a warning.

```go
func HasDrift(desired IGWSpec, observed ObservedState) bool {
    if desired.VpcId != observed.AttachedVpcId {
        return true
    }
    if !tagsMatch(desired.Tags, observed.Tags) {
        return true
    }
    return false
}

func ComputeFieldDiffs(desired IGWSpec, observed ObservedState) []FieldDiffEntry {
    // VPC attachment change, tag diffs
}
```

**Detached IGW drift**: If the IGW exists but is detached (`AttachedVpcId` is empty),
drift is detected and correction reattaches it to the desired VPC.

---

## Step 6 — Driver Implementation

**File**: `internal/drivers/igw/driver.go`

### Provision

1. Input validation: `region`, `vpcId` required.
2. Load current state. If IGW exists, verify it still exists.
3. Pre-flight ownership check via `FindByManagedKey`.
4. Create IGW if new: `CreateInternetGateway`.
5. Attach to VPC: `AttachToVpc`. Handle `Resource.AlreadyAssociated` — if the VPC
   already has a **different** IGW, this is a terminal error. If the VPC already has
   **this** IGW, it's a no-op.
6. Re-provision path: if VPC changed, detach from old VPC and attach to new.
   Update tags if changed.
7. Final describe → build outputs → commit state.
8. Schedule reconcile.

### Delete

1. Block `ModeObserved` (409).
2. Detach from VPC: `DetachFromVpc`. Handle `Gateway.NotAttached` as no-op.
3. Delete IGW: `DeleteInternetGateway`.
4. Set tombstone state.

### Reconcile

1. Describe current state.
2. Check VPC attachment and tags for drift.
3. Managed mode: correct drift (reattach, fix tags). Observed mode: report only.

---

## Step 7 — Provider Adapter

**File**: `internal/core/provider/igw_adapter.go`

- **Key Scope**: `KeyScopeRegion`
- **BuildKey**: `JoinKey(spec.Region, metadata.name)`
- **BuildImportKey**: `JoinKey(region, igwId)`

---

## Step 8 — Registry Integration

`NewIGWAdapterWithAuth` is registered in `NewRegistry()`.

---

## Step 9 — Binary Entry Point & Dockerfile

The IGW driver is bound in `cmd/praxis-network/main.go`:

`.Bind(restate.Reflect(igw.NewIGWDriver(auth)))`

---

## Step 10 — Docker Compose & Justfile

No Docker Compose changes. Justfile:

```text
test-igw:
    go test ./internal/drivers/igw/... -v -count=1 -race
```

---

## Step 11 — Unit Tests

### `internal/drivers/igw/driver_test.go`

1. `TestServiceName` — verifies ServiceName constant.
2. `TestProvision_CreatesAndAttaches` — happy path (requires Docker/testcontainers).
3. `TestProvision_MissingVpcIDFails` — terminal error 400.
4. `TestProvision_ConflictFails` — FindByManagedKey conflict → 409.
5. `TestProvision_IdempotentReprovision` — no duplicate IGW.
6. `TestProvision_VpcAttachmentChange` — detach old, attach new.
7. `TestProvision_TagUpdate` — tags updated.
8. `TestProvision_VpcAlreadyHasIGWFails` — AlreadyAssociated → terminal error 409.
9. `TestImport_ExistingIGW` — describes, synthesizes spec, returns outputs.
10. `TestDelete_DetachesAndDeletes` — detach then delete.
11. `TestDelete_ObservedModeBlocked` — terminal error 409.
12. `TestReconcile_DetachedIGWReattaches` — reattaches to VPC.
13. `TestReconcile_ObservedModeReportsOnly`.
14. `TestGetOutputs_ReturnsCurrentState`.

### `internal/drivers/igw/drift_test.go`

1. `TestHasDrift_NoDrift` — identical → false.
2. `TestHasDrift_DetachedOrWrongVpc` — empty or wrong AttachedVpcId → true.
3. `TestHasDrift_TagChange` → true.
4. `TestComputeFieldDiffs` — reports attachment and tag diffs.

### `internal/drivers/igw/aws_test.go`

1. `TestIsNotFound_True` — error code detection.
2. `TestIsDependencyViolation_True` — error code detection.
3. `TestIsAlreadyAttached_True` — error code detection.
4. `TestIsNotAttached_True` — error code detection.
5. `TestIsInvalidParam_True` — error code detection.
6. `TestSingleManagedKeyMatch` — 0, 1, and multi-match cases.

### `internal/core/provider/igw_adapter_test.go`

1. `TestIGWAdapter_BuildKeyAndDecodeSpec` — key format and spec parsing.
2. `TestIGWAdapter_BuildImportKey` — import key format.
3. `TestIGWAdapter_NormalizeOutputs` — output normalization.

---

## Step 12 — Integration Tests

**File**: `tests/integration/igw_driver_test.go`

1. **TestIGWProvision_CreatesAndAttaches** — Creates VPC, provisions IGW, verifies
   attachment via DescribeInternetGateways.
2. **TestIGWProvision_Idempotent** — Two provisions, same outputs.
3. **TestIGWImport_ExistingIGW** — Creates IGW+VPC via SDK, imports.
4. **TestIGWDelete_DetachesAndDeletes** — Provisions, deletes, verifies gone.
5. **TestIGWReconcile_ReattachesDetachedIGW** — Detaches IGW via SDK, reconcile
   reattaches.
6. **TestIGWReconcile_TagDrift** — Changes tags, reconcile corrects.
7. **TestIGWGetStatus_ReturnsReady** — Provisions, checks status.

### Moto IGW Compatibility Note

Moto supports CreateInternetGateway, DescribeInternetGateways,
DeleteInternetGateway, AttachInternetGateway, DetachInternetGateway, and tag
operations. IGW emulation is comprehensive.

---

## IGW-Specific Design Decisions

### 1. VPC Attachment as Part of IGW Lifecycle

AWS models IGW creation and VPC attachment as two separate API calls. The driver
combines them into a single Provision operation because an unattached IGW is useless.
The two-step process is:

1. `CreateInternetGateway` → creates detached IGW with ID.
2. `AttachInternetGateway` → attaches to specified VPC.

Delete reverses this:

1. `DetachInternetGateway` → detaches from VPC.
2. `DeleteInternetGateway` → removes the IGW.

### 2. One IGW Per VPC Enforcement

AWS enforces that a VPC can have at most one IGW attached. The driver validates
this at provision time. If `AttachToVpc` fails with `Resource.AlreadyAssociated`,
the driver returns a terminal error (409) indicating the VPC already has an IGW.

### 3. Key Scope: Region, Not VPC

Despite the IGW having a 1:1 relationship with a VPC, the key uses
`region~metadata.name` (KeyScopeRegion) rather than `vpcId~metadata.name`
(KeyScopeCustom). Rationale:

- The VPC ID is typically a template expression that resolves at provision time.
  Using it in the key would require the VPC to be provisioned before BuildKey runs.
- An IGW conceptually belongs to a region (it's a regional resource in the AWS
  console), and its VPC association is a configuration property, not an identity
  property.
- Consistency with VPC and EC2 key schemes.

### 4. Minimal Configuration Surface

IGWs have no configurable properties beyond the VPC attachment and tags. There are
no attributes to modify, no capacity settings, no bandwidth limits. This makes
the driver the simplest in the networking stack.

### 5. IGW as a DAG Dependency

In compound templates:

```text
VPC → Internet Gateway → Route Table (route 0.0.0.0/0 → igw-xxx)
```

The IGW depends on the VPC (needs `vpcId`). Route tables depend on the IGW (need
`internetGatewayId` for the default route). During delete, routes are removed
first, then the IGW is detached and deleted.

### 6. Detached IGW Recovery

If an IGW becomes detached externally (someone runs `DetachInternetGateway` via
the console), the driver treats this as drift and reattaches it during reconciliation.
This is important because a detached IGW breaks all internet connectivity for the VPC.

---

## Design Decisions (Resolved)

1. **Should the driver support Egress-Only Internet Gateways?**
   No. Egress-Only IGWs are a separate AWS resource type (`EgressOnlyInternetGateway`)
   used exclusively for IPv6 outbound traffic. They would be a separate driver if
   IPv6 support is added.

2. **Should the driver auto-create a default route in the route table?**
   No. Route creation is the Route Table driver's responsibility. The IGW driver
   just creates and attaches the gateway. The template author must create a route
   `0.0.0.0/0 → igw-xxx` in the Route Table spec.

3. **Should detaching from one VPC and attaching to another be supported?**
   Yes, but with a warning. Changing `spec.vpcId` on re-provision detaches from the
   old VPC and attaches to the new one. This is a disruptive operation (breaks
   internet connectivity for the old VPC) and is logged as a warning.

4. **Should the driver track "state"?**
   IGW attachment state is tracked via `ObservedState.AttachedVpcId`. AWS IGW
   attachments have a state field (`available`, `attaching`, `detaching`, `detached`)
   but transitions are nearly instantaneous. The driver does not wait for attachment
   state transitions.

---

## Example Template

```cue
import (
    "praxis.io/schemas/aws/vpc"
    "praxis.io/schemas/aws/igw"
)

resources: {
    "my-vpc": vpc.#VPC & {
        apiVersion: "praxis.io/v1"
        kind:       "VPC"
        metadata: name: "web-vpc"
        spec: {
            region:             "\(variables.region)"
            cidrBlock:          "10.0.0.0/16"
            enableDnsHostnames: true
            enableDnsSupport:   true
            tags: { Name: "web-vpc" }
        }
    }

    "my-igw": igw.#InternetGateway & {
        apiVersion: "praxis.io/v1"
        kind:       "InternetGateway"
        metadata: name: "web-igw"
        spec: {
            region: "\(variables.region)"
            vpcId:  "${resources.my-vpc.outputs.vpcId}"
            tags: {
                Name: "web-igw"
                Environment: "production"
            }
        }
    }
}

variables: {
    region: string | *"us-east-1"
}
```

---

## Checklist

- [x] **Schema**: `schemas/aws/igw/igw.cue` created
- [x] **Types**: `internal/drivers/igw/types.go` created
- [x] **AWS API**: `internal/drivers/igw/aws.go` created with IGWAPI interface + realIGWAPI
- [x] **Drift**: `internal/drivers/igw/drift.go` created
- [x] **Driver**: `internal/drivers/igw/driver.go` created with all 6 handlers
- [x] **Adapter**: `internal/core/provider/igw_adapter.go` created
- [x] **Registry**: `internal/core/provider/registry.go` updated
- [x] **Entry point**: `.Bind()` added to `cmd/praxis-network/main.go`
- [x] **Justfile**: Updated with igw test targets
- [x] **Unit tests**: driver, drift, aws helpers created
- [x] **Unit tests (adapter)**: `internal/core/provider/igw_adapter_test.go` created
- [x] **Integration tests**: `tests/integration/igw_driver_test.go` created
- [x] **Conflict check**: `FindByManagedKey` in IGWAPI
- [x] **Ownership tag**: `praxis:managed-key` written at creation
- [x] **Import default mode**: ModeObserved
- [x] **Delete mode guard**: Blocks deletion for ModeObserved (409)
- [x] **VPC attachment managed**: Create+Attach, Detach+Delete lifecycle
- [x] **Detached IGW recovery**: Reconcile reattaches detached IGWs
- [x] **Build passes**: `go build ./...`
- [x] **Unit tests pass**: `go test ./internal/drivers/igw/... -race`
- [ ] **Integration tests pass**: `go test ./tests/integration/ -run TestIGW -tags=integration`
