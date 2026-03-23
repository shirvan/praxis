# DB Parameter Group Driver — Implementation Plan

> **Implementation note:** This plan references a `praxis-database` driver pack.
> The actual implementation places the DB Parameter Group driver in **`praxis-storage`**
> (`cmd/praxis-storage/main.go`).

> Target: A Restate Virtual Object driver that manages Amazon RDS DB Parameter
> Groups and Aurora DB Cluster Parameter Groups, providing full lifecycle
> management including creation, configuration, import, deletion, drift detection,
> and drift correction for parameter values and tags.
>
> Key scope: `KeyScopeRegion` — key format is `region~groupName`, permanent
> and immutable for the lifetime of the Virtual Object.

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
12. [Step 9 — Database Driver Pack Entry Point](#step-9--database-driver-pack-entry-point)
13. [Step 10 — Docker Compose & Justfile](#step-10--docker-compose--justfile)
14. [Step 11 — Unit Tests](#step-11--unit-tests)
15. [Step 12 — Integration Tests](#step-12--integration-tests)
16. [Parameter-Group-Specific Design Decisions](#parameter-group-specific-design-decisions)
17. [Design Decisions (Resolved)](#design-decisions-resolved)
18. [Checklist](#checklist)

---

## 1. Overview & Scope

The DB Parameter Group driver manages the lifecycle of RDS **DB Parameter Groups**
and Aurora **DB Cluster Parameter Groups**. These are configuration containers that
hold engine-specific tuning parameters applied to DB instances or Aurora clusters.

Both DB Parameter Groups and DB Cluster Parameter Groups share the same resource
model and API patterns. The driver uses a `type` field (`"db"` or `"cluster"`) to
distinguish between them. This avoids duplicating a nearly-identical driver.

The driver follows the established Virtual Object contract:

| Handler | Context | Purpose |
|---|---|---|
| `Provision` | `ObjectContext` (exclusive) | Create or converge a parameter group |
| `Import` | `ObjectContext` (exclusive) | Adopt an existing parameter group |
| `Delete` | `ObjectContext` (exclusive) | Delete a parameter group |
| `Reconcile` | `ObjectContext` (exclusive) | Detect/correct drift (report-only for Observed) |
| `GetStatus` | `ObjectSharedContext` (shared) | Return current status |
| `GetOutputs` | `ObjectSharedContext` (shared) | Return parameter group outputs |

### Mutable vs Immutable Attributes

| Attribute | Mutability | Notes |
|---|---|---|
| `groupName` | Immutable | Part of Virtual Object key |
| `region` | Immutable | Cannot move between regions |
| `type` | Immutable | `"db"` (instance) or `"cluster"` — set at creation |
| `family` | Immutable | Engine parameter family (e.g., `"postgres16"`, `"aurora-postgresql16"`); cannot change |
| `description` | Immutable | Set at creation, cannot be modified |
| `parameters` | Mutable | Individual parameter values; can be added, changed, or removed |
| `tags` | Mutable | Full replace via ARN-based tagging |

### Downstream Consumers

```
${resources.my-param-group.outputs.groupName}   → RDS Instance dbParameterGroupName
${resources.my-cluster-pg.outputs.groupName}     → Aurora Cluster dbClusterParameterGroupName
${resources.my-param-group.outputs.arn}          → IAM policies
```

---

## 2. Key Strategy

### Key Scope: `KeyScopeRegion`

Parameter group names are unique per region per account. The key is
`region~groupName`.

```
region~groupName
```

### BuildKey vs BuildImportKey

- **`BuildKey(resourceDoc)`**: Extracts `spec.region` and `metadata.name`.
  Returns `region~metadata.name`.

- **`BuildImportKey(region, resourceID)`**: Returns `region~resourceID`.

### BuildImportKey Produces the Same Key as BuildKey

Parameter group names are user-chosen and unique within a region. Import and
template management converge on the same Virtual Object.

### Identifier Uniqueness

RDS enforces parameter group name uniqueness per region per account.
`CreateDBParameterGroup` / `CreateDBClusterParameterGroup` return
`DBParameterGroupAlreadyExistsFault` if the name is taken.

---

## 3. File Inventory

```text
✦ schemas/aws/rds/db_parameter_group.cue                          — CUE schema
✦ internal/drivers/dbparametergroup/types.go                       — Spec, Outputs, ObservedState, State
✦ internal/drivers/dbparametergroup/aws.go                         — DBParameterGroupAPI interface + real impl
✦ internal/drivers/dbparametergroup/drift.go                       — HasDrift(), ComputeFieldDiffs()
✦ internal/drivers/dbparametergroup/driver.go                      — DBParameterGroupDriver Virtual Object
✦ internal/drivers/dbparametergroup/driver_test.go                 — Unit tests for driver
✦ internal/drivers/dbparametergroup/aws_test.go                    — Unit tests for error classification
✦ internal/drivers/dbparametergroup/drift_test.go                  — Unit tests for drift detection
✦ internal/core/provider/dbparametergroup_adapter.go               — Adapter
✦ internal/core/provider/dbparametergroup_adapter_test.go          — Adapter unit tests
✦ tests/integration/dbparametergroup_driver_test.go                — Integration tests
✎ internal/core/provider/registry.go                               — Add NewDBParameterGroupAdapter
✎ cmd/praxis-database/main.go                                      — Bind DBParameterGroupDriver
```

---

## Step 1 — CUE Schema

**File**: `schemas/aws/rds/db_parameter_group.cue`

```cue
package rds

#DBParameterGroup: {
    apiVersion: "praxis.io/v1"
    kind:       "DBParameterGroup"

    metadata: {
        // name maps to the parameter group name.
        name: string & =~"^[a-zA-Z][a-zA-Z0-9-]{0,253}[a-zA-Z0-9]$"
        labels: [string]: string
    }

    spec: {
        // region is the AWS region.
        region: string

        // type distinguishes DB Parameter Group from DB Cluster Parameter Group.
        // "db"      → CreateDBParameterGroup (for RDS instances)
        // "cluster" → CreateDBClusterParameterGroup (for Aurora clusters)
        type: "db" | "cluster" | *"db"

        // family is the parameter group family (e.g., "postgres16",
        // "aurora-postgresql16", "mysql8.0", "aurora-mysql3.07").
        family: string

        // description is the parameter group description.
        description: string | *""

        // parameters is a map of parameter name → value.
        // Unspecified parameters retain engine defaults.
        // To reset a parameter to default, remove it from this map.
        parameters: [string]: string

        // tags applied to the parameter group.
        tags: [string]: string
    }

    outputs?: {
        groupName:  string
        arn:        string
        family:     string
        type:       string
    }
}
```

### Key Design Decisions

- **Unified `type` field**: One driver handles both DB parameter groups (for RDS
  instances) and DB cluster parameter groups (for Aurora clusters). The API calls
  differ but the resource model is identical.

- **`parameters` as `map[string]string`**: AWS parameters have string values. The
  driver sends only user-specified parameters; all others remain at engine defaults.

- **Reset-to-default semantics**: Removing a parameter from the map triggers a
  `ResetDBParameterGroup` / `ResetDBClusterParameterGroup` call for that specific
  parameter, restoring the engine default.

---

## Step 2 — AWS Client Factory

Uses the shared `NewRDSClient` from `internal/infra/awsclient/client.go`.

---

## Step 3 — Driver Types

**File**: `internal/drivers/dbparametergroup/types.go`

```go
package dbparametergroup

import "github.com/praxiscloud/praxis/pkg/types"

const ServiceName = "DBParameterGroup"

type DBParameterGroupSpec struct {
    Account     string            `json:"account,omitempty"`
    Region      string            `json:"region"`
    GroupName   string            `json:"groupName"`
    Type        string            `json:"type"`
    Family      string            `json:"family"`
    Description string            `json:"description,omitempty"`
    Parameters  map[string]string `json:"parameters,omitempty"`
    Tags        map[string]string `json:"tags,omitempty"`
}

type DBParameterGroupOutputs struct {
    GroupName string `json:"groupName"`
    ARN       string `json:"arn"`
    Family    string `json:"family"`
    Type      string `json:"type"`
}

type ObservedState struct {
    GroupName   string            `json:"groupName"`
    ARN         string            `json:"arn"`
    Family      string            `json:"family"`
    Type        string            `json:"type"`
    Description string            `json:"description"`
    Parameters  map[string]string `json:"parameters"`
    Tags        map[string]string `json:"tags"`
}

type DBParameterGroupState struct {
    Desired            DBParameterGroupSpec    `json:"desired"`
    Observed           ObservedState           `json:"observed"`
    Outputs            DBParameterGroupOutputs `json:"outputs"`
    Status             types.ResourceStatus    `json:"status"`
    Mode               types.Mode              `json:"mode"`
    Error              string                  `json:"error,omitempty"`
    Generation         int64                   `json:"generation"`
    LastReconcile      string                  `json:"lastReconcile,omitempty"`
    ReconcileScheduled bool                    `json:"reconcileScheduled"`
}
```

### Observed.Parameters

Only stores user-modified parameters, not the full list of engine defaults.
AWS's `DescribeDBParameters` / `DescribeDBClusterParameters` can return hundreds
of parameters; filtering to `Source=user` keeps state manageable.

---

## Step 4 — AWS API Abstraction Layer

**File**: `internal/drivers/dbparametergroup/aws.go`

### DBParameterGroupAPI Interface

```go
type DBParameterGroupAPI interface {
    // CreateParameterGroup creates a DB or Cluster parameter group.
    CreateParameterGroup(ctx context.Context, spec DBParameterGroupSpec) (string, error)

    // DescribeParameterGroup returns the observed state.
    DescribeParameterGroup(ctx context.Context, groupName string, pgType string) (ObservedState, error)

    // ModifyParameters applies parameter changes.
    ModifyParameters(ctx context.Context, groupName string, pgType string, params map[string]string) error

    // ResetParameters resets specified parameters to engine defaults.
    ResetParameters(ctx context.Context, groupName string, pgType string, paramNames []string) error

    // DeleteParameterGroup deletes the parameter group.
    DeleteParameterGroup(ctx context.Context, groupName string, pgType string) error

    // GetUserParameters returns only user-modified parameters (Source=user).
    GetUserParameters(ctx context.Context, groupName string, pgType string) (map[string]string, error)

    // UpdateTags replaces all tags on the parameter group (by ARN).
    UpdateTags(ctx context.Context, arn string, tags map[string]string) error

    // ListTags returns all tags on the parameter group.
    ListTags(ctx context.Context, arn string) (map[string]string, error)
}
```

### Key Implementation Details

#### Type-Conditional API Calls

The implementation dispatches to the correct AWS API based on `pgType`:

```go
func (r *realDBParameterGroupAPI) CreateParameterGroup(ctx context.Context, spec DBParameterGroupSpec) (string, error) {
    switch spec.Type {
    case "db":
        input := &rds.CreateDBParameterGroupInput{
            DBParameterGroupName:   aws.String(spec.GroupName),
            DBParameterGroupFamily: aws.String(spec.Family),
            Description:            aws.String(spec.Description),
        }
        if len(spec.Tags) > 0 {
            input.Tags = toRDSTags(spec.Tags)
        }
        out, err := r.client.CreateDBParameterGroup(ctx, input)
        if err != nil {
            return "", err
        }
        return aws.ToString(out.DBParameterGroup.DBParameterGroupArn), nil

    case "cluster":
        input := &rds.CreateDBClusterParameterGroupInput{
            DBClusterParameterGroupName: aws.String(spec.GroupName),
            DBParameterGroupFamily:      aws.String(spec.Family),
            Description:                 aws.String(spec.Description),
        }
        if len(spec.Tags) > 0 {
            input.Tags = toRDSTags(spec.Tags)
        }
        out, err := r.client.CreateDBClusterParameterGroup(ctx, input)
        if err != nil {
            return "", err
        }
        return aws.ToString(out.DBClusterParameterGroup.DBClusterParameterGroupArn), nil
    }
    return "", fmt.Errorf("unknown parameter group type: %s", spec.Type)
}
```

#### Paginated Parameter Listing

`DescribeDBParameters` / `DescribeDBClusterParameters` is paginated. The
`GetUserParameters` method iterates all pages and filters for `Source == "user"`:

```go
func (r *realDBParameterGroupAPI) GetUserParameters(ctx context.Context, groupName string, pgType string) (map[string]string, error) {
    params := make(map[string]string)
    var marker *string

    for {
        switch pgType {
        case "db":
            out, err := r.client.DescribeDBParameters(ctx, &rds.DescribeDBParametersInput{
                DBParameterGroupName: aws.String(groupName),
                Source:               aws.String("user"),
                Marker:               marker,
            })
            if err != nil {
                return nil, err
            }
            for _, p := range out.Parameters {
                if p.ParameterName != nil && p.ParameterValue != nil {
                    params[aws.ToString(p.ParameterName)] = aws.ToString(p.ParameterValue)
                }
            }
            marker = out.Marker
            if marker == nil {
                return params, nil
            }

        case "cluster":
            out, err := r.client.DescribeDBClusterParameters(ctx, &rds.DescribeDBClusterParametersInput{
                DBClusterParameterGroupName: aws.String(groupName),
                Source:                      aws.String("user"),
                Marker:                      marker,
            })
            if err != nil {
                return nil, err
            }
            for _, p := range out.Parameters {
                if p.ParameterName != nil && p.ParameterValue != nil {
                    params[aws.ToString(p.ParameterName)] = aws.ToString(p.ParameterValue)
                }
            }
            marker = out.Marker
            if marker == nil {
                return params, nil
            }
        }
    }
}
```

#### `ModifyParameters` — Batching

AWS limits `ModifyDBParameterGroup` / `ModifyDBClusterParameterGroup` to 20
parameters per call. The implementation batches parameters automatically:

```go
func (r *realDBParameterGroupAPI) ModifyParameters(ctx context.Context, groupName string, pgType string, params map[string]string) error {
    if len(params) == 0 {
        return nil
    }

    rdsParams := make([]rdstypes.Parameter, 0, len(params))
    for k, v := range params {
        rdsParams = append(rdsParams, rdstypes.Parameter{
            ParameterName:  aws.String(k),
            ParameterValue: aws.String(v),
            ApplyMethod:    rdstypes.ApplyMethodImmediate,
        })
    }

    // Batch into groups of 20
    const batchSize = 20
    for i := 0; i < len(rdsParams); i += batchSize {
        end := i + batchSize
        if end > len(rdsParams) {
            end = len(rdsParams)
        }
        batch := rdsParams[i:end]

        switch pgType {
        case "db":
            if _, err := r.client.ModifyDBParameterGroup(ctx, &rds.ModifyDBParameterGroupInput{
                DBParameterGroupName: aws.String(groupName),
                Parameters:           batch,
            }); err != nil {
                return err
            }
        case "cluster":
            if _, err := r.client.ModifyDBClusterParameterGroup(ctx, &rds.ModifyDBClusterParameterGroupInput{
                DBClusterParameterGroupName: aws.String(groupName),
                Parameters:                  batch,
            }); err != nil {
                return err
            }
        }
    }
    return nil
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
        return apiErr.ErrorCode() == "DBParameterGroupNotFound" ||
               apiErr.ErrorCode() == "DBClusterParameterGroupNotFound"
    }
    return false
}

func IsAlreadyExists(err error) bool {
    if err == nil {
        return false
    }
    var apiErr smithy.APIError
    if errors.As(err, &apiErr) {
        return apiErr.ErrorCode() == "DBParameterGroupAlreadyExistsFault"
    }
    return false
}

func IsInUse(err error) bool {
    if err == nil {
        return false
    }
    var apiErr smithy.APIError
    if errors.As(err, &apiErr) {
        return apiErr.ErrorCode() == "InvalidDBParameterGroupStateFault"
    }
    return false
}
```

---

## Step 5 — Drift Detection

**File**: `internal/drivers/dbparametergroup/drift.go`

### Core Functions

**`HasDrift(desired DBParameterGroupSpec, observed ObservedState) bool`**

```go
func HasDrift(desired DBParameterGroupSpec, observed ObservedState) bool {
    // Check parameters: desired params must match observed user-modified params
    if !parametersMatch(desired.Parameters, observed.Parameters) {
        return true
    }
    return !tagsMatch(desired.Tags, observed.Tags)
}
```

**Parameter Drift Logic**

Parameters have three-way comparison:
1. **Added**: Key in desired but not in observed → drift (need to set).
2. **Changed**: Key in both, values differ → drift (need to modify).
3. **Removed**: Key in observed but not in desired → drift (need to reset to default).

```go
func parametersMatch(desired, observed map[string]string) bool {
    // Check for added or changed parameters
    for k, dv := range desired {
        if ov, ok := observed[k]; !ok || ov != dv {
            return false
        }
    }
    // Check for removed parameters (present in observed but not desired)
    for k := range observed {
        if _, ok := desired[k]; !ok {
            return false
        }
    }
    return true
}
```

**`ComputeFieldDiffs(desired DBParameterGroupSpec, observed ObservedState) []FieldDiffEntry`**

Reports:
- Immutable fields: `family`, `description` — "(immutable, ignored)".
- Added parameters: `"parameter.<name>"` with desired value, observed "(default)".
- Changed parameters: `"parameter.<name>"` with both values.
- Removed parameters: `"parameter.<name>"` with observed value, desired "(default)".
- Tags: standard tag diff.

---

## Step 6 — Driver Implementation

**File**: `internal/drivers/dbparametergroup/driver.go`

### Provision Handler

1. **Input validation**: `groupName`, `type`, `family` required. `type` must be
   `"db"` or `"cluster"`. Returns `TerminalError(400)`.

2. **Load state, set Provisioning**.

3. **Re-provision path**: If `state.Outputs.GroupName` is non-empty, describes the
   group. If deleted externally (404), falls through to creation.

4. **Create**: Calls `api.CreateParameterGroup`. On `IsAlreadyExists` → `TerminalError(409)`.

5. **Set parameters**: If `spec.Parameters` is non-empty, calls `api.ModifyParameters`.

6. **Converge parameters** (re-provision path):
   - **Compute diff**: Compare desired vs observed user parameters.
   - **Apply additions/changes**: `api.ModifyParameters` for new/changed params.
   - **Reset removals**: `api.ResetParameters` for params no longer in desired.

7. **Update tags**: `api.UpdateTags`.

8. **Describe final state**: `api.DescribeParameterGroup` + `api.GetUserParameters`.

9. **Set Ready, save state, schedule reconcile**.

### Import Handler

1. Describes the parameter group.
2. Gets user-modified parameters via `api.GetUserParameters`.
3. Gets tags via `api.ListTags`.
4. Synthesizes spec from observed state.
5. Sets `ModeObserved`.
6. Schedules reconciliation.

### Delete Handler

1. Sets `Deleting`.
2. Calls `api.DeleteParameterGroup`.
   - `IsNotFound` → silent success.
   - `IsInUse` → `TerminalError(409)` with message about instances/clusters
     still referencing the group.
3. Sets `Deleted`.

### Reconcile Handler

Standard 5-minute reconcile timer. Detects and corrects parameter drift for
Managed mode. Reports-only for Observed mode.

---

## Step 7 — Provider Adapter

**File**: `internal/core/provider/dbparametergroup_adapter.go`

### Methods

**`Scope() KeyScope`** → `KeyScopeRegion`

**`Kind() string`** → `"DBParameterGroup"`

**`ServiceName() string`** → `"DBParameterGroup"`

**`BuildKey(resourceDoc json.RawMessage) (string, error)`**:
Returns `region~metadata.name`.

**`BuildImportKey(region, resourceID string) (string, error)`**:
Returns `region~resourceID`.

**`Plan(ctx, key, account, desiredSpec) (DiffOperation, []FieldDiff, error)`**:
Calls `api.DescribeParameterGroup`. Not found → `OpCreate`. Found → compare
parameters + tags. No diffs → `OpNoOp`. Diffs → `OpUpdate`.

---

## Step 8 — Registry Integration

Add `NewDBParameterGroupAdapterWithRegistry(accounts)` to `NewRegistry()`.

---

## Step 9 — Database Driver Pack Entry Point

Bind `DBParameterGroupDriver` in `cmd/praxis-database/main.go`.

---

## Step 10 — Docker Compose & Justfile

Uses the same `praxis-database` service on port 9086.

### Justfile Targets

| Target | Command |
|---|---|
| `test-dbparametergroup` | `go test ./internal/drivers/dbparametergroup/... -v -count=1 -race` |
| `test-dbparametergroup-integration` | `go test ./tests/integration/ -run TestDBParameterGroup -v -count=1 -tags=integration -timeout=10m` |

---

## Step 11 — Unit Tests

### `internal/drivers/dbparametergroup/drift_test.go`

| Test | Purpose |
|---|---|
| `TestHasDrift_NoDrift` | Matching parameters and tags → no drift |
| `TestHasDrift_ParameterAdded` | New parameter in desired → drift |
| `TestHasDrift_ParameterChanged` | Different value → drift |
| `TestHasDrift_ParameterRemoved` | Param in observed but not desired → drift |
| `TestHasDrift_TagDrift` | Tag change → drift |
| `TestHasDrift_EmptyParameters` | Both empty → no drift |
| `TestComputeFieldDiffs_ImmutableFamily` | Reports family as "(immutable, ignored)" |
| `TestComputeFieldDiffs_AddedParam` | Reports param as added with desired value |
| `TestComputeFieldDiffs_RemovedParam` | Reports param as removed with "(default)" target |

### `internal/drivers/dbparametergroup/aws_test.go`

| Test | Purpose |
|---|---|
| `TestIsNotFound_DBParameterGroup` | DB param group not-found → true |
| `TestIsNotFound_ClusterParameterGroup` | Cluster param group not-found → true |
| `TestIsAlreadyExists_True` | Duplicate name → true |
| `TestIsInUse_True` | In-use error → true |

### `internal/drivers/dbparametergroup/driver_test.go`

| Test | Purpose |
|---|---|
| `TestSpecFromObserved_RoundTrip` | Observed → spec preserves fields |
| `TestServiceName` | Returns "DBParameterGroup" |

### `internal/core/provider/dbparametergroup_adapter_test.go`

| Test | Purpose |
|---|---|
| `TestDBParameterGroupAdapter_BuildKey` | Returns `region~groupName` |
| `TestDBParameterGroupAdapter_BuildImportKey` | Returns `region~groupName` |
| `TestDBParameterGroupAdapter_Kind` | Returns "DBParameterGroup" |
| `TestDBParameterGroupAdapter_Scope` | Returns `KeyScopeRegion` |

---

## Step 12 — Integration Tests

**File**: `tests/integration/dbparametergroup_driver_test.go`

### Test Cases

| Test | Description |
|---|---|
| `TestDBParameterGroup_Provision_DBType` | Creates a DB parameter group with 2 custom params. Verifies in LocalStack. |
| `TestDBParameterGroup_Provision_ClusterType` | Creates a cluster parameter group. Verifies creation. |
| `TestDBParameterGroup_Provision_Idempotent` | Provisions same spec twice. Same ARN returned. |
| `TestDBParameterGroup_Provision_AddParameter` | Re-provisions with an additional parameter. Verifies it's set. |
| `TestDBParameterGroup_Provision_RemoveParameter` | Re-provisions without a param. Verifies reset to default. |
| `TestDBParameterGroup_Provision_ChangeParameter` | Re-provisions with changed parameter value. |
| `TestDBParameterGroup_Import_Existing` | Creates group via API, imports via driver. Verifies Observed mode. |
| `TestDBParameterGroup_Delete_Removes` | Provisions, deletes. Verifies removal. |
| `TestDBParameterGroup_Delete_InUse` | Creates group assigned to instance, attempts delete. Verifies 409. |
| `TestDBParameterGroup_Reconcile_DetectsDrift` | Modifies param via API, reconciles. Verifies correction. |

---

## Parameter-Group-Specific Design Decisions

### 1. Unified DB + Cluster Parameter Group Driver

Both resource types share identical structure: a name, family, description,
parameters, and tags. The only difference is the AWS API call prefix
(`DBParameterGroup` vs `DBClusterParameterGroup`). Using a single driver with
a `type` discriminator avoids code duplication.

### 2. Reset-to-Default Semantics

Removing a parameter from `spec.parameters` triggers a reset to the engine default
via `ResetDBParameterGroup` / `ResetDBClusterParameterGroup`. This provides clean
"remove parameter" semantics without requiring the user to know engine default
values.

### 3. Apply Method: Immediate

All parameter modifications use `ApplyMethod: "immediate"`. Parameters with
`ApplyType: "static"` require a reboot of the associated DB instance/cluster to
take effect, but the parameter group driver does not manage that lifecycle. The
RDS Instance or Aurora Cluster driver (or the user) must handle reboots.

### 4. Only User-Modified Parameters in State

The driver stores only `Source=user` parameters in `ObservedState.Parameters`.
A typical parameter group has 300+ parameters at engine defaults. Storing only
user-modified parameters keeps state manageable and drift detection focused.

### 5. Parameter Batching

AWS limits `ModifyDBParameterGroup` to 20 parameters per call. The implementation
batches automatically. This is transparent to the driver logic.

### 6. No Wait Required

Unlike DB instances and clusters, parameter groups are available immediately after
creation. No `WaitUntilAvailable` method is needed.

---

## Design Decisions (Resolved)

1. **Should DB and Cluster parameter groups be separate drivers?**
   No. They share identical structure and nearly identical API patterns.
   A `type` discriminator handles the difference.

2. **Should the driver track all parameters or only user-modified ones?**
   Only user-modified. Engine defaults number in the hundreds and change with
   engine versions. Storing only `Source=user` parameters keeps drift detection
   clean and state compact.

3. **Should the driver support `pending-reboot` ApplyMethod?**
   Not in v1. The driver always uses `immediate`. Static parameters that require
   a reboot will show as "pending-reboot" status on the instance — this is the
   responsibility of the instance/cluster driver.

4. **Should the driver support copying parameter groups?**
   Not in v1. `CopyDBParameterGroup` / `CopyDBClusterParameterGroup` is a
   convenience feature. Users can achieve the same by defining a new group with
   the same parameters.

---

## Checklist

- [x] **Schema**: `schemas/aws/rds/db_parameter_group.cue` created
- [x] **Types**: `internal/drivers/dbparametergroup/types.go` created
- [x] **AWS API**: `internal/drivers/dbparametergroup/aws.go` created
- [x] **Drift**: `internal/drivers/dbparametergroup/drift.go` created
- [x] **Driver**: `internal/drivers/dbparametergroup/driver.go` created with all 6 handlers
- [x] **Adapter**: `internal/core/provider/dbparametergroup_adapter.go` created
- [x] **Registry**: `internal/core/provider/registry.go` updated
- [x] **Entry point**: `cmd/praxis-database/main.go` updated with binding
- [x] **Unit tests (drift)**: `internal/drivers/dbparametergroup/drift_test.go`
- [x] **Unit tests (aws helpers)**: `internal/drivers/dbparametergroup/aws_test.go`
- [x] **Unit tests (driver)**: `internal/drivers/dbparametergroup/driver_test.go`
- [x] **Unit tests (adapter)**: `internal/core/provider/dbparametergroup_adapter_test.go`
- [x] **Integration tests**: `tests/integration/dbparametergroup_driver_test.go`
- [x] **Parameter batching**: 20-parameter batch limit implemented
- [x] **Reset-to-default**: Removed params reset correctly
- [x] **DB + Cluster type dispatch**: Both paths tested
- [x] **Import default mode**: `ModeObserved`
- [x] **Delete mode guard**: Delete handler blocks for ModeObserved (409)
- [x] **Build passes**: `go build ./...` succeeds
- [x] **Unit tests pass**: `go test ./internal/drivers/dbparametergroup/... -race`
- [x] **Integration tests pass**: `go test ./tests/integration/ -run TestDBParameterGroup -tags=integration`
