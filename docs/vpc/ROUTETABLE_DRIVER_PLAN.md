# Route Table Driver — Implementation Plan

> Target: A Restate Virtual Object driver that manages AWS VPC Route Tables, following
> the exact patterns established by the VPC, Subnet, Security Group, and EC2 Instance
> drivers.
>
> Key scope: `KeyScopeCustom` — key format is `vpcId~metadata.name`, permanent and
> immutable for the lifetime of the Virtual Object. The AWS-assigned Route Table ID
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
16. [Route Table-Specific Design Decisions](#route-table-specific-design-decisions)
17. [Design Decisions (Resolved)](#design-decisions-resolved)
18. [Checklist](#checklist)

---

## 1. Overview & Scope

The Route Table driver manages the lifecycle of AWS **Route Tables** and their
**routes** and **subnet associations**. Routes and subnet associations are tightly
coupled to the route table — they have no independent lifecycle — so they are managed
as sub-resources within this driver rather than as separate drivers.

### Why Route Tables

Route tables define packet forwarding rules for VPC traffic. Without managed route
tables, subnets use the VPC's main route table by default, which only routes within
the VPC. Public subnets need a route to an Internet Gateway. Private subnets need
a route to a NAT Gateway. Custom routing topologies (VPN, peering, Transit Gateway)
all flow through route table configuration. Route tables are the third pillar of
VPC networking after VPCs and subnets.

### Resource Scope for This Plan

| In Scope | Out of Scope (Separate Drivers) |
|---|---|
| Route table creation, deletion | Internet gateways (target) |
| Routes (static, IGW, NAT GW, peering, etc.) | NAT gateways (target) |
| Subnet associations | VPN gateways (target) |
| Tags | Transit gateways (target) |
| Import and drift detection | VPC endpoints (target) |
| Ownership tag enforcement | VPC peering connections (target) |
| Route propagation enable/disable | |

### Route Table Driver Contract

| Handler | Context | Purpose |
|---|---|---|
| `Provision` | `ObjectContext` (exclusive) | Create or converge a route table with routes and associations |
| `Import` | `ObjectContext` (exclusive) | Adopt an existing route table |
| `Delete` | `ObjectContext` (exclusive) | Delete a route table (blocked for Observed mode) |
| `Reconcile` | `ObjectContext` (exclusive) | Detect/correct drift (report-only for Observed mode) |
| `GetStatus` | `ObjectSharedContext` (shared) | Return current status |
| `GetOutputs` | `ObjectSharedContext` (shared) | Return route table outputs |

### Downstream Consumers

```text
${resources.my-rt.outputs.routeTableId}  → Subnet associations, VPN route propagation
```

---

## 2. Key Strategy

### Key Format: `vpcId~metadata.name`

Route table names are meaningful only within a VPC. The key uses `KeyScopeCustom`
with the format `vpcId~metadata.name`, matching the Security Group and Subnet
patterns.

1. **BuildKey**: returns `vpcId~metadata.name`.
2. **BuildImportKey**: returns `region~routeTableId`.
3. **Import**: `ModeObserved` by default — deleting a route table disrupts all
   associated subnets' traffic routing.

### Constraint: metadata.name Must Be Unique Within a VPC

Route tables in AWS don't have native names — they only have Name tags. Praxis
requires `metadata.name` to be unique per VPC for managed route tables.

### Conflict Enforcement via Ownership Tags

Same pattern as VPC/Subnet: `praxis:managed-key = <vpcId~metadata.name>` written
at creation, checked by `FindByManagedKey` pre-flight.

---

## 3. File Inventory

```text
✦ internal/drivers/routetable/types.go             — Spec, Outputs, ObservedState, State
✦ internal/drivers/routetable/aws.go               — RouteTableAPI interface + realRouteTableAPI
✦ internal/drivers/routetable/drift.go             — HasDrift(), ComputeFieldDiffs()
✦ internal/drivers/routetable/driver.go            — RouteTableDriver Virtual Object
✦ internal/drivers/routetable/driver_test.go       — Unit tests for driver (mocked AWS)
✦ internal/drivers/routetable/aws_test.go          — Unit tests for error classification
✦ internal/drivers/routetable/drift_test.go        — Unit tests for drift detection
✦ internal/core/provider/routetable_adapter.go     — RouteTableAdapter
✦ internal/core/provider/routetable_adapter_test.go — Unit tests for adapter
✦ schemas/aws/routetable/routetable.cue            — CUE schema
✦ tests/integration/routetable_driver_test.go      — Integration tests
✎ cmd/praxis-network/main.go                      — Add RouteTable driver .Bind()
✎ internal/core/provider/registry.go               — Add NewRouteTableAdapter
✎ justfile                                         — Add routetable test targets
```

---

## Step 1 — CUE Schema

**File**: `schemas/aws/routetable/routetable.cue`

```cue
package routetable

#RouteTable: {
    apiVersion: "praxis.io/v1"
    kind:       "RouteTable"

    metadata: {
        name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9._-]{0,254}$"
        labels: [string]: string
    }

    spec: {
        // region is the AWS region.
        region: string

        // vpcId is the VPC to create the route table in.
        vpcId: string

        // routes defines the static routes in this route table.
        // The local route (VPC CIDR → local) is added automatically by AWS
        // and should NOT be included here.
        routes: [...#Route]

        // associations defines which subnets are associated with this route table.
        // Associating a subnet replaces its current route table association
        // (default: main route table).
        associations: [...#Association]

        // tags applied to the route table resource.
        tags: [string]: string
    }

    outputs?: {
        routeTableId: string
        vpcId:        string
        ownerId:      string
        routes: [...{
            destinationCidrBlock: string
            gatewayId?:           string
            natGatewayId?:        string
            vpcPeeringConnectionId?: string
            state:                string
        }]
        associations: [...{
            associationId:       string
            subnetId:            string
            main:                bool
        }]
    }
}

#Route: {
    // destinationCidrBlock is the CIDR for the route's destination.
    destinationCidrBlock: string & =~"^([0-9]{1,3}\\.){3}[0-9]{1,3}/([0-9]|[12][0-9]|3[0-2])$"

    // Exactly one of the following targets must be specified.
    gatewayId?:                string  // Internet Gateway or Virtual Private Gateway
    natGatewayId?:             string  // NAT Gateway
    vpcPeeringConnectionId?:   string  // VPC Peering Connection
    transitGatewayId?:         string  // Transit Gateway
    networkInterfaceId?:       string  // Network Interface (ENI)
    vpcEndpointId?:            string  // VPC Endpoint (Gateway type)
}

#Association: {
    // subnetId is the subnet to associate with this route table.
    subnetId: string
}
```

**Key decisions**:

- Routes are an ordered list in the spec but compared as a set for drift detection
  (AWS returns routes in arbitrary order).
- The local route (`vpcCidr → local`) is auto-created by AWS and excluded from
  drift detection. Users must NOT specify it in their template.
- Each route requires exactly one target — the CUE schema doesn't enforce mutual
  exclusivity of target fields (CUE can express this but the pipeline doesn't
  evaluate structural constraints). The driver validates this at provision time.
- Associations are modeled as part of the route table spec, not as separate resources,
  because they have no independent lifecycle.

---

## Step 2 — AWS Client Factory

**NO CHANGES NEEDED** — uses `NewEC2Client`.

---

## Step 3 — Driver Types

**File**: `internal/drivers/routetable/types.go`

```go
package routetable

import "github.com/praxiscloud/praxis/pkg/types"

const ServiceName = "RouteTable"

type Route struct {
    DestinationCidrBlock     string `json:"destinationCidrBlock"`
    GatewayId                string `json:"gatewayId,omitempty"`
    NatGatewayId             string `json:"natGatewayId,omitempty"`
    VpcPeeringConnectionId   string `json:"vpcPeeringConnectionId,omitempty"`
    TransitGatewayId         string `json:"transitGatewayId,omitempty"`
    NetworkInterfaceId       string `json:"networkInterfaceId,omitempty"`
    VpcEndpointId            string `json:"vpcEndpointId,omitempty"`
}

type Association struct {
    SubnetId string `json:"subnetId"`
}

type RouteTableSpec struct {
    Account      string            `json:"account,omitempty"`
    Region       string            `json:"region"`
    VpcId        string            `json:"vpcId"`
    Routes       []Route           `json:"routes,omitempty"`
    Associations []Association     `json:"associations,omitempty"`
    Tags         map[string]string `json:"tags,omitempty"`
    ManagedKey   string            `json:"managedKey,omitempty"`
}

type ObservedRoute struct {
    DestinationCidrBlock     string `json:"destinationCidrBlock"`
    GatewayId                string `json:"gatewayId,omitempty"`
    NatGatewayId             string `json:"natGatewayId,omitempty"`
    VpcPeeringConnectionId   string `json:"vpcPeeringConnectionId,omitempty"`
    TransitGatewayId         string `json:"transitGatewayId,omitempty"`
    NetworkInterfaceId       string `json:"networkInterfaceId,omitempty"`
    VpcEndpointId            string `json:"vpcEndpointId,omitempty"`
    State                    string `json:"state"` // "active", "blackhole"
    Origin                   string `json:"origin"` // "CreateRouteTable", "CreateRoute", "EnableVgwRoutePropagation"
}

type ObservedAssociation struct {
    AssociationId string `json:"associationId"`
    SubnetId      string `json:"subnetId"`
    Main          bool   `json:"main"`
}

type RouteTableOutputs struct {
    RouteTableId string                `json:"routeTableId"`
    VpcId        string                `json:"vpcId"`
    OwnerId      string                `json:"ownerId"`
    Routes       []ObservedRoute       `json:"routes,omitempty"`
    Associations []ObservedAssociation `json:"associations,omitempty"`
}

type ObservedState struct {
    RouteTableId string                `json:"routeTableId"`
    VpcId        string                `json:"vpcId"`
    OwnerId      string                `json:"ownerId"`
    Routes       []ObservedRoute       `json:"routes"`
    Associations []ObservedAssociation `json:"associations"`
    Tags         map[string]string     `json:"tags"`
}

type RouteTableState struct {
    Desired            RouteTableSpec       `json:"desired"`
    Observed           ObservedState        `json:"observed"`
    Outputs            RouteTableOutputs    `json:"outputs"`
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

**File**: `internal/drivers/routetable/aws.go`

### RouteTableAPI Interface

```go
type RouteTableAPI interface {
    CreateRouteTable(ctx context.Context, spec RouteTableSpec) (string, error)
    DescribeRouteTable(ctx context.Context, routeTableId string) (ObservedState, error)
    DeleteRouteTable(ctx context.Context, routeTableId string) error

    // Route management
    CreateRoute(ctx context.Context, routeTableId string, route Route) error
    DeleteRoute(ctx context.Context, routeTableId string, destinationCidr string) error
    ReplaceRoute(ctx context.Context, routeTableId string, route Route) error

    // Association management
    AssociateSubnet(ctx context.Context, routeTableId string, subnetId string) (string, error)
    DisassociateSubnet(ctx context.Context, associationId string) error

    // Tag management
    UpdateTags(ctx context.Context, routeTableId string, tags map[string]string) error

    // Ownership
    FindByManagedKey(ctx context.Context, managedKey string) (string, error)
}
```

### Implementation Notes

- `CreateRouteTable`: Uses `CreateRouteTable` API with `TagSpecifications`.
  Does NOT create routes or associations — those are separate calls.
- `DescribeRouteTable`: Uses `DescribeRouteTables` API. Returns routes,
  associations, and tags in a single call (unlike VPC which needs separate
  attribute calls).
- `DeleteRouteTable`: Must disassociate all subnets before deleting. The main
  route table cannot be deleted (AWS restriction).
- `CreateRoute`: Uses `CreateRoute` API — one call per route.
- `DeleteRoute`: Uses `DeleteRoute` API — one call per route.
- `ReplaceRoute`: Uses `ReplaceRoute` API — atomic update for a destination.
- `AssociateSubnet`: Uses `AssociateRouteTable` API. Returns association ID.
- `DisassociateSubnet`: Uses `DisassociateRouteTable` API.

### Error Classification

```go
func IsNotFound(err error) bool            // "InvalidRouteTableID.NotFound"
func IsRouteNotFound(err error) bool       // "InvalidRoute.NotFound"
func IsRouteAlreadyExists(err error) bool  // "RouteAlreadyExists"
func IsMainRouteTable(err error) bool      // Cannot delete main route table
func IsInvalidParam(err error) bool        // "InvalidParameterValue"
func IsInvalidRoute(err error) bool        // "InvalidRoute.InvalidState", invalid target
```

---

## Step 5 — Drift Detection

**File**: `internal/drivers/routetable/drift.go`

### Drift Rules

| Field | Mutable? | Drift Checked? | How Corrected |
|---|---|---|---|
| `routes` | Yes | **Yes** | CreateRoute / DeleteRoute / ReplaceRoute |
| `associations` | Yes | **Yes** | AssociateRouteTable / DisassociateRouteTable |
| `tags` | Yes | **Yes** | CreateTags / DeleteTags |
| `vpcId` | **No** | **No** | Requires replacement |

### Route Drift Detection

Routes are compared as **sets**, not ordered lists. The comparison key is
`destinationCidrBlock`. For each destination CIDR:

- **Present in desired but not observed** → route was deleted externally → add it.
- **Present in observed but not desired** → route was added externally → remove it.
- **Present in both but target differs** → route was modified → replace it.

The local route (origin=`CreateRouteTable`) is **excluded** from drift — it's
auto-managed by AWS and cannot be modified.

Propagated routes (origin=`EnableVgwRoutePropagation`) are also excluded from drift —
they're managed by VPN gateway route propagation, not by static configuration.

### Association Drift Detection

Associations are compared by `subnetId`:

- **Desired subnet not associated** → associate it.
- **Associated subnet not in desired** → disassociate it.
- Main route table associations (main=true) are excluded from drift.

```go
func HasDrift(desired RouteTableSpec, observed ObservedState) bool
func ComputeFieldDiffs(desired RouteTableSpec, observed ObservedState) []FieldDiffEntry

// NormalizeRoute converts a Route to a comparable key for set-based comparison.
func NormalizeRoute(r Route) string

// filterManagedRoutes excludes local and propagated routes from comparison.
func filterManagedRoutes(routes []ObservedRoute) []ObservedRoute
```

---

## Step 6 — Driver Implementation

**File**: `internal/drivers/routetable/driver.go`

### Provision

1. Input validation: `region`, `vpcId` required. Each route must have exactly one target.
2. Load current state. If route table exists, verify it still exists.
3. Pre-flight ownership check via `FindByManagedKey`.
4. Create route table if new: `CreateRouteTable` (creates only the table + local route).
5. Add routes: iterate `spec.Routes`, call `CreateRoute` for each. Apply
   **add-before-remove** ordering (same as SG rules) to avoid connectivity gaps.
6. Add associations: iterate `spec.Associations`, call `AssociateSubnet` for each.
7. Re-provision path: reconcile routes (add missing, remove extra, replace changed)
   and associations (associate missing, disassociate extra).
8. Update tags if changed.
9. Final describe → build outputs → commit state.
10. Schedule reconcile.

### Delete

1. Block `ModeObserved` (409).
2. Block main route table deletion (409) — cannot delete the VPC's main route table.
3. Disassociate all subnets first.
4. Delete all non-local routes.
5. Delete route table.
6. Set tombstone state.

### Route Application Order

When updating routes, apply **add-before-remove** to minimize connectivity disruption:

1. **Add new routes** that exist in desired but not observed.
2. **Replace modified routes** where the destination exists but the target changed.
3. **Remove stale routes** that exist in observed but not desired.

This ensures that traffic always has a valid route during convergence.

---

## Step 7 — Provider Adapter

**File**: `internal/core/provider/routetable_adapter.go`

- **Key Scope**: `KeyScopeCustom`
- **BuildKey**: `JoinKey(spec.VpcId, metadata.name)`
- **BuildImportKey**: `JoinKey(region, routeTableId)`

---

## Step 8 — Registry Integration

Add `NewRouteTableAdapterWithRegistry(accounts)` to `NewRegistry()`.

---

## Step 9 — Binary Entry Point & Dockerfile

Add `.Bind(restate.Reflect(routetable.NewRouteTableDriver(cfg.Auth())))` to
`cmd/praxis-network/main.go`.

---

## Step 10 — Docker Compose & Justfile

No Docker Compose changes. Justfile:

```makefile
test-routetable:
    go test ./internal/drivers/routetable/... -v -count=1 -race
```

---

## Step 11 — Unit Tests

### `internal/drivers/routetable/driver_test.go`

1. `TestProvision_CreatesNewRouteTable` — happy path with routes and associations.
2. `TestProvision_MissingVpcIdFails` — terminal error 400.
3. `TestProvision_RouteWithMultipleTargetsFails` — terminal error 400.
4. `TestProvision_RouteWithNoTargetFails` — terminal error 400.
5. `TestProvision_IdempotentReprovision` — no duplicate route table.
6. `TestProvision_AddNewRoute` — re-provision adds new route.
7. `TestProvision_RemoveRoute` — re-provision removes stale route.
8. `TestProvision_ReplaceRouteTarget` — re-provision replaces changed target.
9. `TestProvision_AddAssociation` — re-provision associates new subnet.
10. `TestProvision_RemoveAssociation` — re-provision disassociates old subnet.
11. `TestProvision_TagUpdate` — tag change calls UpdateTags.
12. `TestProvision_ConflictFails` — FindByManagedKey conflict → 409.
13. `TestImport_ExistingRouteTable` — describes, synthesizes spec, returns outputs.
14. `TestImport_NotFoundFails` — terminal error 404.
15. `TestImport_DefaultsToObservedMode` — ModeObserved.
16. `TestDelete_DeletesRouteTable` — disassociates, removes routes, deletes.
17. `TestDelete_MainRouteTableBlocked` — terminal error 409.
18. `TestDelete_ObservedModeBlocked` — terminal error 409.
19. `TestDelete_AlreadyGone` — IsNotFound returns success.
20. `TestReconcile_NoDrift` — no changes.
21. `TestReconcile_DetectsRouteDrift` — drift=true, routes corrected.
22. `TestReconcile_DetectsAssociationDrift` — drift=true, associations corrected.
23. `TestReconcile_DetectsTagDrift` — drift=true, tags corrected.
24. `TestReconcile_ObservedModeReportsOnly` — drift=true, correcting=false.
25. `TestReconcile_IgnoresLocalRoute` — local route not considered drift.
26. `TestReconcile_IgnoresPropagatedRoutes` — propagated routes excluded.
27. `TestGetStatus_ReturnsCurrentState`.
28. `TestGetOutputs_ReturnsOutputs`.

### `internal/drivers/routetable/drift_test.go`

1. `TestHasDrift_NoDrift` — identical returns false.
2. `TestHasDrift_RouteAdded` — extra observed route → true.
3. `TestHasDrift_RouteRemoved` — missing observed route → true.
4. `TestHasDrift_RouteTargetChanged` — different target → true.
5. `TestHasDrift_AssociationAdded` — extra association → true.
6. `TestHasDrift_AssociationRemoved` — missing association → true.
7. `TestHasDrift_TagChanged` — true.
8. `TestHasDrift_LocalRouteIgnored` — local route doesn't cause drift.
9. `TestHasDrift_PropagatedRouteIgnored` — propagated routes excluded.
10. `TestNormalizeRoute_Sorting` — routes sorted by destination for comparison.
11. `TestFilterManagedRoutes_ExcludesLocal` — filters correctly.

---

## Step 12 — Integration Tests

**File**: `tests/integration/routetable_driver_test.go`

1. **TestRouteTableProvision_CreatesWithRoutes** — Creates VPC + IGW, provisions
   route table with IGW route, verifies routes.
2. **TestRouteTableProvision_WithSubnetAssociation** — Provisions RT with subnet
   association, verifies subnet is associated.
3. **TestRouteTableProvision_Idempotent** — Two provisions, same outputs.
4. **TestRouteTableImport_Existing** — Creates RT via SDK, imports.
5. **TestRouteTableDelete_Deletes** — Provisions, deletes, verifies gone.
6. **TestRouteTableReconcile_RouteAddedExternally** — Adds route via SDK,
   reconcile removes it.
7. **TestRouteTableReconcile_RouteRemovedExternally** — Removes route via SDK,
   reconcile restores it.
8. **TestRouteTableReconcile_TagDrift** — Changes tags, reconcile corrects.

---

## Route Table-Specific Design Decisions

### 1. Routes as Sub-Resources

Routes have no independent AWS resource identity (no route ID) — they're identified
by their `destinationCidrBlock` within a route table. Modeling them as separate
Praxis drivers would be artificial overhead. They're managed as a set within the
route table spec, following the Security Group's pattern for ingress/egress rules.

### 2. Add-Before-Remove Route Updates

When the route set changes, new routes are created before stale routes are deleted.
This ensures continuous connectivity during convergence. If a "replace" is needed
(same destination, different target), `ReplaceRoute` is used instead of
delete+create to maintain atomicity.

### 3. Main Route Table Protection

Each VPC has exactly one main route table. AWS prevents its deletion. The driver
detects the main route table via the `main` flag in associations and blocks delete
with a terminal error (409). Import of the main route table is allowed for
read-only monitoring.

### 4. Local Route Handling

AWS automatically adds a local route to every route table
(`vpcCidr → local`, origin=`CreateRouteTable`). This route:

- Cannot be deleted or modified.
- Is excluded from drift detection.
- Is excluded from the desired spec (users must NOT include it).
- Is included in outputs for visibility.

### 5. Route Propagation

Route propagation (from VPN gateways) is a boolean toggle per route table, not a
static route. Propagated routes (origin=`EnableVgwRoutePropagation`) appear in the
route table but are managed by the VPN gateway, not by this driver. The driver:

- Excludes propagated routes from drift detection.
- Includes propagated routes in outputs for visibility.
- Does NOT support `enableRoutePropagation` in the initial spec — this can be added
  later when a VPN Gateway driver exists.

### 6. Association Semantics

A subnet can be associated with exactly one route table. If a subnet is currently
associated with route table A and the driver associates it with route table B,
the old association is implicitly replaced by AWS. The driver does NOT need to
disassociate first — `AssociateRouteTable` handles this atomically.

However, during **drift correction**, if a subnet was associated externally, the
driver must `DisassociateRouteTable` to restore the desired state.

---

## Design Decisions (Resolved)

1. **Should routes and associations be separate drivers?**
   No. Routes have no independent identity (the "key" is destinationCidrBlock within
   a route table). Associations are just pointers. Both are sub-resources.

2. **Should the driver support IPv6 destination CIDRs?**
   No. Consistent with VPC and Subnet drivers — IPv6 is out of scope.

3. **Should the driver support route table replacement (main RT swap)?**
   Not in v1. Replacing the main route table is a `ReplaceRouteTableAssociation`
   call that's complex and rarely needed outside VPC peering/migration scenarios.

4. **Should the driver validate that route targets exist?**
   No. Route targets (IGW IDs, NAT GW IDs, etc.) are validated by AWS at route
   creation time. The driver surfaces AWS validation errors as terminal errors.

---

## Example Template

```cue
import (
    "praxis.io/schemas/aws/vpc"
    "praxis.io/schemas/aws/subnet"
    "praxis.io/schemas/aws/routetable"
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

    "public-rt": routetable.#RouteTable & {
        apiVersion: "praxis.io/v1"
        kind:       "RouteTable"
        metadata: name: "public-rt"
        spec: {
            region: "\(variables.region)"
            vpcId:  "${resources.my-vpc.outputs.vpcId}"
            routes: [{
                destinationCidrBlock: "0.0.0.0/0"
                gatewayId:            "${resources.my-igw.outputs.internetGatewayId}"
            }]
            associations: [{
                subnetId: "${resources.public-subnet.outputs.subnetId}"
            }]
            tags: {
                Name: "public-rt"
                Tier: "public"
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

- [x] **Schema**: `schemas/aws/routetable/routetable.cue` created
- [x] **Types**: `internal/drivers/routetable/types.go` created
- [x] **AWS API**: `internal/drivers/routetable/aws.go` created
- [x] **Drift**: `internal/drivers/routetable/drift.go` created (set-based route comparison)
- [x] **Driver**: `internal/drivers/routetable/driver.go` created with all 6 handlers
- [x] **Adapter**: `internal/core/provider/routetable_adapter.go` created
- [x] **Registry**: `internal/core/provider/registry.go` updated
- [x] **Entry point**: `.Bind()` added to `cmd/praxis-network/main.go`
- [x] **Justfile**: Updated with routetable test targets
- [x] **Unit tests (drift)**: `internal/drivers/routetable/drift_test.go` created
- [x] **Unit tests (aws)**: `internal/drivers/routetable/aws_test.go` created
- [x] **Unit tests (driver)**: `internal/drivers/routetable/driver_test.go` created
- [x] **Unit tests (adapter)**: `internal/core/provider/routetable_adapter_test.go` created
- [x] **Integration tests**: `tests/integration/routetable_driver_test.go` created
- [x] **Conflict check**: `FindByManagedKey` in RouteTableAPI
- [x] **Ownership tag**: `praxis:managed-key` written at creation
- [x] **Import default mode**: ModeObserved
- [x] **Delete guards**: ModeObserved (409) + main route table (409)
- [x] **Local route exclusion**: Excluded from drift detection
- [x] **Add-before-remove**: Route updates applied safely
- [x] **Build passes**: `go build ./...`
- [x] **Unit tests pass**: `go test ./internal/drivers/routetable/... -race`
- [x] **Integration tests pass**: `go test ./tests/integration/ -run TestRouteTable -tags=integration`
