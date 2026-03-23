# VPC Peering Connection Driver — Implementation Plan

> Target: A Restate Virtual Object driver that manages AWS VPC Peering Connections,
> following the exact patterns established by the VPC, Subnet, IGW, and EC2 Instance
> drivers.
>
> Key scope: `KeyScopeRegion` — key format is `region~metadata.name`, permanent and
> immutable for the lifetime of the Virtual Object. The AWS-assigned VPC Peering
> Connection ID lives only in state/outputs.

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
16. [VPC Peering-Specific Design Decisions](#vpc-peering-specific-design-decisions)
17. [Design Decisions (Resolved)](#design-decisions-resolved)
18. [Checklist](#checklist)

---

## 1. Overview & Scope

The VPC Peering Connection driver manages the lifecycle of AWS **VPC Peering
Connections**. A VPC Peering Connection is a networking connection between two
VPCs that enables routing traffic between them using private IPv4/IPv6 addresses.
Instances in either VPC can communicate as if they are in the same network.

### Why VPC Peering

VPC peering is fundamental for multi-VPC architectures. Common patterns include:

- **Application isolation**: separate VPCs for production, staging, shared services.
- **Microservice networking**: service mesh across VPCs without traversing the
  internet.
- **Shared services VPC**: centralized logging, monitoring, or authentication
  peered to application VPCs.

Without a peering driver, users cannot establish private connectivity between VPCs
managed by Praxis.

### Resource Scope for This Plan

| In Scope | Out of Scope |
|---|---|
| VPC Peering Connection creation | Route table entries for peering |
| Peering acceptance (same-account) | Cross-account peering acceptance |
| DNS resolution options | VPC management |
| Tags | Transit Gateway peering |
| Import and drift detection | Cross-region peering (v2) |
| Ownership tag enforcement | |
| Requester/accepter options | |

### VPC Peering Driver Contract

| Handler | Context | Purpose |
|---|---|---|
| `Provision` | `ObjectContext` (exclusive) | Create + accept peering connection |
| `Import` | `ObjectContext` (exclusive) | Adopt an existing peering connection |
| `Delete` | `ObjectContext` (exclusive) | Delete peering connection (blocked for Observed mode) |
| `Reconcile` | `ObjectContext` (exclusive) | Detect/correct drift (report-only for Observed mode) |
| `GetStatus` | `ObjectSharedContext` (shared) | Return current status |
| `GetOutputs` | `ObjectSharedContext` (shared) | Return peering outputs |

### Downstream Consumers

```text
${resources.my-peering.outputs.vpcPeeringConnectionId}  → Route table routes
${resources.my-peering.outputs.requesterVpcId}            → Configuration references
${resources.my-peering.outputs.accepterVpcId}             → Configuration references
${resources.my-peering.outputs.requesterCidrBlock}        → Route table CIDR entries
${resources.my-peering.outputs.accepterCidrBlock}         → Route table CIDR entries
```

---

## 2. Key Strategy

### Key Format: `region~metadata.name`

VPC Peering Connections are regional resources (same-region peering). This is
consistent with VPC, EC2, and IGW drivers.

1. **BuildKey**: returns `region~metadata.name`.
2. **BuildImportKey**: returns `region~vpcPeeringConnectionId`.
3. **Import**: `ModeObserved` by default — deleting a peering connection disrupts
   all private traffic between the two VPCs.

> **Cross-region peering**: In cross-region peering, the connection exists in BOTH
> regions. V1 of this driver targets same-region peering only. Cross-region peering
> is deferred to v2 where key strategy and multi-region coordination need careful
> design.

### Conflict Enforcement via Ownership Tags

Same pattern: `praxis:managed-key = <region~metadata.name>` written at creation.

---

## 3. File Inventory

```text
✦ internal/drivers/vpcpeering/types.go             — Spec, Outputs, ObservedState, State
✦ internal/drivers/vpcpeering/aws.go               — VPCPeeringAPI interface + realVPCPeeringAPI
✦ internal/drivers/vpcpeering/drift.go             — HasDrift(), ComputeFieldDiffs()
✦ internal/drivers/vpcpeering/driver.go            — VPCPeeringDriver Virtual Object
✦ internal/drivers/vpcpeering/driver_test.go       — Unit tests for driver
✦ internal/drivers/vpcpeering/aws_test.go          — Unit tests for error classification
✦ internal/drivers/vpcpeering/drift_test.go        — Unit tests for drift detection
✦ internal/core/provider/vpcpeering_adapter.go     — VPCPeeringAdapter
✦ internal/core/provider/vpcpeering_adapter_test.go — Unit tests for adapter
✦ schemas/aws/vpcpeering/vpcpeering.cue            — CUE schema
✦ tests/integration/vpcpeering_driver_test.go       — Integration tests
✎ cmd/praxis-network/main.go                       — Add VPCPeering driver .Bind()
✎ internal/core/provider/registry.go                — Add NewVPCPeeringAdapter
✎ justfile                                          — Add vpcpeering test targets
```

---

## Step 1 — CUE Schema

**File**: `schemas/aws/vpcpeering/vpcpeering.cue`

```cue
package vpcpeering

#VPCPeeringConnection: {
    apiVersion: "praxis.io/v1"
    kind:       "VPCPeeringConnection"

    metadata: {
        name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9._-]{0,254}$"
        labels: [string]: string
    }

    spec: {
        // region is the AWS region where the peering connection is created.
        region: string

        // requesterVpcId is the VPC initiating the peering request.
        // Immutable after creation.
        requesterVpcId: string

        // accepterVpcId is the VPC accepting the peering request.
        // Immutable after creation.
        accepterVpcId: string

        // peerOwnerId is the AWS account ID of the accepter VPC owner.
        // Required for cross-account peering. For same-account, this can
        // be omitted (defaults to the caller's account).
        peerOwnerId?: string

        // peerRegion is the region of the accepter VPC for cross-region peering.
        // For same-region peering, this can be omitted.
        peerRegion?: string

        // autoAccept controls whether the peering connection is automatically
        // accepted. Only works for same-account, same-region peering.
        // Default: true
        autoAccept: bool | *true

        // requesterOptions configures the requester side of the peering.
        requesterOptions?: {
            // allowDnsResolutionFromRemoteVpc enables DNS resolution of
            // public DNS hostnames to private IPs when queried from the
            // accepter VPC. Default: false.
            allowDnsResolutionFromRemoteVpc: bool | *false
        }

        // accepterOptions configures the accepter side of the peering.
        accepterOptions?: {
            // allowDnsResolutionFromRemoteVpc enables DNS resolution of
            // public DNS hostnames to private IPs when queried from the
            // requester VPC. Default: false.
            allowDnsResolutionFromRemoteVpc: bool | *false
        }

        // tags applied to the VPC Peering Connection.
        tags: [string]: string
    }

    outputs?: {
        vpcPeeringConnectionId: string
        requesterVpcId:         string
        accepterVpcId:          string
        requesterCidrBlock:     string
        accepterCidrBlock:      string
        status:                 string  // "pending-acceptance", "active", "deleted", etc.
        requesterOwnerId:       string
        accepterOwnerId:        string
    }
}
```

**Key decisions**:

- `requesterVpcId` and `accepterVpcId` are immutable — changing either means
  the peering is between entirely different VPCs, requiring replacement.
- `autoAccept: true` by default for same-account peering (the common case).
  Cross-account peering requires manual acceptance or a separate handler.
- DNS resolution options are mutable via `ModifyVpcPeeringConnectionOptions`.
- `peerOwnerId` and `peerRegion` are provided for forward compatibility but
  cross-account/cross-region peering is out-of-scope for v1.

---

## Step 2 — AWS Client Factory

**NO CHANGES NEEDED** — uses `NewEC2Client`.

---

## Step 3 — Driver Types

**File**: `internal/drivers/vpcpeering/types.go`

```go
package vpcpeering

import "github.com/shirvan/praxis/pkg/types"

const ServiceName = "VPCPeeringConnection"

type VPCPeeringSpec struct {
    Account          string            `json:"account,omitempty"`
    Region           string            `json:"region"`
    RequesterVpcId   string            `json:"requesterVpcId"`
    AccepterVpcId    string            `json:"accepterVpcId"`
    PeerOwnerId      string            `json:"peerOwnerId,omitempty"`
    PeerRegion       string            `json:"peerRegion,omitempty"`
    AutoAccept       bool              `json:"autoAccept"`
    RequesterOptions *PeeringOptions   `json:"requesterOptions,omitempty"`
    AccepterOptions  *PeeringOptions   `json:"accepterOptions,omitempty"`
    Tags             map[string]string `json:"tags,omitempty"`
    ManagedKey       string            `json:"managedKey,omitempty"`
}

type PeeringOptions struct {
    AllowDnsResolutionFromRemoteVpc bool `json:"allowDnsResolutionFromRemoteVpc"`
}

type VPCPeeringOutputs struct {
    VpcPeeringConnectionId string `json:"vpcPeeringConnectionId"`
    RequesterVpcId         string `json:"requesterVpcId"`
    AccepterVpcId          string `json:"accepterVpcId"`
    RequesterCidrBlock     string `json:"requesterCidrBlock"`
    AccepterCidrBlock      string `json:"accepterCidrBlock"`
    Status                 string `json:"status"`
    RequesterOwnerId       string `json:"requesterOwnerId"`
    AccepterOwnerId        string `json:"accepterOwnerId"`
}

type ObservedState struct {
    VpcPeeringConnectionId string            `json:"vpcPeeringConnectionId"`
    RequesterVpcId         string            `json:"requesterVpcId"`
    AccepterVpcId          string            `json:"accepterVpcId"`
    RequesterCidrBlock     string            `json:"requesterCidrBlock"`
    AccepterCidrBlock      string            `json:"accepterCidrBlock"`
    Status                 string            `json:"status"`
    RequesterOwnerId       string            `json:"requesterOwnerId"`
    AccepterOwnerId        string            `json:"accepterOwnerId"`
    RequesterOptions       *PeeringOptions   `json:"requesterOptions,omitempty"`
    AccepterOptions        *PeeringOptions   `json:"accepterOptions,omitempty"`
    Tags                   map[string]string `json:"tags"`
}

type VPCPeeringState struct {
    Desired            VPCPeeringSpec       `json:"desired"`
    Observed           ObservedState        `json:"observed"`
    Outputs            VPCPeeringOutputs    `json:"outputs"`
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

**File**: `internal/drivers/vpcpeering/aws.go`

### VPCPeeringAPI Interface

```go
type VPCPeeringAPI interface {
    // CreateVPCPeeringConnection creates a peering request between two VPCs.
    // Returns the peering connection ID.
    CreateVPCPeeringConnection(ctx context.Context, spec VPCPeeringSpec) (string, error)

    // AcceptVPCPeeringConnection accepts a pending peering connection.
    // Only valid for same-account peering or when called with accepter credentials.
    AcceptVPCPeeringConnection(ctx context.Context, peeringId string) error

    // DescribeVPCPeeringConnection returns the full observed state.
    DescribeVPCPeeringConnection(ctx context.Context, peeringId string) (ObservedState, error)

    // DeleteVPCPeeringConnection deletes (rejects or deletes) a peering connection.
    DeleteVPCPeeringConnection(ctx context.Context, peeringId string) error

    // ModifyPeeringOptions updates DNS resolution options for requester/accepter.
    ModifyPeeringOptions(ctx context.Context, peeringId string, requester *PeeringOptions, accepter *PeeringOptions) error

    // UpdateTags replaces user-managed tags.
    UpdateTags(ctx context.Context, peeringId string, tags map[string]string) error

    // FindByManagedKey searches for peering connections tagged with praxis:managed-key.
    FindByManagedKey(ctx context.Context, managedKey string) (string, error)
}
```

### Implementation Notes

- `CreateVPCPeeringConnection`: Uses `CreateVpcPeeringConnection` API. Sets
  `TagSpecifications`. Returns peering ID. Connection starts in
  "pending-acceptance" or "provisioning" state.
- `AcceptVPCPeeringConnection`: Uses `AcceptVpcPeeringConnection`. Only possible
  for same-account peering or with cross-account accepter credentials. Transitions
  state to "active".
- `DescribeVPCPeeringConnection`: Uses `DescribeVpcPeeringConnections`.
  Returns CIDR blocks from `RequesterVpcInfo` and `AccepterVpcInfo`. Must handle
  "deleted" and "rejected" states.
- `DeleteVPCPeeringConnection`: Uses `DeleteVpcPeeringConnection`. Works for
  both active and pending connections. Deletion is synchronous.
- `ModifyPeeringOptions`: Uses `ModifyVpcPeeringConnectionOptions`. Only works
  on active peering connections. Must be called AFTER acceptance.
- `FindByManagedKey`: Filters by tag. Must filter out "deleted", "rejected",
  "expired", and "failed" state connections.

### Error Classification

```go
func IsNotFound(err error) bool          // "InvalidVpcPeeringConnectionID.NotFound"
func IsVpcNotFound(err error) bool       // One of the VPCs doesn't exist
func IsAlreadyExists(err error) bool     // Duplicate peering between same VPCs
func IsCidrOverlap(err error) bool       // VPCs have overlapping CIDRs
func IsPeeringLimitExceeded(err error) bool // Account peering limit reached
```

---

## Step 5 — Drift Detection

**File**: `internal/drivers/vpcpeering/drift.go`

### Drift Rules

| Field | Mutable? | Drift Checked? | How Corrected |
|---|---|---|---|
| `tags` | Yes | **Yes** | CreateTags / DeleteTags |
| `requesterOptions.allowDnsResolution` | Yes | **Yes** | ModifyVpcPeeringConnectionOptions |
| `accepterOptions.allowDnsResolution` | Yes | **Yes** | ModifyVpcPeeringConnectionOptions |
| `requesterVpcId` | **No** | **No** | Requires replacement |
| `accepterVpcId` | **No** | **No** | Requires replacement |

```go
func HasDrift(desired VPCPeeringSpec, observed ObservedState) bool {
    // Skip if not active
    if observed.Status != "active" {
        return false
    }
    if !tagsMatch(desired.Tags, observed.Tags) {
        return true
    }
    if dnsOptionsDrift(desired.RequesterOptions, observed.RequesterOptions) {
        return true
    }
    if dnsOptionsDrift(desired.AccepterOptions, observed.AccepterOptions) {
        return true
    }
    return false
}

func ComputeFieldDiffs(desired VPCPeeringSpec, observed ObservedState) []FieldDiffEntry {
    // Mutable: tags, requesterOptions, accepterOptions
    // Immutable (reported only): requesterVpcId, accepterVpcId
}
```

---

## Step 6 — Driver Implementation

**File**: `internal/drivers/vpcpeering/driver.go`

### Provision

1. Input validation: `region`, `requesterVpcId`, `accepterVpcId` required.
   VPCs must be different (self-peering is not allowed).
2. Load current state. If peering exists, verify via Describe.
3. Pre-flight ownership check via `FindByManagedKey`.
4. Create peering connection: `CreateVPCPeeringConnection`.
5. Auto-accept if `autoAccept` is true: `AcceptVPCPeeringConnection`.
   This step is critical — without acceptance, the peering stays in
   "pending-acceptance" and no traffic flows.
6. Configure peering options after acceptance: `ModifyPeeringOptions`
   (DNS resolution settings).
7. Re-provision path: converge tags and peering options.
8. Final describe → build outputs → commit state.
9. Schedule reconcile.

> **Two-step creation**: VPC Peering is unique among VPC resources because creation
> is a two-step process: create (requester initiates) + accept (accepter approves).
> For same-account peering with `autoAccept: true`, both steps happen in the same
> Provision call. For cross-account peering (future), a separate acceptance
> mechanism is needed.

### Delete

1. Block `ModeObserved` (409).
2. Delete peering connection: `DeleteVPCPeeringConnection`.
3. Set tombstone state.

> **Synchronous deletion**: Unlike NAT Gateways, VPC Peering Connection deletion
> is synchronous. The API call returns when the connection is deleted. No waiter
> needed.

### State Machine

```text
    ┌──────────────────┐
    │  initiating-      │
    │  request          │
    └────────┬─────────┘
             │ (create)
    ┌────────▼─────────┐
    │  pending-         │    ──── (expire after 7 days)──→ expired
    │  acceptance       │    ──── (reject) ──→ rejected
    └────────┬─────────┘
             │ (accept)
    ┌────────▼─────────┐
    │  provisioning     │
    └────────┬─────────┘
             │
    ┌────────▼─────────┐
    │  active           │    ──── (delete) ──→ deleting → deleted
    └───────────────────┘
```

The driver is responsible for moving from "pending-acceptance" → "active" via
the Accept call. If the peering is stuck in "pending-acceptance" during reconcile,
the driver re-attempts acceptance (for same-account peering).

---

## Step 7 — Provider Adapter

**File**: `internal/core/provider/vpcpeering_adapter.go`

- **Key Scope**: `KeyScopeRegion`
- **BuildKey**: `JoinKey(spec.Region, metadata.name)`
- **BuildImportKey**: `JoinKey(region, vpcPeeringConnectionId)`

---

## Step 8 — Registry Integration

Add `NewVPCPeeringAdapterWithRegistry(accounts)` to `NewRegistry()`.

---

## Step 9 — Binary Entry Point & Dockerfile

Add `.Bind(restate.Reflect(vpcpeering.NewVPCPeeringDriver(cfg.Auth())))` to
`cmd/praxis-network/main.go`.

---

## Step 10 — Docker Compose & Justfile

No Docker Compose changes. Justfile:

```makefile
test-vpcpeering:
    go test ./internal/drivers/vpcpeering/... -v -count=1 -race
```

---

## Step 11 — Unit Tests

### `internal/drivers/vpcpeering/driver_test.go`

1. `TestProvision_CreatesPeering` — happy path: create + accept.
2. `TestProvision_SelfPeeringFails` — same VPC → terminal 400.
3. `TestProvision_MissingVpcIdFails` — terminal error 400.
4. `TestProvision_CidrOverlapFails` — overlapping CIDRs → terminal 409.
5. `TestProvision_IdempotentReprovision` — no duplicate peering.
6. `TestProvision_TagUpdate` — tags converged.
7. `TestProvision_DnsOptionsUpdate` — DNS options converged.
8. `TestProvision_ConflictFails` — FindByManagedKey → 409.
9. `TestProvision_PendingAcceptanceRetry` — stuck peering re-accepted.
10. `TestImport_ExistingPeering` — describes, synthesizes spec, returns outputs.
11. `TestImport_NotFoundFails` — terminal error 404.
12. `TestImport_DefaultsToObservedMode`.
13. `TestDelete_DeletesPeering`.
14. `TestDelete_AlreadyDeleted` — IsNotFound → success.
15. `TestDelete_ObservedModeBlocked` — terminal error 409.
16. `TestReconcile_NoDrift` — no changes.
17. `TestReconcile_DetectsTagDrift` — tags corrected.
18. `TestReconcile_DetectsDnsOptionDrift` — DNS options corrected.
19. `TestReconcile_ObservedModeReportsOnly`.
20. `TestReconcile_PendingAcceptanceReaccepted` — stuck state recovered.
21. `TestGetStatus_ReturnsCurrentState`.
22. `TestGetOutputs_ReturnsOutputs`.

### `internal/drivers/vpcpeering/drift_test.go`

1. `TestHasDrift_NoDrift` — identical → false.
2. `TestHasDrift_TagChanged` → true.
3. `TestHasDrift_DnsOptionsChanged` → true.
4. `TestHasDrift_NonActiveSkipped` — "pending-acceptance" → false.
5. `TestHasDrift_VpcChangedNoDrift` — immutable, not checked → false.
6. `TestComputeFieldDiffs_Tags` — tag diff reported.
7. `TestComputeFieldDiffs_DnsOptions` — DNS options diff reported.
8. `TestComputeFieldDiffs_ImmutableVpc` — reported with replacement suffix.
9. `TestTagsMatch_IgnoresPraxisTags`.

---

## Step 12 — Integration Tests

**File**: `tests/integration/vpcpeering_driver_test.go`

1. **TestVPCPeeringProvision_CreatesPeering** — Creates two VPCs, provisions
   peering between them, verifies via DescribeVpcPeeringConnections.
2. **TestVPCPeeringProvision_AutoAccept** — Verifies peering reaches "active"
   state automatically.
3. **TestVPCPeeringProvision_Idempotent** — Two provisions, same outputs.
4. **TestVPCPeeringProvision_CidrOverlapFails** — Two VPCs with same CIDR.
5. **TestVPCPeeringImport_Existing** — Creates peering via SDK, imports.
6. **TestVPCPeeringDelete_Deletes** — Provisions, deletes, verifies gone.
7. **TestVPCPeeringReconcile_TagDrift** — Changes tags, reconcile corrects.
8. **TestVPCPeeringGetStatus_ReturnsActive**.

### LocalStack VPC Peering Compatibility Note

LocalStack (Pro) supports VPC peering connections including create, accept, describe,
delete, and modify options. LocalStack Community may have limited support. Integration
tests should verify LocalStack capabilities and skip unsupported operations.

---

## VPC Peering-Specific Design Decisions

### 1. Two-Phase Creation (Create + Accept)

VPC Peering is the only VPC resource with a two-phase creation model:

1. **Create**: Requester initiates. State = "pending-acceptance".
2. **Accept**: Accepter approves. State = "active".

For same-account peering (autoAccept: true), both phases happen in a single
Provision call:

```text
restate.Run: CreateVpcPeeringConnection → pcx-xxx
restate.Run: AcceptVpcPeeringConnection(pcx-xxx) → active
restate.Run: ModifyVpcPeeringConnectionOptions (DNS settings)
restate.Run: DescribeVpcPeeringConnections → build outputs
```

Each phase is a separate `restate.Run()` call so that if Restate replays, it
doesn't re-create an already-created peering or re-accept an already-active one.

### 2. Cross-Account Peering (Deferred to v2)

Cross-account peering requires:

- The accepter to call `AcceptVpcPeeringConnection` with their own credentials.
- A mechanism to coordinate between requester and accepter (e.g., a shared
  durable promise or external workflow).

This is architecturally complex and deferred. V1 supports same-account peering
only. The schema includes `peerOwnerId` for forward compatibility.

### 3. Cross-Region Peering (Deferred to v2)

Cross-region peering requires:

- The `peerRegion` field to specify the accepter VPC's region.
- EC2 clients for both regions (create in requester region, accept in accepter).
- Key strategy consideration — the peering exists in both regions.

Deferred to v2. V1 targets same-region peering only.

### 4. CIDR Overlap Prevention

AWS rejects peering between VPCs with overlapping CIDRs. The driver surfaces this
as a terminal error (409) with a clear message. It does NOT pre-validate CIDRs —
it relies on the AWS API to enforce the constraint and maps the error appropriately.

### 5. Peering Connection Expiration

A peering connection in "pending-acceptance" state expires after 7 days. The
reconcile handler detects "expired" state and reports it. The driver does not
automatically recreate expired peering connections — the user must delete and
re-provision to reset the state.

### 6. VPC Peering as a DAG Dependency

In compound templates:

```text
VPC-A ──┐
         ├── VPC Peering Connection ──→ Route Table (A → B CIDR → pcx-xxx)
VPC-B ──┘                             Route Table (B → A CIDR → pcx-xxx)
```

Both VPCs must exist before the peering connection. Route tables in both VPCs
need routes pointing to the peering connection ID. During delete: routes first,
then peering, then VPCs.

### 7. Duplicate Peering Detection

AWS enforces that only one active peering connection can exist between any two
VPCs (in a given direction). Attempting to create a duplicate returns an error.
The driver:

1. First checks `FindByManagedKey` for ownership conflicts.
2. If creation fails due to duplicate, maps it to a terminal 409 with a message
   indicating the existing peering connection ID.

---

## Design Decisions (Resolved)

1. **Should the driver support bidirectional peering setup?**
   No. A single VPC Peering Connection is inherently bidirectional — traffic
   flows in both directions once active. A single driver resource is sufficient.
   Route tables in both VPCs need separate route entries, managed by the Route
   Table driver.

2. **Should the driver handle both requester and accepter roles?**
   Yes, for same-account peering. The driver creates (as requester) and accepts
   (as accepter) in a single Provision call. For cross-account peering, the
   accepter role requires separate credentials — deferred to v2.

3. **Should the driver auto-retry acceptance on reconcile?**
   Yes. If the peering is stuck in "pending-acceptance" during reconcile (e.g.,
   the accept step failed on a previous attempt), the driver re-attempts
   acceptance. This is safe because AcceptVpcPeeringConnection is idempotent
   for already-accepted connections.

4. **Should the driver expose VPC CIDR blocks in outputs?**
   Yes. Route tables need the peer VPC's CIDR block to create routes. Exposing
   `requesterCidrBlock` and `accepterCidrBlock` in outputs avoids requiring users
   to manually look up or cross-reference CIDR blocks.

5. **Should the driver validate that VPCs exist before creating?**
   No. This would require cross-driver calls. The driver relies on the AWS API
   to validate VPC existence and surfaces any errors as terminal errors. The DAG
   ensures VPCs are provisioned before peering connections.

---

## Example Template

```cue
import (
    "praxis.io/schemas/aws/vpc"
    "praxis.io/schemas/aws/vpcpeering"
    "praxis.io/schemas/aws/routetable"
)

resources: {
    "app-vpc": vpc.#VPC & {
        apiVersion: "praxis.io/v1"
        kind:       "VPC"
        metadata: name: "app-vpc"
        spec: {
            region:    "\(variables.region)"
            cidrBlock: "10.0.0.0/16"
            tags: { Name: "app-vpc" }
        }
    }

    "shared-vpc": vpc.#VPC & {
        apiVersion: "praxis.io/v1"
        kind:       "VPC"
        metadata: name: "shared-services-vpc"
        spec: {
            region:    "\(variables.region)"
            cidrBlock: "10.1.0.0/16"
            tags: { Name: "shared-services-vpc" }
        }
    }

    "app-to-shared-peering": vpcpeering.#VPCPeeringConnection & {
        apiVersion: "praxis.io/v1"
        kind:       "VPCPeeringConnection"
        metadata: name: "app-to-shared"
        spec: {
            region:          "\(variables.region)"
            requesterVpcId:  "${resources.app-vpc.outputs.vpcId}"
            accepterVpcId:   "${resources.shared-vpc.outputs.vpcId}"
            autoAccept:      true
            requesterOptions: {
                allowDnsResolutionFromRemoteVpc: true
            }
            accepterOptions: {
                allowDnsResolutionFromRemoteVpc: true
            }
            tags: {
                Name:        "app-to-shared"
                Environment: "production"
            }
        }
    }

    "app-rt-peering-route": routetable.#RouteTable & {
        apiVersion: "praxis.io/v1"
        kind:       "RouteTable"
        metadata: name: "app-rt"
        spec: {
            region: "\(variables.region)"
            vpcId:  "${resources.app-vpc.outputs.vpcId}"
            routes: [{
                destinationCidrBlock:    "10.1.0.0/16"
                vpcPeeringConnectionId:  "${resources.app-to-shared-peering.outputs.vpcPeeringConnectionId}"
            }]
            tags: { Name: "app-rt" }
        }
    }
}

variables: {
    region: string | *"us-east-1"
}
```

---

## Checklist

- [ ] **Schema**: `schemas/aws/vpcpeering/vpcpeering.cue` created
- [ ] **Types**: `internal/drivers/vpcpeering/types.go` created
- [ ] **AWS API**: `internal/drivers/vpcpeering/aws.go` created
- [ ] **Drift**: `internal/drivers/vpcpeering/drift.go` created
- [ ] **Driver**: `internal/drivers/vpcpeering/driver.go` created with all 6 handlers
- [ ] **Adapter**: `internal/core/provider/vpcpeering_adapter.go` created
- [ ] **Registry**: `internal/core/provider/registry.go` updated
- [ ] **Entry point**: `.Bind()` added to `cmd/praxis-network/main.go`
- [ ] **Justfile**: Updated with vpcpeering test targets
- [ ] **Unit tests**: driver, drift, aws helpers created
- [ ] **Unit tests (adapter)**: `internal/core/provider/vpcpeering_adapter_test.go` created
- [ ] **Integration tests**: `tests/integration/vpcpeering_driver_test.go` created
- [ ] **Conflict check**: `FindByManagedKey` with state filtering
- [ ] **Ownership tag**: `praxis:managed-key` written at creation
- [ ] **Import default mode**: ModeObserved
- [ ] **Delete mode guard**: Blocks deletion for ModeObserved (409)
- [ ] **Two-phase creation**: Create + Accept in separate restate.Run calls
- [ ] **Pending acceptance recovery**: Reconcile re-accepts stuck peerings
- [ ] **CIDR overlap**: Surfaced as terminal 409
- [ ] **DNS options**: ModifyVpcPeeringConnectionOptions support
- [ ] **Build passes**: `go build ./...`
- [ ] **Unit tests pass**: `go test ./internal/drivers/vpcpeering/... -race`
- [ ] **Integration tests pass**: `go test ./tests/integration/ -run TestVPCPeering -tags=integration`
