# NAT Gateway Driver — Implementation Plan

> Target: A Restate Virtual Object driver that manages AWS NAT Gateways, following
> the exact patterns established by the VPC, Subnet, IGW, and EC2 Instance drivers.
>
> Key scope: `KeyScopeRegion` — key format is `region~metadata.name`, permanent and
> immutable for the lifetime of the Virtual Object. The AWS-assigned NAT Gateway ID
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
16. [NAT Gateway-Specific Design Decisions](#nat-gateway-specific-design-decisions)
17. [Design Decisions (Resolved)](#design-decisions-resolved)
18. [Checklist](#checklist)

---

## 1. Overview & Scope

The NAT Gateway driver manages the lifecycle of AWS **NAT Gateways**. NAT Gateways
enable instances in private subnets to connect to the internet or other AWS services
while preventing the internet from initiating inbound connections.

### Why NAT Gateways

NAT Gateways are essential for private subnet internet access. The canonical VPC
architecture uses public subnets with an IGW for load balancers and NAT Gateways,
and private subnets with a route to the NAT Gateway for application servers and
databases. Without a NAT Gateway driver, private subnets are isolated from the
internet — instances can't download updates, connect to external APIs, or reach
AWS service endpoints that require internet access.

### Resource Scope for This Plan

| In Scope | Out of Scope |
|---|---|
| NAT Gateway creation, deletion | Route table entries pointing to NAT GW |
| Elastic IP allocation association | Subnet management |
| Subnet placement | VPC management |
| Connectivity type (public/private) | Elastic IP lifecycle (EIP driver) |
| Tags | |
| Import and drift detection | |
| Ownership tag enforcement | |

### NAT Gateway Driver Contract

| Handler | Context | Purpose |
|---|---|---|
| `Provision` | `ObjectContext` (exclusive) | Create NAT Gateway in subnet |
| `Import` | `ObjectContext` (exclusive) | Adopt an existing NAT Gateway |
| `Delete` | `ObjectContext` (exclusive) | Delete NAT Gateway (blocked for Observed mode) |
| `Reconcile` | `ObjectContext` (exclusive) | Detect/correct drift (report-only for Observed mode) |
| `GetStatus` | `ObjectSharedContext` (shared) | Return current status |
| `GetOutputs` | `ObjectSharedContext` (shared) | Return NAT Gateway outputs |

### Downstream Consumers

```text
${resources.my-natgw.outputs.natGatewayId}  → Route table routes (0.0.0.0/0 → nat-xxx)
${resources.my-natgw.outputs.publicIp}      → Allowlists, firewall rules
${resources.my-natgw.outputs.privateIp}     → Network diagnostics
```

---

## 2. Key Strategy

### Key Format: `region~metadata.name`

NAT Gateways are regional resources placed into a specific subnet and AZ. Using
`region~metadata.name` is consistent with the VPC, EC2, and IGW drivers.

1. **BuildKey**: returns `region~metadata.name`.
2. **BuildImportKey**: returns `region~natGatewayId`.
3. **Import**: `ModeObserved` by default — deleting a NAT Gateway disrupts all
   internet connectivity for private subnets routing through it.

**Why not `subnetId~metadata.name`**: NAT Gateways are often referenced cross-AZ
(a single NAT GW can serve multiple private subnets in different AZs). The subnet
is a placement detail, not an identity dimension. Region-scoped keys are simpler
and consistent with the driver fleet.

### Conflict Enforcement via Ownership Tags

Same pattern: `praxis:managed-key = <region~metadata.name>` written at creation.

---

## 3. File Inventory

```text
✦ internal/drivers/natgw/types.go             — Spec, Outputs, ObservedState, State
✦ internal/drivers/natgw/aws.go               — NATGatewayAPI interface + realNATGatewayAPI
✦ internal/drivers/natgw/drift.go             — HasDrift(), ComputeFieldDiffs()
✦ internal/drivers/natgw/driver.go            — NATGatewayDriver Virtual Object
✦ internal/drivers/natgw/driver_test.go       — Unit tests for driver
✦ internal/drivers/natgw/aws_test.go          — Unit tests for error classification
✦ internal/drivers/natgw/drift_test.go        — Unit tests for drift detection
✦ internal/core/provider/natgw_adapter.go     — NATGatewayAdapter
✦ internal/core/provider/natgw_adapter_test.go — Unit tests for adapter
✦ schemas/aws/natgw/natgw.cue                 — CUE schema
✦ tests/integration/natgw_driver_test.go       — Integration tests
✎ cmd/praxis-network/main.go                  — Add NATGateway driver .Bind()
✎ internal/core/provider/registry.go           — Add NewNATGatewayAdapter
✎ justfile                                     — Add natgw test targets
```

---

## Step 1 — CUE Schema

**File**: `schemas/aws/natgw/natgw.cue`

```cue
package natgw

#NATGateway: {
    apiVersion: "praxis.io/v1"
    kind:       "NATGateway"

    metadata: {
        name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9._-]{0,254}$"
        labels: [string]: string
    }

    spec: {
        // region is the AWS region.
        region: string

        // subnetId is the public subnet to place the NAT Gateway in.
        // For public NAT Gateways, this must be a public subnet (with IGW route).
        subnetId: string

        // connectivityType controls whether the NAT Gateway is public or private.
        // - "public": routes traffic to the internet via an IGW. Requires an EIP.
        // - "private": routes traffic to other VPCs or on-premises networks.
        //   Does not require an EIP.
        // Default: "public"
        // Immutable after creation.
        connectivityType: "public" | "private" | *"public"

        // allocationId is the Elastic IP allocation ID for public NAT Gateways.
        // Required when connectivityType is "public".
        // Ignored when connectivityType is "private".
        // Typically: "${resources.my-eip.outputs.allocationId}"
        // Immutable after creation — changing requires replacement.
        allocationId?: string

        // tags applied to the NAT Gateway resource.
        tags: [string]: string
    }

    outputs?: {
        natGatewayId:    string
        subnetId:        string
        vpcId:           string
        connectivityType: string
        state:           string  // "pending", "available", "deleting", "deleted", "failed"
        publicIp?:       string  // only for public NAT GWs
        privateIp:       string
        allocationId?:   string  // only for public NAT GWs
        networkInterfaceId: string
    }
}
```

**Key decisions**:

- `connectivityType` exposes both "public" and "private" NAT Gateways. Private NAT
  GWs (added by AWS in 2021) enable VPC-to-VPC traffic without internet access.
- `allocationId` is required for public NAT GWs but optional/ignored for private.
  The driver validates this at provision time.
- Both `connectivityType` and `allocationId` are immutable — changing either requires
  NAT Gateway replacement (delete + recreate). AWS does not support modifying these.
- `subnetId` is immutable — a NAT Gateway cannot be moved to a different subnet.

---

## Step 2 — AWS Client Factory

**NO CHANGES NEEDED** — uses `NewEC2Client`.

---

## Step 3 — Driver Types

**File**: `internal/drivers/natgw/types.go`

```go
package natgw

import "github.com/praxiscloud/praxis/pkg/types"

const ServiceName = "NATGateway"

type NATGatewaySpec struct {
    Account          string            `json:"account,omitempty"`
    Region           string            `json:"region"`
    SubnetId         string            `json:"subnetId"`
    ConnectivityType string            `json:"connectivityType,omitempty"` // "public" or "private"
    AllocationId     string            `json:"allocationId,omitempty"`
    Tags             map[string]string `json:"tags,omitempty"`
    ManagedKey       string            `json:"managedKey,omitempty"`
}

type NATGatewayOutputs struct {
    NatGatewayId       string `json:"natGatewayId"`
    SubnetId           string `json:"subnetId"`
    VpcId              string `json:"vpcId"`
    ConnectivityType   string `json:"connectivityType"`
    State              string `json:"state"`
    PublicIp           string `json:"publicIp,omitempty"`
    PrivateIp          string `json:"privateIp"`
    AllocationId       string `json:"allocationId,omitempty"`
    NetworkInterfaceId string `json:"networkInterfaceId"`
}

type ObservedState struct {
    NatGatewayId       string            `json:"natGatewayId"`
    SubnetId           string            `json:"subnetId"`
    VpcId              string            `json:"vpcId"`
    ConnectivityType   string            `json:"connectivityType"`
    State              string            `json:"state"`
    PublicIp           string            `json:"publicIp,omitempty"`
    PrivateIp          string            `json:"privateIp"`
    AllocationId       string            `json:"allocationId,omitempty"`
    NetworkInterfaceId string            `json:"networkInterfaceId"`
    Tags               map[string]string `json:"tags"`
}

type NATGatewayState struct {
    Desired            NATGatewaySpec       `json:"desired"`
    Observed           ObservedState        `json:"observed"`
    Outputs            NATGatewayOutputs    `json:"outputs"`
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

**File**: `internal/drivers/natgw/aws.go`

### NATGatewayAPI Interface

```go
type NATGatewayAPI interface {
    // CreateNATGateway creates a new NAT Gateway in the specified subnet.
    // For public type, allocationId is required.
    // Returns the NAT Gateway ID.
    CreateNATGateway(ctx context.Context, spec NATGatewaySpec) (string, error)

    // DescribeNATGateway returns the full observed state.
    DescribeNATGateway(ctx context.Context, natGatewayId string) (ObservedState, error)

    // DeleteNATGateway deletes a NAT Gateway.
    // NAT GWs take several minutes to delete. The API returns immediately
    // and the NAT GW transitions through "deleting" → "deleted" states.
    DeleteNATGateway(ctx context.Context, natGatewayId string) error

    // WaitUntilAvailable blocks until the NAT Gateway reaches "available" state.
    WaitUntilAvailable(ctx context.Context, natGatewayId string) error

    // WaitUntilDeleted blocks until the NAT Gateway reaches "deleted" state.
    WaitUntilDeleted(ctx context.Context, natGatewayId string) error

    // UpdateTags replaces user-managed tags.
    UpdateTags(ctx context.Context, natGatewayId string, tags map[string]string) error

    // FindByManagedKey searches for NAT Gateways tagged with praxis:managed-key.
    FindByManagedKey(ctx context.Context, managedKey string) (string, error)
}
```

### Implementation Notes

- `CreateNATGateway`: Uses `CreateNatGateway` API with `TagSpecifications`.
  For public type, pass `AllocationId`. For private type, omit `AllocationId`
  and set `ConnectivityType: "private"`.
- `DescribeNATGateway`: Uses `DescribeNatGateways`. Returns the NAT GW's
  addresses (public IP, private IP, allocation ID, network interface ID).
  Must filter out "deleted" NAT Gateways — AWS returns them for up to an hour.
- `DeleteNATGateway`: Uses `DeleteNatGateway`. Returns immediately; the NAT GW
  enters "deleting" state. The driver waits for deletion to complete.
- `WaitUntilAvailable`: Uses `ec2sdk.NewNatGatewayAvailableWaiter`. NAT Gateways
  take **1-5 minutes** to become available (significantly longer than VPCs/subnets).
  Timeout: 10 minutes.
- `WaitUntilDeleted`: Uses `ec2sdk.NewNatGatewayDeletedWaiter`. Deletion takes
  **1-5 minutes**. Timeout: 10 minutes.
- `FindByManagedKey`: Filters by tag. Must also filter out `"deleted"` and
  `"failed"` state NAT Gateways to avoid false ownership conflicts.

### Error Classification

```go
func IsNotFound(err error) bool         // "NatGatewayNotFound"
func IsInvalidParam(err error) bool     // "InvalidParameterValue"
func IsAllocationInUse(err error) bool  // "InvalidAllocationID.NotFound", EIP not available
func IsSubnetNotFound(err error) bool   // Subnet doesn't exist
func IsFailed(state string) bool        // NAT GW in "failed" state
```

---

## Step 5 — Drift Detection

**File**: `internal/drivers/natgw/drift.go`

### Drift Rules

| Field | Mutable? | Drift Checked? | How Corrected |
|---|---|---|---|
| `tags` | Yes | **Yes** | CreateTags / DeleteTags |
| `subnetId` | **No** | **No** | Requires replacement |
| `connectivityType` | **No** | **No** | Requires replacement |
| `allocationId` | **No** | **No** | Requires replacement |

NAT Gateways are almost entirely immutable. The only mutable attribute is tags.
All infrastructure properties (subnet, connectivity type, EIP) are immutable.

```go
func HasDrift(desired NATGatewaySpec, observed ObservedState) bool {
    // Skip drift check if NAT GW is not available
    if observed.State != "available" {
        return false
    }
    if !tagsMatch(desired.Tags, observed.Tags) {
        return true
    }
    return false
}

func ComputeFieldDiffs(desired NATGatewaySpec, observed ObservedState) []FieldDiffEntry {
    // Mutable: tags
    // Immutable (reported only): subnetId, connectivityType, allocationId
}
```

---

## Step 6 — Driver Implementation

**File**: `internal/drivers/natgw/driver.go`

### Provision

1. Input validation: `region`, `subnetId` required. If `connectivityType` is
   `"public"` (or default), `allocationId` is required. If private, `allocationId`
   must be empty.
2. Load current state. If NAT GW exists, verify it still exists and isn't "failed".
3. Pre-flight ownership check via `FindByManagedKey`.
4. Create NAT Gateway if new: `CreateNATGateway`.
5. Wait for "available" state: `WaitUntilAvailable` — NAT GWs take 1-5 minutes.
6. Re-provision path: converge tags only (all other fields are immutable). If
   immutable fields changed, report as informational diff but do not correct.
7. Final describe → build outputs → commit state.
8. Schedule reconcile.

### Delete

1. Block `ModeObserved` (409).
2. Delete NAT Gateway: `DeleteNATGateway`.
3. Wait for "deleted" state: `WaitUntilDeleted` — takes 1-5 minutes.
4. Set tombstone state.

> **Deletion wait**: Unlike VPC/Subnet deletion which is nearly instant, NAT Gateway
> deletion is asynchronous. The driver must wait for the "deleted" state before
> setting the tombstone. This is wrapped in `restate.Run()` and is idempotent:
> if Restate replays the handler, the waiter will either see the NAT GW is already
> deleted or resume waiting.

### Failed State Recovery

NAT Gateways can enter a "failed" state during creation (e.g., EIP already in use,
subnet in wrong VPC, insufficient capacity). When the driver detects a "failed"
state:

1. Delete the failed NAT Gateway.
2. Wait for "deleted" state.
3. Retry creation from scratch.

This handles the common case where a transient issue blocked creation.

---

## Step 7 — Provider Adapter

**File**: `internal/core/provider/natgw_adapter.go`

- **Key Scope**: `KeyScopeRegion`
- **BuildKey**: `JoinKey(spec.Region, metadata.name)`
- **BuildImportKey**: `JoinKey(region, natGatewayId)`

---

## Step 8 — Registry Integration

Add `NewNATGatewayAdapterWithRegistry(accounts)` to `NewRegistry()`.

---

## Step 9 — Binary Entry Point & Dockerfile

Add `.Bind(restate.Reflect(natgw.NewNATGatewayDriver(cfg.Auth())))` to
`cmd/praxis-network/main.go`.

---

## Step 10 — Docker Compose & Justfile

No Docker Compose changes. Justfile:

```makefile
test-natgw:
    go test ./internal/drivers/natgw/... -v -count=1 -race
```

---

## Step 11 — Unit Tests

### `internal/drivers/natgw/driver_test.go`

1. `TestProvision_CreatesPublicNATGW` — happy path with EIP.
2. `TestProvision_CreatesPrivateNATGW` — happy path without EIP.
3. `TestProvision_MissingSubnetIdFails` — terminal error 400.
4. `TestProvision_PublicMissingAllocationIdFails` — terminal error 400.
5. `TestProvision_PrivateWithAllocationIdFails` — terminal error 400.
6. `TestProvision_IdempotentReprovision` — no duplicate NAT GW.
7. `TestProvision_TagUpdate` — tags updated.
8. `TestProvision_ConflictFails` — FindByManagedKey → 409.
9. `TestProvision_FailedStateRecovery` — deletes failed NAT GW and retries.
10. `TestProvision_WaitsForAvailable` — verifies WaitUntilAvailable is called.
11. `TestImport_ExistingNATGW` — describes, synthesizes spec, returns outputs.
12. `TestImport_NotFoundFails` — terminal error 404.
13. `TestImport_DefaultsToObservedMode`.
14. `TestDelete_DeletesAndWaits` — calls Delete + WaitUntilDeleted.
15. `TestDelete_AlreadyDeleted` — IsNotFound returns success.
16. `TestDelete_ObservedModeBlocked` — terminal error 409.
17. `TestReconcile_NoDrift` — no changes.
18. `TestReconcile_DetectsTagDrift` — tags corrected.
19. `TestReconcile_ObservedModeReportsOnly`.
20. `TestReconcile_FailedStateReported` — error status when state is "failed".
21. `TestGetStatus_ReturnsCurrentState`.
22. `TestGetOutputs_ReturnsOutputs`.

### `internal/drivers/natgw/drift_test.go`

1. `TestHasDrift_NoDrift` — identical → false.
2. `TestHasDrift_TagChanged` → true.
3. `TestHasDrift_NonAvailableSkipped` — "pending" state → false.
4. `TestHasDrift_SubnetChangedNoDrift` — immutable, not checked → false.
5. `TestComputeFieldDiffs_Tags` — tag diff reported.
6. `TestComputeFieldDiffs_ImmutableSubnet` — reported with replacement suffix.
7. `TestComputeFieldDiffs_ImmutableConnectivity` — reported with replacement suffix.
8. `TestTagsMatch_IgnoresPraxisTags`.

---

## Step 12 — Integration Tests

**File**: `tests/integration/natgw_driver_test.go`

1. **TestNATGWProvision_CreatesPublicNATGW** — Creates VPC + subnet + EIP,
   provisions public NAT GW, verifies via DescribeNatGateways.
2. **TestNATGWProvision_CreatesPrivateNATGW** — Creates VPC + subnet, provisions
   private NAT GW (no EIP needed).
3. **TestNATGWProvision_Idempotent** — Two provisions, same outputs.
4. **TestNATGWImport_Existing** — Creates NAT GW via SDK, imports.
5. **TestNATGWDelete_DeletesAndWaits** — Provisions, deletes, verifies gone.
6. **TestNATGWReconcile_TagDrift** — Changes tags, reconcile corrects.
7. **TestNATGWGetStatus_ReturnsReady**.

### LocalStack NAT Gateway Compatibility Note

LocalStack supports CreateNatGateway, DescribeNatGateways, DeleteNatGateway, and
tag operations. NAT Gateway state transitions (pending → available, deleting →
deleted) may be instantaneous in LocalStack rather than taking minutes as in real
AWS. Integration tests should account for this by using short wait timeouts.

---

## NAT Gateway-Specific Design Decisions

### 1. Nearly Immutable Resource

NAT Gateways have the most immutable properties of any networking resource:

- Subnet placement: immutable.
- Connectivity type (public/private): immutable.
- Elastic IP association: immutable.
- Only tags are mutable.

This means most "updates" require a replacement (delete + recreate). The driver
does NOT implement automatic replacement — it reports immutable field changes as
informational diffs. The user must delete and re-provision manually (or the DAG
scheduler handles it if automatic replacement semantics are implemented in the
future).

### 2. Elastic IP Association

For public NAT Gateways, an EIP must be allocated before the NAT Gateway is
created. The EIP is managed by the EIP driver (already implemented). The template
references the EIP's allocation ID:

```text
allocationId: "${resources.my-eip.outputs.allocationId}"
```

The DAG ensures the EIP is provisioned before the NAT Gateway.

When the NAT Gateway is deleted, the EIP is **not** automatically released — it
remains allocated for potential reuse. The EIP driver manages its own lifecycle.

### 3. Wait Time: Significantly Longer Than Other Resources

NAT Gateway creation and deletion take 1-5 minutes, much longer than VPCs
(seconds), subnets (seconds), or IGWs (instantaneous). The driver uses AWS SDK
waiters with generous timeouts:

- Creation: 10-minute timeout with `NatGatewayAvailableWaiter`.
- Deletion: 10-minute timeout with `NatGatewayDeletedWaiter`.

If Restate replays the handler mid-wait, the full wait starts over. This is
acceptable because the waiter is idempotent and simply polls DescribeNatGateways
until the target state is reached.

### 4. Failed State Handling

NAT Gateways can enter a "failed" state during creation. Common causes:

- EIP is already associated with another resource.
- Subnet doesn't have an internet gateway route (for public NAT GWs).
- Insufficient capacity in the AZ.

Failed NAT Gateways are never recovered automatically by AWS — they persist in
"failed" state until deleted. The driver handles this by:

1. Detecting "failed" state on re-provision or reconcile.
2. Deleting the failed NAT Gateway.
3. Recreating from scratch.

### 5. Cost Considerations

NAT Gateways incur hourly charges and per-GB data processing charges. This is
informational only — the driver does not enforce cost controls. However, the
reconcile handler logs a warning if a NAT Gateway is in "failed" state, since
failed NAT GWs don't incur charges but indicate a configuration problem.

### 6. NAT Gateway as a DAG Dependency

In compound templates:

```text
VPC → Subnet (public) → EIP → NAT Gateway → Route Table (0.0.0.0/0 → nat-xxx)
                                                ↑
                                        Subnet (private) ← EC2 Instance
```

The NAT Gateway depends on the subnet and EIP. Private subnet route tables depend
on the NAT Gateway. During delete: routes first, then NAT GW (wait for deletion),
then EIP, then subnet, then VPC.

### 7. FindByManagedKey: State Filtering

Unlike VPCs which disappear immediately after deletion, NAT Gateways linger in
"deleted" state for up to an hour. `FindByManagedKey` must filter out NAT GWs
in "deleted" and "failed" states to avoid false ownership conflicts. Only "pending"
and "available" NAT GWs are considered live matches.

---

## Design Decisions (Resolved)

1. **Should the driver auto-allocate an EIP for public NAT GWs?**
   No. EIP allocation is the EIP driver's responsibility. The template must
   explicitly reference an EIP resource. This keeps resource lifecycles clean —
   if the NAT GW is deleted, the EIP remains for potential reuse.

2. **Should the driver create private NAT GWs by default?**
   No. Public is the default because the most common use case is internet access
   for private subnets. Private NAT GWs are for VPC-to-VPC/on-premises routing.

3. **Should the driver support secondary IP addresses?**
   Not in v1. AWS supports assigning secondary private IPs to NAT Gateways for
   high-throughput scenarios. This adds complexity and serves a narrow use case.

4. **Should the driver validate that the subnet has an IGW route?**
   No. This is a validation that crosses driver boundaries. The NAT Gateway driver
   creates the NAT GW in the specified subnet; if the subnet lacks internet
   connectivity, the NAT GW will fail (for public type) and the error is surfaced.

5. **Should the driver wait for deletion before returning from Delete?**
   Yes. Unlike VPC deletion which is synchronous, NAT GW deletion is asynchronous.
   Returning before the NAT GW is fully deleted could cause DAG ordering issues:
   the VPC or subnet might attempt deletion while the NAT GW still holds resources.

---

## Example Template

```cue
import (
    "praxis.io/schemas/aws/vpc"
    "praxis.io/schemas/aws/subnet"
    "praxis.io/schemas/aws/eip"
    "praxis.io/schemas/aws/natgw"
    "praxis.io/schemas/aws/routetable"
)

resources: {
    "my-vpc": vpc.#VPC & {
        apiVersion: "praxis.io/v1"
        kind:       "VPC"
        metadata: name: "app-vpc"
        spec: {
            region:             "\(variables.region)"
            cidrBlock:          "10.0.0.0/16"
            enableDnsHostnames: true
            enableDnsSupport:   true
            tags: { Name: "app-vpc" }
        }
    }

    "public-subnet": subnet.#Subnet & {
        apiVersion: "praxis.io/v1"
        kind:       "Subnet"
        metadata: name: "public-a"
        spec: {
            region:              "\(variables.region)"
            vpcId:               "${resources.my-vpc.outputs.vpcId}"
            cidrBlock:           "10.0.1.0/24"
            availabilityZone:    "\(variables.region)a"
            mapPublicIpOnLaunch: true
            tags: { Name: "public-a" }
        }
    }

    "nat-eip": eip.#ElasticIP & {
        apiVersion: "praxis.io/v1"
        kind:       "ElasticIP"
        metadata: name: "nat-eip"
        spec: {
            region: "\(variables.region)"
            tags: { Name: "nat-eip" }
        }
    }

    "nat-gw": natgw.#NATGateway & {
        apiVersion: "praxis.io/v1"
        kind:       "NATGateway"
        metadata: name: "nat-gw-a"
        spec: {
            region:           "\(variables.region)"
            subnetId:         "${resources.public-subnet.outputs.subnetId}"
            connectivityType: "public"
            allocationId:     "${resources.nat-eip.outputs.allocationId}"
            tags: {
                Name:        "nat-gw-a"
                Environment: "production"
            }
        }
    }

    "private-rt": routetable.#RouteTable & {
        apiVersion: "praxis.io/v1"
        kind:       "RouteTable"
        metadata: name: "private-rt"
        spec: {
            region: "\(variables.region)"
            vpcId:  "${resources.my-vpc.outputs.vpcId}"
            routes: [{
                destinationCidrBlock: "0.0.0.0/0"
                natGatewayId:         "${resources.nat-gw.outputs.natGatewayId}"
            }]
            tags: { Name: "private-rt" }
        }
    }
}

variables: {
    region: string | *"us-east-1"
}
```

---

## Checklist

- [ ] **Schema**: `schemas/aws/natgw/natgw.cue` created
- [ ] **Types**: `internal/drivers/natgw/types.go` created
- [ ] **AWS API**: `internal/drivers/natgw/aws.go` created
- [ ] **Drift**: `internal/drivers/natgw/drift.go` created
- [ ] **Driver**: `internal/drivers/natgw/driver.go` created with all 6 handlers
- [ ] **Adapter**: `internal/core/provider/natgw_adapter.go` created
- [ ] **Registry**: `internal/core/provider/registry.go` updated
- [ ] **Entry point**: `.Bind()` added to `cmd/praxis-network/main.go`
- [ ] **Justfile**: Updated with natgw test targets
- [ ] **Unit tests**: driver, drift, aws helpers created
- [ ] **Unit tests (adapter)**: `internal/core/provider/natgw_adapter_test.go` created
- [ ] **Integration tests**: `tests/integration/natgw_driver_test.go` created
- [ ] **Conflict check**: `FindByManagedKey` with state filtering
- [ ] **Ownership tag**: `praxis:managed-key` written at creation
- [ ] **Import default mode**: ModeObserved
- [ ] **Delete mode guard**: Blocks deletion for ModeObserved (409)
- [ ] **Delete waits**: WaitUntilDeleted before tombstone
- [ ] **Failed state recovery**: Detects and recreates failed NAT GWs
- [ ] **Public/private validation**: allocationId required for public, rejected for private
- [ ] **Build passes**: `go build ./...`
- [ ] **Unit tests pass**: `go test ./internal/drivers/natgw/... -race`
- [ ] **Integration tests pass**: `go test ./tests/integration/ -run TestNATGW -tags=integration`
