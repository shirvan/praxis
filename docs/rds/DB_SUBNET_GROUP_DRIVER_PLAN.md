# DB Subnet Group Driver — Implementation Spec

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
12. [Step 9 — Storage Driver Pack Entry Point](#step-9--storage-driver-pack-entry-point)
13. [Step 10 — Docker Compose & Justfile](#step-10--docker-compose--justfile)
14. [Step 11 — Unit Tests](#step-11--unit-tests)
15. [Step 12 — Integration Tests](#step-12--integration-tests)
16. [Subnet-Group-Specific Design Decisions](#subnet-group-specific-design-decisions)
17. [Design Decisions (Resolved)](#design-decisions-resolved)
18. [Checklist](#checklist)

---

## 1. Overview & Scope

The DB Subnet Group driver manages the lifecycle of RDS **DB Subnet Groups**.
A DB Subnet Group is a collection of subnets (typically private) that RDS uses
to place DB instances and Aurora clusters in a VPC. It is a prerequisite for
any VPC-based RDS deployment.

This is the simplest driver in the RDS family. The resource has few mutable
attributes (subnet list, description, tags) and requires no waiters — subnet
groups are available immediately after creation.

The driver follows the established Virtual Object contract:

| Handler | Context | Purpose |
|---|---|---|
| `Provision` | `ObjectContext` (exclusive) | Create or converge a DB subnet group |
| `Import` | `ObjectContext` (exclusive) | Adopt an existing DB subnet group |
| `Delete` | `ObjectContext` (exclusive) | Delete a DB subnet group |
| `Reconcile` | `ObjectContext` (exclusive) | Detect/correct drift |
| `GetStatus` | `ObjectSharedContext` (shared) | Return current status |
| `GetOutputs` | `ObjectSharedContext` (shared) | Return subnet group outputs |

### Mutable vs Immutable Attributes

| Attribute | Mutability | Notes |
|---|---|---|
| `groupName` | Immutable | Part of Virtual Object key |
| `region` | Immutable | Cannot move between regions |
| `description` | Mutable | Changed via `ModifyDBSubnetGroup` |
| `subnetIds` | Mutable | Full replace via `ModifyDBSubnetGroup`. Must span ≥2 AZs. |
| `tags` | Mutable | Full replace via ARN-based tagging |

### Downstream Consumers

```text
${resources.my-subnet-group.outputs.groupName}   → RDS Instance dbSubnetGroupName
${resources.my-subnet-group.outputs.groupName}   → Aurora Cluster dbSubnetGroupName
${resources.my-subnet-group.outputs.arn}          → IAM policies
```

---

## 2. Key Strategy

### Key Scope: `KeyScopeRegion`

DB subnet group names are unique per region per account. The key is
`region~groupName`.

```text
region~groupName
```

### BuildKey vs BuildImportKey

- **`BuildKey(resourceDoc)`**: Extracts `spec.region` and `metadata.name`.
  Returns `region~metadata.name`.

- **`BuildImportKey(region, resourceID)`**: Returns `region~resourceID`.

### BuildImportKey Produces the Same Key as BuildKey

Subnet group names are user-chosen and unique within a region. Import and
template management converge on the same Virtual Object.

### Identifier Uniqueness

RDS enforces subnet group name uniqueness per region per account.
`CreateDBSubnetGroup` returns `DBSubnetGroupAlreadyExistsFault` if the name
is taken.

---

## 3. File Inventory

```text
✦ schemas/aws/rds/db_subnet_group.cue                          — CUE schema
✦ internal/drivers/dbsubnetgroup/types.go                       — Spec, Outputs, ObservedState, State
✦ internal/drivers/dbsubnetgroup/aws.go                         — DBSubnetGroupAPI interface + real impl
✦ internal/drivers/dbsubnetgroup/drift.go                       — HasDrift(), ComputeFieldDiffs()
✦ internal/drivers/dbsubnetgroup/driver.go                      — DBSubnetGroupDriver Virtual Object
✦ internal/drivers/dbsubnetgroup/driver_test.go                 — Unit tests for driver
✦ internal/drivers/dbsubnetgroup/aws_test.go                    — Unit tests for error classification
✦ internal/drivers/dbsubnetgroup/drift_test.go                  — Unit tests for drift detection
✦ internal/core/provider/dbsubnetgroup_adapter.go               — Adapter
✦ internal/core/provider/dbsubnetgroup_adapter_test.go          — Adapter unit tests
✦ tests/integration/dbsubnetgroup_driver_test.go                — Integration tests
✎ internal/core/provider/registry.go                            — Add NewDBSubnetGroupAdapter
✔ cmd/praxis-storage/main.go                                   — Bind DBSubnetGroupDriver
```

---

## Step 1 — CUE Schema

**File**: `schemas/aws/rds/db_subnet_group.cue`

```cue
package rds

#DBSubnetGroup: {
    apiVersion: "praxis.io/v1"
    kind:       "DBSubnetGroup"

    metadata: {
        // name maps to the DB subnet group name.
        // Must match RDS naming rules: 1-255 chars, alphanumeric + hyphens,
        // first char must be a letter, cannot end with hyphen or contain
        // two consecutive hyphens.
        name: string & =~"^[a-zA-Z][a-zA-Z0-9-]{0,253}[a-zA-Z0-9]$"
        labels: [string]: string
    }

    spec: {
        // region is the AWS region.
        region: string

        // description is the subnet group description.
        description: string

        // subnetIds is the list of VPC subnet IDs.
        // Must include subnets in at least 2 Availability Zones (AWS requirement).
        subnetIds: [...string] & [_, _, ...]

        // tags applied to the DB subnet group.
        tags: [string]: string
    }

    outputs?: {
        groupName:           string
        arn:                 string
        vpcId:               string
        subnetIds:           [...string]
        availabilityZones:   [...string]
        status:              string
    }
}
```

### Key Design Decisions

- **`subnetIds` minimum 2**: AWS requires DB subnet groups to span at least 2
  Availability Zones. The CUE constraint `[_, _, ...]` enforces ≥2 entries.
  The actual AZ-span validation happens at the AWS API level.

- **`description` required**: AWS requires a non-empty description for DB subnet
  groups, unlike most other resources.

- **`vpcId` in outputs only**: The VPC is inferred from the provided subnets.
  Users don't specify it directly — AWS derives it.

---

## Step 2 — AWS Client Factory

Uses the shared `NewRDSClient` from `internal/infra/awsclient/client.go`.

---

## Step 3 — Driver Types

**File**: `internal/drivers/dbsubnetgroup/types.go`

```go
package dbsubnetgroup

import "github.com/shirvan/praxis/pkg/types"

const ServiceName = "DBSubnetGroup"

type DBSubnetGroupSpec struct {
    Account     string            `json:"account,omitempty"`
    Region      string            `json:"region"`
    GroupName   string            `json:"groupName"`
    Description string            `json:"description"`
    SubnetIds   []string          `json:"subnetIds"`
    Tags        map[string]string `json:"tags,omitempty"`
}

type DBSubnetGroupOutputs struct {
    GroupName         string   `json:"groupName"`
    ARN               string   `json:"arn"`
    VpcId             string   `json:"vpcId"`
    SubnetIds         []string `json:"subnetIds"`
    AvailabilityZones []string `json:"availabilityZones"`
    Status            string   `json:"status"`
}

type ObservedState struct {
    GroupName         string            `json:"groupName"`
    ARN               string            `json:"arn"`
    Description       string            `json:"description"`
    VpcId             string            `json:"vpcId"`
    SubnetIds         []string          `json:"subnetIds"`
    AvailabilityZones []string          `json:"availabilityZones"`
    Status            string            `json:"status"`
    Tags              map[string]string `json:"tags"`
}

type DBSubnetGroupState struct {
    Desired            DBSubnetGroupSpec    `json:"desired"`
    Observed           ObservedState        `json:"observed"`
    Outputs            DBSubnetGroupOutputs `json:"outputs"`
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

**File**: `internal/drivers/dbsubnetgroup/aws.go`

### DBSubnetGroupAPI Interface

```go
type DBSubnetGroupAPI interface {
    // CreateDBSubnetGroup creates a new DB subnet group.
    CreateDBSubnetGroup(ctx context.Context, spec DBSubnetGroupSpec) (string, error)

    // DescribeDBSubnetGroup returns the observed state.
    DescribeDBSubnetGroup(ctx context.Context, groupName string) (ObservedState, error)

    // ModifyDBSubnetGroup updates the description and/or subnet list.
    ModifyDBSubnetGroup(ctx context.Context, groupName string, description string, subnetIds []string) error

    // DeleteDBSubnetGroup deletes the subnet group.
    DeleteDBSubnetGroup(ctx context.Context, groupName string) error

    // UpdateTags replaces all tags on the subnet group (by ARN).
    UpdateTags(ctx context.Context, arn string, tags map[string]string) error

    // ListTags returns all tags on the subnet group.
    ListTags(ctx context.Context, arn string) (map[string]string, error)
}
```

### realDBSubnetGroupAPI Implementation

```go
type realDBSubnetGroupAPI struct {
    client  *rds.Client
    limiter *ratelimit.Limiter
}

func NewDBSubnetGroupAPI(client *rds.Client) DBSubnetGroupAPI {
    return &realDBSubnetGroupAPI{
        client:  client,
        limiter: ratelimit.New("rds", 15, 8),
    }
}
```

### Key Implementation Details

#### `CreateDBSubnetGroup`

```go
func (r *realDBSubnetGroupAPI) CreateDBSubnetGroup(ctx context.Context, spec DBSubnetGroupSpec) (string, error) {
    input := &rds.CreateDBSubnetGroupInput{
        DBSubnetGroupName:        aws.String(spec.GroupName),
        DBSubnetGroupDescription: aws.String(spec.Description),
        SubnetIds:                spec.SubnetIds,
    }
    if len(spec.Tags) > 0 {
        input.Tags = toRDSTags(spec.Tags)
    }

    out, err := r.client.CreateDBSubnetGroup(ctx, input)
    if err != nil {
        return "", err
    }
    return aws.ToString(out.DBSubnetGroup.DBSubnetGroupArn), nil
}
```

#### `DescribeDBSubnetGroup`

```go
func (r *realDBSubnetGroupAPI) DescribeDBSubnetGroup(ctx context.Context, groupName string) (ObservedState, error) {
    out, err := r.client.DescribeDBSubnetGroups(ctx, &rds.DescribeDBSubnetGroupsInput{
        DBSubnetGroupName: aws.String(groupName),
    })
    if err != nil {
        return ObservedState{}, err
    }
    if len(out.DBSubnetGroups) == 0 {
        return ObservedState{}, fmt.Errorf("DB subnet group %s not found", groupName)
    }
    group := out.DBSubnetGroups[0]

    obs := ObservedState{
        GroupName:   aws.ToString(group.DBSubnetGroupName),
        ARN:         aws.ToString(group.DBSubnetGroupArn),
        Description: aws.ToString(group.DBSubnetGroupDescription),
        VpcId:       aws.ToString(group.VpcId),
        Status:      aws.ToString(group.SubnetGroupStatus),
    }

    azSet := make(map[string]bool)
    for _, subnet := range group.Subnets {
        subnetId := aws.ToString(subnet.SubnetIdentifier)
        obs.SubnetIds = append(obs.SubnetIds, subnetId)
        if subnet.SubnetAvailabilityZone != nil {
            az := aws.ToString(subnet.SubnetAvailabilityZone.Name)
            azSet[az] = true
        }
    }
    for az := range azSet {
        obs.AvailabilityZones = append(obs.AvailabilityZones, az)
    }

    return obs, nil
}
```

#### `ModifyDBSubnetGroup`

```go
func (r *realDBSubnetGroupAPI) ModifyDBSubnetGroup(ctx context.Context, groupName string, description string, subnetIds []string) error {
    input := &rds.ModifyDBSubnetGroupInput{
        DBSubnetGroupName:        aws.String(groupName),
        DBSubnetGroupDescription: aws.String(description),
        SubnetIds:                subnetIds,
    }
    _, err := r.client.ModifyDBSubnetGroup(ctx, input)
    return err
}
```

### Error Classification

```go
func IsNotFound(err error) bool {
    if err == nil {
        return false
    }
    var apiErr smithy.APIError
    if errors.As(err, &apiErr) {
        return apiErr.ErrorCode() == "DBSubnetGroupNotFoundFault"
    }
    return strings.Contains(err.Error(), "DBSubnetGroupNotFoundFault")
}

func IsAlreadyExists(err error) bool {
    if err == nil {
        return false
    }
    var apiErr smithy.APIError
    if errors.As(err, &apiErr) {
        return apiErr.ErrorCode() == "DBSubnetGroupAlreadyExistsFault"
    }
    return false
}

func IsInUse(err error) bool {
    if err == nil {
        return false
    }
    var apiErr smithy.APIError
    if errors.As(err, &apiErr) {
        // Subnet group is in use by a DB instance or cluster
        return apiErr.ErrorCode() == "InvalidDBSubnetGroupStateFault"
    }
    return false
}

func IsInvalidSubnet(err error) bool {
    if err == nil {
        return false
    }
    var apiErr smithy.APIError
    if errors.As(err, &apiErr) {
        return apiErr.ErrorCode() == "InvalidSubnet" ||
               apiErr.ErrorCode() == "DBSubnetGroupDoesNotCoverEnoughAZs"
    }
    return false
}
```

---

## Step 5 — Drift Detection

**File**: `internal/drivers/dbsubnetgroup/drift.go`

### Core Functions

**`HasDrift(desired DBSubnetGroupSpec, observed ObservedState) bool`**

```go
func HasDrift(desired DBSubnetGroupSpec, observed ObservedState) bool {
    if desired.Description != observed.Description {
        return true
    }
    if !subnetIdsEqual(desired.SubnetIds, observed.SubnetIds) {
        return true
    }
    return !tagsMatch(desired.Tags, observed.Tags)
}
```

#### Subnet ID Comparison

Subnet IDs are compared as sets (order-independent):

```go
func subnetIdsEqual(desired, observed []string) bool {
    if len(desired) != len(observed) {
        return false
    }
    dSet := make(map[string]bool, len(desired))
    for _, id := range desired {
        dSet[id] = true
    }
    for _, id := range observed {
        if !dSet[id] {
            return false
        }
    }
    return true
}
```

**`ComputeFieldDiffs`**

Reports diffs for: `description`, `subnetIds` (sorted for display), `tags`.

---

## Step 6 — Driver Implementation

**File**: `internal/drivers/dbsubnetgroup/driver.go`

### Provision Handler

1. **Input validation**: `groupName`, `description`, `subnetIds` required.
   `subnetIds` must have ≥2 entries. Returns `TerminalError(400)`.

2. **Load state, set Provisioning**.

3. **Re-provision path**: If `state.Outputs.GroupName` is non-empty, describes
   the group. If deleted externally (404), falls through to creation.

4. **Create**: Calls `api.CreateDBSubnetGroup`.
   - `IsAlreadyExists` → `TerminalError(409)`.
   - `IsInvalidSubnet` → `TerminalError(400)` with helpful message about AZ coverage.

5. **Converge** (re-provision path): If description or subnet list has drifted,
   calls `api.ModifyDBSubnetGroup` with full desired state.

6. **Update tags**: `api.UpdateTags`.

7. **Describe final state**: `api.DescribeDBSubnetGroup`.

8. **Set Ready, save state, schedule reconcile**.

### Import Handler

1. Describes the subnet group.
2. Gets tags via `api.ListTags`.
3. Synthesizes spec from observed state.
4. Sets `ModeObserved`.
5. Schedules reconciliation.

### Delete Handler

1. Sets `Deleting`.
2. Calls `api.DeleteDBSubnetGroup`.
   - `IsNotFound` → silent success.
   - `IsInUse` → `TerminalError(409)` with message about instances/clusters
     still referencing the subnet group.
3. Sets `Deleted`.

### Reconcile Handler

Standard 5-minute reconcile timer. Subnet groups have very low drift risk but
reconcile catches manual modifications.

---

## Step 7 — Provider Adapter

**File**: `internal/core/provider/dbsubnetgroup_adapter.go`

### Methods

**`Scope() KeyScope`** → `KeyScopeRegion`

**`Kind() string`** → `"DBSubnetGroup"`

**`ServiceName() string`** → `"DBSubnetGroup"`

**`BuildKey(resourceDoc json.RawMessage) (string, error)`**:
Returns `region~metadata.name`.

**`BuildImportKey(region, resourceID string) (string, error)`**:
Returns `region~resourceID`.

**`Plan(ctx, key, account, desiredSpec) (DiffOperation, []FieldDiff, error)`**:
Calls `api.DescribeDBSubnetGroup`. Not found → `OpCreate`. Found → diff
description, subnets, tags. No diffs → `OpNoOp`. Diffs → `OpUpdate`.

---

## Step 8 — Registry Integration

Add `NewDBSubnetGroupAdapterWithRegistry(auth)` to `NewRegistry()`.

---

## Step 9 — Storage Driver Pack Entry Point

Bind `DBSubnetGroupDriver` in `cmd/praxis-storage/main.go`.

---

## Step 10 — Docker Compose & Justfile

Part of the `praxis-storage` service (port 9081). No additional configuration needed.

### Justfile Targets

| Target | Command |
|---|---|
| `test-dbsubnetgroup` | `go test ./internal/drivers/dbsubnetgroup/... -v -count=1 -race` |
| `test-dbsubnetgroup-integration` | `go test ./tests/integration/ -run TestDBSubnetGroup -v -count=1 -tags=integration -timeout=10m` |

---

## Step 11 — Unit Tests

### `internal/drivers/dbsubnetgroup/drift_test.go`

| Test | Purpose |
|---|---|
| `TestHasDrift_NoDrift` | Matching state → no drift |
| `TestHasDrift_DescriptionDrift` | Description change → drift |
| `TestHasDrift_SubnetAdded` | Additional subnet → drift |
| `TestHasDrift_SubnetRemoved` | Missing subnet → drift |
| `TestHasDrift_SubnetChanged` | Different subnet ID → drift |
| `TestHasDrift_SubnetOrderIndependent` | Same subnets, different order → no drift |
| `TestHasDrift_TagDrift` | Tag change → drift |
| `TestComputeFieldDiffs_DescriptionDiff` | Reports description change |
| `TestComputeFieldDiffs_SubnetDiff` | Reports subnet list change (sorted) |

### `internal/drivers/dbsubnetgroup/aws_test.go`

| Test | Purpose |
|---|---|
| `TestIsNotFound_True` | Not-found error → true |
| `TestIsNotFound_OtherError` | Other error → false |
| `TestIsAlreadyExists_True` | Duplicate name → true |
| `TestIsInUse_True` | In-use error → true |
| `TestIsInvalidSubnet_NotEnoughAZs` | AZ coverage error → true |
| `TestIsInvalidSubnet_InvalidSubnet` | Invalid subnet ID → true |

### `internal/drivers/dbsubnetgroup/driver_test.go`

| Test | Purpose |
|---|---|
| `TestSpecFromObserved_RoundTrip` | Observed → spec preserves fields |
| `TestServiceName` | Returns "DBSubnetGroup" |
| `TestOutputsFromObserved` | Correct output mapping |

### `internal/core/provider/dbsubnetgroup_adapter_test.go`

| Test | Purpose |
|---|---|
| `TestDBSubnetGroupAdapter_BuildKey` | Returns `region~groupName` |
| `TestDBSubnetGroupAdapter_BuildImportKey` | Returns `region~groupName` |
| `TestDBSubnetGroupAdapter_Kind` | Returns "DBSubnetGroup" |
| `TestDBSubnetGroupAdapter_Scope` | Returns `KeyScopeRegion` |

---

## Step 12 — Integration Tests

**File**: `tests/integration/dbsubnetgroup_driver_test.go`

### Prerequisites

Integration tests require VPC and subnets in at least 2 AZs. Use Moto to
create test VPC infrastructure before running subnet group tests.

### Test Cases

| Test | Description |
|---|---|
| `TestDBSubnetGroup_Provision_Creates` | Creates subnet group with 2 subnets in 2 AZs. Verifies in Moto. |
| `TestDBSubnetGroup_Provision_Idempotent` | Provisions same spec twice. Same ARN returned. |
| `TestDBSubnetGroup_Provision_AddSubnet` | Re-provisions with an additional subnet. Verifies 3 subnets. |
| `TestDBSubnetGroup_Provision_RemoveSubnet` | Re-provisions with one fewer subnet (still ≥2 AZs). Verifies update. |
| `TestDBSubnetGroup_Provision_ChangeDescription` | Re-provisions with changed description. |
| `TestDBSubnetGroup_Import_Existing` | Creates subnet group via API, imports via driver. Verifies Observed mode. |
| `TestDBSubnetGroup_Delete_Removes` | Provisions, deletes. Verifies removal. |
| `TestDBSubnetGroup_Delete_InUse` | Creates subnet group used by DB instance, attempts delete. Verifies 409. |
| `TestDBSubnetGroup_Reconcile_DetectsDrift` | Modifies subnets via API, reconciles. Verifies correction. |
| `TestDBSubnetGroup_GetStatus_ReturnsReady` | Provisions, checks `GetStatus` returns Ready. |

---

## Subnet-Group-Specific Design Decisions

### 1. Simplest RDS Driver

DB Subnet Groups have the fewest moving parts of any RDS resource: a name,
description, subnet list, and tags. No wait logic, no deletion protection,
no apply-immediately semantics. This makes it the ideal first driver to
implement in the RDS pack.

### 2. Full Subnet List Replace

`ModifyDBSubnetGroup` requires the full subnet list, not an add/remove delta.
The driver always sends the complete desired list. This is simpler and matches
the declarative "desired state" model.

### 3. AZ Coverage Validation

AWS requires subnet groups to span ≥2 AZs. The CUE schema enforces ≥2 subnet
IDs, but cannot validate AZ coverage (subnets could be in the same AZ). The
driver propagates the `DBSubnetGroupDoesNotCoverEnoughAZs` error as a clear
`TerminalError(400)`.

### 4. VPC Consistency

All subnets in a subnet group must belong to the same VPC. The driver does not
validate this — AWS returns `InvalidSubnet` if subnets span VPCs. This error is
passed through as `TerminalError(400)`.

### 5. No Wait Logic

Subnet groups are available immediately after `CreateDBSubnetGroup` returns
successfully. No waiter is needed, unlike DB instances and clusters.

---

## Design Decisions (Resolved)

1. **Should subnetIds be order-sensitive?**
   No. Subnet group membership is a set. Drift detection compares as sets
   (order-independent). This avoids false drift when AWS returns subnets in a
   different order.

2. **Should the driver validate subnet AZ distribution?**
   No. AWS performs this validation and returns a clear error. Replicating
   this validation would require additional AWS API calls (`DescribeSubnets`) and
   adds complexity for no benefit.

3. **Should the driver support the `default` subnet group?**
   No. AWS creates a `default` subnet group automatically in each VPC. Import
   can adopt it, but the driver should not try to create or delete it. The default
   group name is reserved.

---

## Checklist

- [x] **Schema**: `schemas/aws/rds/db_subnet_group.cue` created
- [x] **Types**: `internal/drivers/dbsubnetgroup/types.go` created
- [x] **AWS API**: `internal/drivers/dbsubnetgroup/aws.go` created
- [x] **Drift**: `internal/drivers/dbsubnetgroup/drift.go` created
- [x] **Driver**: `internal/drivers/dbsubnetgroup/driver.go` created with all 6 handlers
- [x] **Adapter**: `internal/core/provider/dbsubnetgroup_adapter.go` created
- [x] **Registry**: `internal/core/provider/registry.go` updated
- [x] **Entry point**: `cmd/praxis-storage/main.go` — `.Bind()` call for DB Subnet Group driver
- [x] **Unit tests (drift)**: `internal/drivers/dbsubnetgroup/drift_test.go`
- [x] **Unit tests (aws helpers)**: `internal/drivers/dbsubnetgroup/aws_test.go`
- [x] **Unit tests (driver)**: `internal/drivers/dbsubnetgroup/driver_test.go`
- [x] **Unit tests (adapter)**: `internal/core/provider/dbsubnetgroup_adapter_test.go`
- [x] **Integration tests**: `tests/integration/dbsubnetgroup_driver_test.go`
- [x] **Subnet set comparison**: Order-independent drift detection
- [x] **AZ error handling**: Clear TerminalError for insufficient AZ coverage
- [x] **Import default mode**: `ModeObserved`
- [x] **Delete mode guard**: Delete handler blocks for ModeObserved (409)
- [x] **Build passes**: `go build ./...` succeeds
- [x] **Unit tests pass**: `go test ./internal/drivers/dbsubnetgroup/... -race`
- [x] **Integration tests pass**: `go test ./tests/integration/ -run TestDBSubnetGroup -tags=integration`
