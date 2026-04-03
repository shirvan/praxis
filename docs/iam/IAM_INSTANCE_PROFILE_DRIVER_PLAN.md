# IAM Instance Profile Driver — Implementation Spec

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
12. [Step 9 — Identity Driver Pack Entry Point](#step-9--identity-driver-pack-entry-point)
13. [Step 10 — Docker Compose & Justfile](#step-10--docker-compose--justfile)
14. [Step 11 — Unit Tests](#step-11--unit-tests)
15. [Step 12 — Integration Tests](#step-12--integration-tests)
16. [Instance-Profile-Specific Design Decisions](#instance-profile-specific-design-decisions)
17. [Design Decisions (Resolved)](#design-decisions-resolved)
18. [Checklist](#checklist)

---

## 1. Overview & Scope

An IAM Instance Profile is a container for an IAM role that can be attached to an
EC2 instance. It is the mechanism by which EC2 instances assume IAM roles. The
relationship is:

```text
EC2 Instance ─uses→ Instance Profile ─contains→ IAM Role
```

While AWS technically supports up to one role per instance profile, the instance
profile itself is the entity referenced in EC2 launch configurations /
`IamInstanceProfile` parameters.

**In scope**:

- Create / delete instance profiles
- Associate / disassociate a single IAM role
- Tags management
- Drift detection on role association and tags

**Out of scope**:

- Managing the IAM role itself — that's the IAM Role driver's concern
- EC2 instance association — that's the EC2 driver's concern

### Driver Contract

| Handler | Context | Purpose |
|---|---|---|
| `Provision` | `ObjectContext` (exclusive) | Create or converge an instance profile |
| `Import` | `ObjectContext` (exclusive) | Adopt an existing instance profile |
| `Delete` | `ObjectContext` (exclusive) | Remove an instance profile (disassociate role first) |
| `Reconcile` | `ObjectContext` (exclusive) | Detect/correct drift |
| `GetStatus` | `ObjectSharedContext` (shared) | Return current status |
| `GetOutputs` | `ObjectSharedContext` (shared) | Return instance profile outputs |

### Mutable vs Immutable Attributes

| Attribute | Mutability | Notes |
|---|---|---|
| `instanceProfileName` | Immutable | Part of the Virtual Object key |
| `path` | Immutable | Cannot be changed after creation |
| `roleName` | Mutable | Changed via `RemoveRoleFromInstanceProfile` + `AddRoleToInstanceProfile` |
| `tags` | Mutable | Updated via `TagInstanceProfile` / `UntagInstanceProfile` |

### Downstream Consumers

```text
${resources.my-profile.outputs.arn}                 → EC2 IamInstanceProfile.Arn
${resources.my-profile.outputs.instanceProfileName} → EC2 IamInstanceProfile.Name
${resources.my-profile.outputs.instanceProfileId}   → Audit references
```

Instance profiles are the **critical bridge** between IAM and EC2. The EC2 driver
references instance profile ARN/name in its spec.

---

## 2. Key Strategy

### Key Scope: `KeyScopeGlobal`

IAM is a global service. Instance profile names are unique within an account.

### BuildKey vs BuildImportKey

- **`BuildKey(resourceDoc)`**: Extracts `metadata.name`. Returns instance profile name.
- **`BuildImportKey(region, resourceID)`**: Returns `resourceID` (instance profile name).

### Eventual Consistency Warning

Instance profiles have a well-known eventual consistency delay after creation. An
EC2 instance launched immediately after creating an instance profile may fail with
`InvalidParameterValue: Value (arn:...) for parameter iamInstanceProfile.arn is
invalid`. The **EC2 driver** should handle retries for this — it is not the instance
profile driver's concern. However, the driver should document this behavior.

---

## 3. File Inventory

```text
✦ schemas/aws/iam/instance_profile.cue                         — CUE schema
✦ internal/drivers/iaminstanceprofile/types.go                  — Types
✦ internal/drivers/iaminstanceprofile/aws.go                    — AWS API layer
✦ internal/drivers/iaminstanceprofile/drift.go                  — Drift detection
✦ internal/drivers/iaminstanceprofile/driver.go                 — Driver Virtual Object
✦ internal/drivers/iaminstanceprofile/driver_test.go            — Unit tests (driver)
✦ internal/drivers/iaminstanceprofile/aws_test.go               — Unit tests (aws)
✦ internal/drivers/iaminstanceprofile/drift_test.go             — Unit tests (drift)
✦ internal/core/provider/iaminstanceprofile_adapter.go          — Adapter
✦ internal/core/provider/iaminstanceprofile_adapter_test.go     — Adapter tests
✦ tests/integration/iaminstanceprofile_driver_test.go           — Integration tests
✎ cmd/praxis-identity/main.go                                       — Add driver .Bind()
✎ internal/core/provider/registry.go                            — Add to NewRegistry()
```

---

## Step 1 — CUE Schema

**File**: `schemas/aws/iam/instance_profile.cue`

```cue
package iam

#IAMInstanceProfile: {
    apiVersion: "praxis.io/v1"
    kind:       "IAMInstanceProfile"

    metadata: {
        // name is used as the instance profile name in AWS.
        name: string & =~"^[a-zA-Z0-9+=,.@_-]{1,128}$"
        labels: [string]: string
    }

    spec: {
        // path is the IAM path prefix. Immutable after creation.
        path: string | *"/"

        // roleName is the IAM role to associate.
        // At most one role can be associated with an instance profile.
        roleName: string

        // tags for the instance profile.
        tags: [string]: string
    }

    outputs?: {
        arn:                 string
        instanceProfileId:   string
        instanceProfileName: string
    }
}
```

### Key Design Decisions

- **`roleName` is required**: An instance profile without a role is useless. Making
  it required enforces meaningful configurations.

- **Only one role**: AWS allows at most one role per instance profile. The schema
  takes a single `roleName` string, not a list.

---

## Step 2 — AWS Client Factory

Uses same IAM client as other IAM drivers. No changes needed.

---

## Step 3 — Driver Types

**File**: `internal/drivers/iaminstanceprofile/types.go`

```go
package iaminstanceprofile

import "github.com/shirvan/praxis/pkg/types"

const ServiceName = "IAMInstanceProfile"

type IAMInstanceProfileSpec struct {
    Account              string            `json:"account,omitempty"`
    Path                 string            `json:"path"`
    InstanceProfileName  string            `json:"instanceProfileName"`
    RoleName             string            `json:"roleName"`
    Tags                 map[string]string `json:"tags,omitempty"`
}

type IAMInstanceProfileOutputs struct {
    Arn                 string `json:"arn"`
    InstanceProfileId   string `json:"instanceProfileId"`
    InstanceProfileName string `json:"instanceProfileName"`
}

type ObservedState struct {
    Arn                 string            `json:"arn"`
    InstanceProfileId   string            `json:"instanceProfileId"`
    InstanceProfileName string            `json:"instanceProfileName"`
    Path                string            `json:"path"`
    RoleName            string            `json:"roleName"`
    Tags                map[string]string `json:"tags"`
    CreateDate          string            `json:"createDate"`
}

type IAMInstanceProfileState struct {
    Desired            IAMInstanceProfileSpec    `json:"desired"`
    Observed           ObservedState             `json:"observed"`
    Outputs            IAMInstanceProfileOutputs `json:"outputs"`
    Status             types.ResourceStatus      `json:"status"`
    Mode               types.Mode                `json:"mode"`
    Error              string                    `json:"error,omitempty"`
    Generation         int64                     `json:"generation"`
    LastReconcile      string                    `json:"lastReconcile,omitempty"`
    ReconcileScheduled bool                      `json:"reconcileScheduled"`
}
```

---

## Step 4 — AWS API Abstraction Layer

**File**: `internal/drivers/iaminstanceprofile/aws.go`

### IAMInstanceProfileAPI Interface

```go
type IAMInstanceProfileAPI interface {
    // CreateInstanceProfile creates a new instance profile.
    CreateInstanceProfile(ctx context.Context, spec IAMInstanceProfileSpec) (arn, ipId string, err error)

    // DescribeInstanceProfile returns the observed state.
    DescribeInstanceProfile(ctx context.Context, name string) (ObservedState, error)

    // DeleteInstanceProfile deletes an instance profile.
    DeleteInstanceProfile(ctx context.Context, name string) error

    // AddRoleToInstanceProfile associates a role.
    AddRoleToInstanceProfile(ctx context.Context, name, roleName string) error

    // RemoveRoleFromInstanceProfile disassociates a role.
    RemoveRoleFromInstanceProfile(ctx context.Context, name, roleName string) error

    // TagInstanceProfile sets tags.
    TagInstanceProfile(ctx context.Context, name string, tags map[string]string) error

    // UntagInstanceProfile removes tags by key.
    UntagInstanceProfile(ctx context.Context, name string, keys []string) error
}
```

### realIAMInstanceProfileAPI Implementation

```go
type realIAMInstanceProfileAPI struct {
    client  *iam.Client
    limiter *ratelimit.Limiter
}

func NewIAMInstanceProfileAPI(client *iam.Client) IAMInstanceProfileAPI {
    return &realIAMInstanceProfileAPI{
        client:  client,
        limiter: ratelimit.New("iam", 15, 8),
    }
}
```

### Key Implementation Details

#### `CreateInstanceProfile`

```go
func (r *realIAMInstanceProfileAPI) CreateInstanceProfile(
    ctx context.Context, spec IAMInstanceProfileSpec,
) (string, string, error) {
    input := &iam.CreateInstanceProfileInput{
        InstanceProfileName: aws.String(spec.InstanceProfileName),
        Path:                aws.String(spec.Path),
    }

    // Add tags if present
    if len(spec.Tags) > 0 {
        for k, v := range spec.Tags {
            input.Tags = append(input.Tags, iamtypes.Tag{
                Key:   aws.String(k),
                Value: aws.String(v),
            })
        }
    }

    out, err := r.client.CreateInstanceProfile(ctx, input)
    if err != nil {
        return "", "", err
    }
    return aws.ToString(out.InstanceProfile.Arn),
           aws.ToString(out.InstanceProfile.InstanceProfileId),
           nil
}
```

#### `DescribeInstanceProfile`

```go
func (r *realIAMInstanceProfileAPI) DescribeInstanceProfile(
    ctx context.Context, name string,
) (ObservedState, error) {
    out, err := r.client.GetInstanceProfile(ctx, &iam.GetInstanceProfileInput{
        InstanceProfileName: aws.String(name),
    })
    if err != nil {
        return ObservedState{}, err
    }

    ip := out.InstanceProfile
    obs := ObservedState{
        Arn:                 aws.ToString(ip.Arn),
        InstanceProfileId:   aws.ToString(ip.InstanceProfileId),
        InstanceProfileName: aws.ToString(ip.InstanceProfileName),
        Path:                aws.ToString(ip.Path),
        Tags:                make(map[string]string),
    }

    if ip.CreateDate != nil {
        obs.CreateDate = ip.CreateDate.Format(time.RFC3339)
    }

    // Extract role name (at most one)
    if len(ip.Roles) > 0 {
        obs.RoleName = aws.ToString(ip.Roles[0].RoleName)
    }

    // Extract tags
    for _, tag := range ip.Tags {
        obs.Tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
    }

    return obs, nil
}
```

#### `AddRoleToInstanceProfile` / `RemoveRoleFromInstanceProfile`

```go
func (r *realIAMInstanceProfileAPI) AddRoleToInstanceProfile(
    ctx context.Context, name, roleName string,
) error {
    _, err := r.client.AddRoleToInstanceProfile(ctx, &iam.AddRoleToInstanceProfileInput{
        InstanceProfileName: aws.String(name),
        RoleName:            aws.String(roleName),
    })
    return err
}

func (r *realIAMInstanceProfileAPI) RemoveRoleFromInstanceProfile(
    ctx context.Context, name, roleName string,
) error {
    _, err := r.client.RemoveRoleFromInstanceProfile(ctx, &iam.RemoveRoleFromInstanceProfileInput{
        InstanceProfileName: aws.String(name),
        RoleName:            aws.String(roleName),
    })
    return err
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
        return apiErr.ErrorCode() == "NoSuchEntity"
    }
    return strings.Contains(err.Error(), "NoSuchEntity")
}

func IsAlreadyExists(err error) bool {
    if err == nil {
        return false
    }
    var apiErr smithy.APIError
    if errors.As(err, &apiErr) {
        return apiErr.ErrorCode() == "EntityAlreadyExists"
    }
    return strings.Contains(err.Error(), "EntityAlreadyExists")
}

func IsDeleteConflict(err error) bool {
    if err == nil {
        return false
    }
    var apiErr smithy.APIError
    if errors.As(err, &apiErr) {
        return apiErr.ErrorCode() == "DeleteConflict"
    }
    return strings.Contains(err.Error(), "DeleteConflict")
}

func IsLimitExceeded(err error) bool {
    if err == nil {
        return false
    }
    var apiErr smithy.APIError
    if errors.As(err, &apiErr) {
        return apiErr.ErrorCode() == "LimitExceeded"
    }
    return strings.Contains(err.Error(), "LimitExceeded")
}
```

---

## Step 5 — Drift Detection

**File**: `internal/drivers/iaminstanceprofile/drift.go`

### Core Functions

**`HasDrift(desired IAMInstanceProfileSpec, observed ObservedState) bool`**

```go
func HasDrift(desired IAMInstanceProfileSpec, observed ObservedState) bool {
    if desired.RoleName != observed.RoleName {
        return true
    }
    return !tagsEqual(desired.Tags, observed.Tags)
}
```

> **Note**: Path is immutable and not checked during drift detection.

**`ComputeFieldDiffs`**: Reports diffs for:

- `roleName`: value comparison
- `tags`: uses standard `computeTagDiffs` pattern

### Simplicity

Instance profiles have minimal drift surface — just roles and tags. This makes the
drift detector simpler than most other IAM drivers.

---

## Step 6 — Driver Implementation

**File**: `internal/drivers/iaminstanceprofile/driver.go`

### Service Registration

```go
const ServiceName = "IAMInstanceProfile"
```

### Constructor Pattern

```go
func NewIAMInstanceProfileDriver(auth authservice.AuthClient) *IAMInstanceProfileDriver
func NewIAMInstanceProfileDriverWithFactory(
    auth authservice.AuthClient,
    factory func(aws.Config) IAMInstanceProfileAPI,
) *IAMInstanceProfileDriver
```

### Provision Handler

1. **Input validation**: `instanceProfileName` and `roleName` must be non-empty.

2. **Load current state**: Read, set `Provisioning`, increment generation.

3. **Re-provision check**: If `state.Outputs.Arn` is non-empty, describe. If gone,
   fall through to creation.

4. **Create instance profile**: Calls `api.CreateInstanceProfile`. Classifies:
   - `IsAlreadyExists` → `TerminalError(409)`

5. **Add role**: After creation, calls `api.AddRoleToInstanceProfile`.
   - `IsLimitExceeded` → `TerminalError(409)` "instance profile can only have one role"

6. **Converge mutable attributes** (re-provision path):
   - Role: If changed, remove old role → add new role. Order matters: remove first
     since only one role is allowed.
   - Tags: Apply add-before-remove pattern.

7. **Describe final state**: Calls `api.DescribeInstanceProfile`.

8. **Commit state**: Set `Ready`, save state, schedule reconcile.

### Import Handler

1. Describes instance profile by name.
2. Synthesizes `IAMInstanceProfileSpec` from observed state.
3. Sets mode to `ModeObserved`.
4. Schedules reconciliation.

### Delete Handler

1. **Remove role**: If the instance profile has a role, remove it first. AWS does
   not allow deleting an instance profile with an associated role.
   - Use observed state to get current role name (not desired — it may have drifted).
   - `IsNotFound` on role removal → ignore (role may have been deleted).

2. **Delete instance profile**: Calls `api.DeleteInstanceProfile`.
   - `IsDeleteConflict` → describe again, retry role removal.
   - `IsNotFound` → silent success.

3. **EC2 dependency**: If the instance profile is attached to running EC2 instances,
   `DeleteInstanceProfile` will succeed — AWS detaches it automatically. However,
   the EC2 instance may lose its role. The orchestrator should handle dependency
   ordering (delete EC2 first).

### Reconcile Handler

Standard pattern:

1. Describe current state.
2. **Managed + drift**: Converge role and tags.
3. **Observed + drift**: Report only.
4. Re-schedule.

### GetStatus / GetOutputs

Standard shared handlers.

---

## Step 7 — Provider Adapter

**File**: `internal/core/provider/iaminstanceprofile_adapter.go`

### Methods

**`Scope() KeyScope`** → `KeyScopeGlobal`

**`Kind() string`** → `"IAMInstanceProfile"`

**`ServiceName() string`** → `"IAMInstanceProfile"`

**`BuildKey(resourceDoc)`**: Returns `metadata.name` (instance profile name).

**`BuildImportKey(region, resourceID)`**: Returns `resourceID` (instance profile name).

**`Plan(ctx, key, account, desiredSpec)`**: Calls `api.DescribeInstanceProfile(name)`.
Not found → `OpCreate`. Found → compare, return `OpNoOp` or `OpUpdate`.

---

## Step 8 — Registry Integration

Add `NewIAMInstanceProfileAdapterWithRegistry(accounts)` to `NewRegistry()`.

---

## Step 9 — Identity Driver Pack Entry Point

**File**: `cmd/praxis-identity/main.go` (modified)

Add:

```go
.Bind(restate.Reflect(iaminstanceprofile.NewIAMInstanceProfileDriver(cfg.Auth())))
```

---

## Step 10 — Docker Compose & Justfile

No Docker Compose changes. Add justfile targets:

| Target | Command |
|---|---|
| `test-iaminstanceprofile` | `go test ./internal/drivers/iaminstanceprofile/... -v -count=1 -race` |
| `test-iaminstanceprofile-integration` | `go test ./tests/integration/ -run TestIAMInstanceProfile -v -count=1 -tags=integration -timeout=5m` |

---

## Step 11 — Unit Tests

### `internal/drivers/iaminstanceprofile/drift_test.go`

| Test | Purpose |
|---|---|
| `TestHasDrift_NoDrift` | Matching state → no drift |
| `TestHasDrift_RoleChanged` | Different role → drift |
| `TestHasDrift_RoleMissing` | Role removed externally → drift |
| `TestHasDrift_TagAdded` | Extra tag → drift |
| `TestHasDrift_TagRemoved` | Missing tag → drift |
| `TestHasDrift_TagChanged` | Changed tag value → drift |
| `TestComputeFieldDiffs_Role` | Role diff |
| `TestComputeFieldDiffs_Tags` | Tag diffs |

### `internal/drivers/iaminstanceprofile/aws_test.go`

| Test | Purpose |
|---|---|
| `TestIsNotFound_NoSuchEntity` | True for NoSuchEntity |
| `TestIsAlreadyExists_True` | True for EntityAlreadyExists |
| `TestIsDeleteConflict_True` | True for DeleteConflict |
| `TestIsLimitExceeded_True` | True for LimitExceeded |
| `TestIsNotFound_WrappedRestateError` | String fallback |

### `internal/drivers/iaminstanceprofile/driver_test.go`

| Test | Purpose |
|---|---|
| `TestSpecFromObserved_RoundTrip` | Observed → spec round-trip |
| `TestServiceName` | Returns "IAMInstanceProfile" |
| `TestOutputsFromObserved` | Correct output mapping |

### `internal/core/provider/iaminstanceprofile_adapter_test.go`

| Test | Purpose |
|---|---|
| `TestIAMInstanceProfileAdapter_BuildKey` | Returns instance profile name |
| `TestIAMInstanceProfileAdapter_BuildImportKey` | Returns same key |
| `TestIAMInstanceProfileAdapter_Kind` | Returns "IAMInstanceProfile" |
| `TestIAMInstanceProfileAdapter_Scope` | Returns `KeyScopeGlobal` |
| `TestIAMInstanceProfileAdapter_NormalizeOutputs` | Converts struct to map |

---

## Step 12 — Integration Tests

**File**: `tests/integration/iaminstanceprofile_driver_test.go`

| Test | Description |
|---|---|
| `TestIAMInstanceProfileProvision_Creates` | Creates profile, adds role. Verifies via `GetInstanceProfile`. |
| `TestIAMInstanceProfileProvision_Idempotent` | Two provisions, same ARN. |
| `TestIAMInstanceProfileProvision_ChangeRole` | Re-provisions with new role. Verifies role switched. |
| `TestIAMInstanceProfileProvision_UpdateTags` | Re-provisions with new tags. Verifies convergence. |
| `TestIAMInstanceProfileImport_Existing` | Creates via IAM API, imports via driver. Verifies Observed mode. |
| `TestIAMInstanceProfileDelete_RemovesProfile` | Provisions, adds role via driver, deletes. Verifies role removed + profile deleted. |
| `TestIAMInstanceProfileReconcile_DetectsRoleDrift` | Removes role directly, reconcile re-adds it. |
| `TestIAMInstanceProfileReconcile_DetectsTagDrift` | Adds tag directly, reconcile removes it. |
| `TestIAMInstanceProfileGetStatus_ReturnsReady` | Provisions and checks status. |

### Integration Test Prerequisites

The tests need at least one IAM role to exist (for association). Tests should create
their own roles via the IAM API directly (not through the role driver) to avoid
cross-driver dependencies in tests.

---

## Instance-Profile-Specific Design Decisions

### 1. Role is Required

An instance profile without a role serves no purpose. The schema requires `roleName`.
During import, if the instance profile has no associated role, the driver sets
`roleName` to an empty string and the profile is in `ModeObserved` — it can be
observed but not actively managed until a role is associated.

### 2. Role Change: Remove-Then-Add

Unlike most mutable attributes where we apply add-before-remove, role changes must
use remove-then-add because only one role can be associated at a time. The sequence:

1. `RemoveRoleFromInstanceProfile(currentRole)`
2. `AddRoleToInstanceProfile(newRole)`

There is a brief window where the instance profile has no role. Any EC2 instance
using this profile would temporarily lose its role credentials. This is unavoidable
with the current AWS API. Templates should use separate instance profiles if zero-
downtime role rotation is needed.

### 3. Path is Immutable

Unlike users and roles, instance profile paths cannot be changed after creation.
There is no `UpdateInstanceProfile` API. If a path change is needed, the instance
profile must be deleted and recreated (which requires disassociating from EC2
instances first).

### 4. Eventual Consistency

There is a well-documented delay between creating an instance profile and being able
to use it in EC2 `RunInstances`. This is NOT the instance profile driver's problem —
the EC2 driver should retry with backoff when it encounters
`InvalidParameterValue` for instance profile ARNs.

### 5. Import Defaults to ModeObserved

Instance profiles are often created automatically (e.g., by CloudFormation, CDK, or
the AWS console when creating EC2 instances with roles). Importing defaults to
observed mode to avoid accidental modifications.

### 6. Delete Does Not Check EC2 Usage

AWS allows deleting an instance profile even if EC2 instances reference it. The
instances will lose their role. The orchestrator is responsible for dependency
ordering. The driver does not check for EC2 references before deletion.

---

## Design Decisions (Resolved)

1. **Should roleName be optional?**
   In the schema, it's required. During import of a profile with no role, the
   observed state has `roleName: ""` and mode is Observed. This ensures that
   managed profiles always have a role (enforced at CUE validation time).

2. **Should the driver wait for eventual consistency?**
   No. The instance profile driver's job is to create the profile and associate the
   role. The EC2 driver handles the eventual consistency delay when using the profile.

3. **Should path changes trigger recreate?**
   No. Path changes are rejected as a terminal error during re-provision: "path is
   immutable; delete and recreate the instance profile to change the path." This
   follows the pattern of other immutable attributes.

4. **Should the driver manage multiple roles?**
   No. AWS APIs technically support one role per instance profile, and the console
   enforces this. The SDK allows the `Roles` field to be a list for historical
   reasons, but only one role is supported.

---

## Checklist

- [x] **Schema**: `schemas/aws/iam/instance_profile.cue` created
- [x] **Types**: `internal/drivers/iaminstanceprofile/types.go` created
- [x] **AWS API**: `internal/drivers/iaminstanceprofile/aws.go` created
- [x] **Drift**: `internal/drivers/iaminstanceprofile/drift.go` created
- [x] **Driver**: `internal/drivers/iaminstanceprofile/driver.go` created with all 6 handlers
- [x] **Adapter**: `internal/core/provider/iaminstanceprofile_adapter.go` created
- [x] **Registry**: `internal/core/provider/registry.go` updated
- [x] **Entry point**: IAMInstanceProfile driver bound in `cmd/praxis-identity/main.go`
- [x] **Unit tests (drift)**: `internal/drivers/iaminstanceprofile/drift_test.go`
- [x] **Unit tests (aws)**: `internal/drivers/iaminstanceprofile/aws_test.go`
- [x] **Unit tests (driver)**: `internal/drivers/iaminstanceprofile/driver_test.go`
- [x] **Unit tests (adapter)**: `internal/core/provider/iaminstanceprofile_adapter_test.go`
- [x] **Integration tests**: `tests/integration/iaminstanceprofile_driver_test.go`
- [x] **Role association**: Single role, remove-then-add for changes
- [x] **Path immutability**: Terminal error on path change
- [x] **Tags**: Full tag lifecycle management
- [x] **Import default mode**: `ModeObserved`
- [x] **Delete mode guard**: Delete handler blocks for ModeObserved (409)
- [x] **Pre-deletion cleanup**: Remove associated role before deleting profile
- [x] **Build passes**: `go build ./...` succeeds
- [x] **Unit tests pass**: `go test ./internal/drivers/iaminstanceprofile/... -race`
- [x] **Integration tests pass**: `go test ./tests/integration/ -run TestIAMInstanceProfile -tags=integration`
