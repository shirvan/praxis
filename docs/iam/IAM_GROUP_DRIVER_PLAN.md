# IAM Group Driver — Implementation Plan

> ✅ Implemented
> Target: A Restate Virtual Object driver that manages IAM Groups, providing full
> lifecycle management including creation, import, deletion, drift detection, and
> drift correction for group properties, inline policies, managed policy attachments,
> and tags (where supported).
>
> Key scope: `KeyScopeGlobal` — key format is `groupName`, permanent and immutable
> for the lifetime of the Virtual Object. IAM group names are unique within an AWS
> account.

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
12. [Step 9 — Identity Driver Pack Entry Point](#step-9--iam-driver-pack-entry-point)
13. [Step 10 — Docker Compose & Justfile](#step-10--docker-compose--justfile)
14. [Step 11 — Unit Tests](#step-11--unit-tests)
15. [Step 12 — Integration Tests](#step-12--integration-tests)
16. [IAM-Group-Specific Design Decisions](#iam-group-specific-design-decisions)
17. [Design Decisions (Resolved)](#design-decisions-resolved)
18. [Checklist](#checklist)

---

## 1. Overview & Scope

The IAM Group driver manages the lifecycle of IAM **groups** only. It creates,
imports, updates, and deletes IAM groups along with their inline policies and
managed policy attachments.

**Out of scope**:
- **Group membership** — managed by the IAM User driver. The user driver calls
  `AddUserToGroup` / `RemoveUserFromGroup` to manage which groups a user belongs to.
  The group driver does not manage its member list.
- **Tags** — IAM groups do **not** support tagging in the AWS API (unlike roles,
  users, and policies). There is no `TagGroup` / `UntagGroup` API.

IAM groups provide a way to organize users and apply policies to multiple users at
once. They are a grouping mechanism for permissions, not a security boundary. In
compound templates, groups are typically created before users (so users can reference
group names in their `groups` list).

### Driver Contract

| Handler | Context | Purpose |
|---|---|---|
| `Provision` | `ObjectContext` (exclusive) | Create or converge an IAM group |
| `Import` | `ObjectContext` (exclusive) | Adopt an existing IAM group |
| `Delete` | `ObjectContext` (exclusive) | Remove an IAM group (cleanup first) |
| `Reconcile` | `ObjectContext` (exclusive) | Detect/correct drift (report-only for Observed mode) |
| `GetStatus` | `ObjectSharedContext` (shared) | Return current status |
| `GetOutputs` | `ObjectSharedContext` (shared) | Return IAM group outputs |

### Mutable vs Immutable Attributes

| Attribute | Mutability | Notes |
|---|---|---|
| `groupName` | Immutable | Part of the Virtual Object key; cannot change after creation |
| `path` | Mutable | Updated via `UpdateGroup` |
| `inlinePolicies` | Mutable | Updated via `PutGroupPolicy` / `DeleteGroupPolicy` |
| `managedPolicyArns` | Mutable | Updated via `AttachGroupPolicy` / `DetachGroupPolicy` |

> **No tags**: IAM groups do not support the AWS tagging API. This simplifies the
> driver compared to IAM roles and users.

### Downstream Consumers

```
${resources.my-group.outputs.arn}           → Policy conditions
${resources.my-group.outputs.groupId}       → Audit references
${resources.my-group.outputs.groupName}     → IAMUser spec.groups
```

---

## 2. Key Strategy

### Key Scope: `KeyScopeGlobal`

IAM is a global service. Group names are unique within an account. Key is `groupName`.

### BuildKey vs BuildImportKey

- **`BuildKey(resourceDoc)`**: Extracts `metadata.name`. Returns group name.
- **`BuildImportKey(region, resourceID)`**: Returns `resourceID` (group name).
  Same key as `BuildKey`.

### No Ownership Tags

Group names are AWS-enforced unique. `CreateGroup` returns `EntityAlreadyExists`.

---

## 3. File Inventory

```text
✦ schemas/aws/iam/group.cue                         — CUE schema for IAMGroup resource
✦ internal/drivers/iamgroup/types.go                 — Spec, Outputs, ObservedState, State structs
✦ internal/drivers/iamgroup/aws.go                   — IAMGroupAPI interface + realIAMGroupAPI
✦ internal/drivers/iamgroup/drift.go                 — HasDrift(), ComputeFieldDiffs()
✦ internal/drivers/iamgroup/driver.go                — IAMGroupDriver Virtual Object
✦ internal/drivers/iamgroup/driver_test.go           — Unit tests for driver
✦ internal/drivers/iamgroup/aws_test.go              — Unit tests for error classification
✦ internal/drivers/iamgroup/drift_test.go            — Unit tests for drift detection
✦ internal/core/provider/iamgroup_adapter.go         — IAMGroupAdapter implementing provider.Adapter
✦ internal/core/provider/iamgroup_adapter_test.go    — Unit tests for adapter
✦ tests/integration/iamgroup_driver_test.go          — Integration tests
✎ cmd/praxis-identity/main.go                            — Add IAMGroup driver .Bind()
✎ internal/core/provider/registry.go                 — Add NewIAMGroupAdapter to NewRegistry()
```

---

## Step 1 — CUE Schema

**File**: `schemas/aws/iam/group.cue`

```cue
package iam

#IAMGroup: {
    apiVersion: "praxis.io/v1"
    kind:       "IAMGroup"

    metadata: {
        // name is used as the IAM group name in AWS.
        name: string & =~"^[a-zA-Z0-9+=,.@_-]{1,128}$"
        labels: [string]: string
    }

    spec: {
        // path is the IAM path prefix.
        path: string | *"/"

        // inlinePolicies are policy documents embedded directly in the group.
        // Key is the policy name, value is the JSON policy document.
        inlinePolicies: [string]: string

        // managedPolicyArns is a list of managed policy ARNs to attach.
        managedPolicyArns: [...string] | *[]
    }

    outputs?: {
        arn:       string
        groupId:   string
        groupName: string
    }
}
```

### Key Design Decisions

- **No tags field**: IAM groups do not support AWS tagging. Including a `tags` field
  would be misleading and cause API errors.

- **No members field**: Group membership is managed by the user driver, not the group
  driver. Including a members list would create bidirectional management (group
  declares its members AND user declares its groups), leading to conflicts and
  reconciliation loops.

---

## Step 2 — AWS Client Factory

Uses same IAM client as other IAM drivers. No changes needed.

---

## Step 3 — Driver Types

**File**: `internal/drivers/iamgroup/types.go`

```go
package iamgroup

import "github.com/shirvan/praxis/pkg/types"

const ServiceName = "IAMGroup"

type IAMGroupSpec struct {
    Account           string            `json:"account,omitempty"`
    Path              string            `json:"path"`
    GroupName         string            `json:"groupName"`
    InlinePolicies    map[string]string `json:"inlinePolicies,omitempty"`
    ManagedPolicyArns []string          `json:"managedPolicyArns,omitempty"`
}

type IAMGroupOutputs struct {
    Arn       string `json:"arn"`
    GroupId   string `json:"groupId"`
    GroupName string `json:"groupName"`
}

type ObservedState struct {
    Arn               string            `json:"arn"`
    GroupId           string            `json:"groupId"`
    GroupName         string            `json:"groupName"`
    Path              string            `json:"path"`
    InlinePolicies    map[string]string `json:"inlinePolicies"`
    ManagedPolicyArns []string          `json:"managedPolicyArns"`
    CreateDate        string            `json:"createDate"`
}

type IAMGroupState struct {
    Desired            IAMGroupSpec         `json:"desired"`
    Observed           ObservedState        `json:"observed"`
    Outputs            IAMGroupOutputs      `json:"outputs"`
    Status             types.ResourceStatus `json:"status"`
    Mode               types.Mode           `json:"mode"`
    Error              string               `json:"error,omitempty"`
    Generation         int64                `json:"generation"`
    LastReconcile      string               `json:"lastReconcile,omitempty"`
    ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
```

> **Note**: No `Tags` field anywhere — groups don't support tagging.

---

## Step 4 — AWS API Abstraction Layer

**File**: `internal/drivers/iamgroup/aws.go`

### IAMGroupAPI Interface

```go
type IAMGroupAPI interface {
    // CreateGroup creates a new IAM group.
    CreateGroup(ctx context.Context, spec IAMGroupSpec) (arn, groupId string, err error)

    // DescribeGroup returns the observed state of a group by name.
    DescribeGroup(ctx context.Context, groupName string) (ObservedState, error)

    // DeleteGroup deletes a group (must have no members or policies).
    DeleteGroup(ctx context.Context, groupName string) error

    // UpdateGroup updates the group's path.
    UpdateGroup(ctx context.Context, groupName, newPath string) error

    // PutInlinePolicy creates or updates an inline policy on the group.
    PutInlinePolicy(ctx context.Context, groupName, policyName, policyDocument string) error

    // DeleteInlinePolicy removes an inline policy from the group.
    DeleteInlinePolicy(ctx context.Context, groupName, policyName string) error

    // ListInlinePolicies returns the names of all inline policies on the group.
    ListInlinePolicies(ctx context.Context, groupName string) ([]string, error)

    // GetInlinePolicy returns the policy document for an inline policy.
    GetInlinePolicy(ctx context.Context, groupName, policyName string) (string, error)

    // AttachManagedPolicy attaches a managed policy to the group.
    AttachManagedPolicy(ctx context.Context, groupName, policyArn string) error

    // DetachManagedPolicy detaches a managed policy from the group.
    DetachManagedPolicy(ctx context.Context, groupName, policyArn string) error

    // ListAttachedPolicies returns the ARNs of all managed policies attached.
    ListAttachedPolicies(ctx context.Context, groupName string) ([]string, error)

    // RemoveAllMembers removes all users from the group.
    RemoveAllMembers(ctx context.Context, groupName string) error
}
```

### realIAMGroupAPI Implementation

```go
type realIAMGroupAPI struct {
    client  *iam.Client
    limiter *ratelimit.Limiter
}

func NewIAMGroupAPI(client *iam.Client) IAMGroupAPI {
    return &realIAMGroupAPI{
        client:  client,
        limiter: ratelimit.New("iam", 15, 8),
    }
}
```

### Key Implementation Details

#### `CreateGroup`

```go
func (r *realIAMGroupAPI) CreateGroup(ctx context.Context, spec IAMGroupSpec) (string, string, error) {
    input := &iam.CreateGroupInput{
        GroupName: aws.String(spec.GroupName),
        Path:      aws.String(spec.Path),
    }

    out, err := r.client.CreateGroup(ctx, input)
    if err != nil {
        return "", "", err
    }
    return aws.ToString(out.Group.Arn),
           aws.ToString(out.Group.GroupId),
           nil
}
```

#### `DescribeGroup` — Composite Describe

```go
func (r *realIAMGroupAPI) DescribeGroup(ctx context.Context, groupName string) (ObservedState, error) {
    // 1. GetGroup — base group attributes
    groupOut, err := r.client.GetGroup(ctx, &iam.GetGroupInput{
        GroupName: aws.String(groupName),
    })
    if err != nil {
        return ObservedState{}, err
    }
    group := groupOut.Group

    // 2. ListGroupPolicies — inline policy names
    inlineNames, err := r.ListInlinePolicies(ctx, groupName)
    if err != nil {
        return ObservedState{}, err
    }

    // 3. GetGroupPolicy for each — fetch documents
    inlinePolicies := make(map[string]string, len(inlineNames))
    for _, name := range inlineNames {
        doc, err := r.GetInlinePolicy(ctx, groupName, name)
        if err != nil {
            return ObservedState{}, err
        }
        inlinePolicies[name] = doc
    }

    // 4. ListAttachedGroupPolicies — managed policy ARNs
    managedArns, err := r.ListAttachedPolicies(ctx, groupName)
    if err != nil {
        return ObservedState{}, err
    }

    obs := ObservedState{
        Arn:               aws.ToString(group.Arn),
        GroupId:           aws.ToString(group.GroupId),
        GroupName:         aws.ToString(group.GroupName),
        Path:              aws.ToString(group.Path),
        InlinePolicies:    inlinePolicies,
        ManagedPolicyArns: managedArns,
    }
    if group.CreateDate != nil {
        obs.CreateDate = group.CreateDate.Format(time.RFC3339)
    }
    return obs, nil
}
```

#### `RemoveAllMembers` — Pre-Deletion Helper

```go
func (r *realIAMGroupAPI) RemoveAllMembers(ctx context.Context, groupName string) error {
    // GetGroup returns the group's members in the Users field
    groupOut, err := r.client.GetGroup(ctx, &iam.GetGroupInput{
        GroupName: aws.String(groupName),
    })
    if err != nil {
        return err
    }

    for _, user := range groupOut.Users {
        _, err := r.client.RemoveUserFromGroup(ctx, &iam.RemoveUserFromGroupInput{
            GroupName: aws.String(groupName),
            UserName:  user.UserName,
        })
        if err != nil {
            return err
        }
    }
    return nil
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
```

---

## Step 5 — Drift Detection

**File**: `internal/drivers/iamgroup/drift.go`

### Core Functions

**`HasDrift(desired IAMGroupSpec, observed ObservedState) bool`**

```go
func HasDrift(desired IAMGroupSpec, observed ObservedState) bool {
    if desired.Path != observed.Path {
        return true
    }
    if !inlinePoliciesEqual(desired.InlinePolicies, observed.InlinePolicies) {
        return true
    }
    return !managedPolicyArnsEqual(desired.ManagedPolicyArns, observed.ManagedPolicyArns)
}
```

> **Note**: No tag comparison — groups don't support tags.

**`ComputeFieldDiffs`**: Reports diffs for path, inline policies (per-policy diffs),
and managed policy ARNs (set diffs).

Uses the same `policyDocumentsEqual` / `canonicalizePolicyDoc` pattern as IAM Role
and IAM Policy drivers.

---

## Step 6 — Driver Implementation

**File**: `internal/drivers/iamgroup/driver.go`

### Service Registration

```go
const ServiceName = "IAMGroup"
```

### Constructor Pattern

Standard dual constructor:

```go
func NewIAMGroupDriver(accounts *auth.Registry) *IAMGroupDriver
func NewIAMGroupDriverWithFactory(accounts *auth.Registry, factory func(aws.Config) IAMGroupAPI) *IAMGroupDriver
```

### Provision Handler

1. **Input validation**: `groupName` must be non-empty.

2. **Load current state**: Read, set `Provisioning`, increment generation.

3. **Re-provision check**: If `state.Outputs.Arn` is non-empty, describe the group.
   If gone, fall through to creation.

4. **Create group**: Calls `api.CreateGroup`. Classifies:
   - `IsAlreadyExists` → `TerminalError(409)`

5. **Converge mutable attributes** (re-provision path):
   - Path: update if changed.
   - Inline policies: add-before-remove.
   - Managed policies: attach-before-detach.

6. **Describe final state**: Composite describe.

7. **Commit state**: Set `Ready`, save, schedule reconcile.

### Import Handler

1. Describes group by name.
2. Synthesizes `IAMGroupSpec` from observed state.
3. Sets mode to `ModeObserved`.
4. Schedules reconciliation.

### Delete Handler

Pre-cleanup before deletion:

1. **Remove all members**: Calls `api.RemoveAllMembers` — lists users in group and
   removes each. This is necessary because AWS does not allow deleting a group with
   members.
2. **Detach all managed policies**.
3. **Delete all inline policies**.
4. **Delete group**: Calls `api.DeleteGroup`.
5. Error classification:
   - `IsDeleteConflict` → `TerminalError(409)`.
   - `IsNotFound` → silent success.

### Reconcile Handler

Standard pattern:

1. Describe current state.
2. **Managed + drift**: Converge path, inline policies, managed policies.
3. **Observed + drift**: Report only.
4. Re-schedule.

### GetStatus / GetOutputs

Standard shared handlers.

---

## Step 7 — Provider Adapter

**File**: `internal/core/provider/iamgroup_adapter.go`

### Methods

**`Scope() KeyScope`** → `KeyScopeGlobal`

**`Kind() string`** → `"IAMGroup"`

**`ServiceName() string`** → `"IAMGroup"`

**`BuildKey(resourceDoc)`**: Returns `metadata.name` (group name).

**`BuildImportKey(region, resourceID)`**: Returns `resourceID` (group name).

**`Plan(ctx, key, account, desiredSpec)`**: Calls `api.DescribeGroup(groupName)`.
Not found → `OpCreate`. Found → compare, return `OpNoOp` or `OpUpdate`.

---

## Step 8 — Registry Integration

Add `NewIAMGroupAdapterWithRegistry(accounts)` to `NewRegistry()`.

---

## Step 9 — Identity Driver Pack Entry Point

**File**: `cmd/praxis-identity/main.go` (modified)

Add `.Bind(restate.Reflect(iamgroup.NewIAMGroupDriver(cfg.Auth())))`.

---

## Step 10 — Docker Compose & Justfile

No Docker Compose changes. Add justfile targets:

| Target | Command |
|---|---|
| `test-iamgroup` | `go test ./internal/drivers/iamgroup/... -v -count=1 -race` |
| `test-iamgroup-integration` | `go test ./tests/integration/ -run TestIAMGroup -v -count=1 -tags=integration -timeout=5m` |

---

## Step 11 — Unit Tests

### `internal/drivers/iamgroup/drift_test.go`

| Test | Purpose |
|---|---|
| `TestHasDrift_NoDrift` | Matching state → no drift |
| `TestHasDrift_PathDrift` | Path change → drift |
| `TestHasDrift_InlinePolicyAdded` | New inline policy → drift |
| `TestHasDrift_InlinePolicyRemoved` | Missing policy → drift |
| `TestHasDrift_InlinePolicyChanged` | Changed document → drift |
| `TestHasDrift_ManagedPolicyAdded` | New managed policy → drift |
| `TestHasDrift_ManagedPolicyRemoved` | Missing policy → drift |
| `TestHasDrift_ManagedPolicyOrderIndependent` | Same ARNs, different order → no drift |
| `TestComputeFieldDiffs_Path` | Path diff |
| `TestComputeFieldDiffs_InlinePolicies` | Per-policy diffs |
| `TestComputeFieldDiffs_ManagedPolicies` | Set diffs |

### `internal/drivers/iamgroup/aws_test.go`

| Test | Purpose |
|---|---|
| `TestIsNotFound_NoSuchEntity` | True for NoSuchEntity |
| `TestIsAlreadyExists_True` | True for EntityAlreadyExists |
| `TestIsDeleteConflict_True` | True for DeleteConflict |
| `TestIsNotFound_WrappedRestateError` | String fallback |

### `internal/drivers/iamgroup/driver_test.go`

| Test | Purpose |
|---|---|
| `TestSpecFromObserved_RoundTrip` | Observed → spec round-trip |
| `TestServiceName` | Returns "IAMGroup" |
| `TestOutputsFromObserved` | Correct output mapping |

### `internal/core/provider/iamgroup_adapter_test.go`

| Test | Purpose |
|---|---|
| `TestIAMGroupAdapter_BuildKey` | Returns group name |
| `TestIAMGroupAdapter_BuildImportKey` | Returns group name (same) |
| `TestIAMGroupAdapter_Kind` | Returns "IAMGroup" |
| `TestIAMGroupAdapter_Scope` | Returns `KeyScopeGlobal` |
| `TestIAMGroupAdapter_NormalizeOutputs` | Converts struct to map |

---

## Step 12 — Integration Tests

**File**: `tests/integration/iamgroup_driver_test.go`

| Test | Description |
|---|---|
| `TestIAMGroupProvision_CreatesGroup` | Creates group with inline and managed policies. Verifies via `GetGroup`. |
| `TestIAMGroupProvision_Idempotent` | Two provisions, same ARN. |
| `TestIAMGroupProvision_UpdatePath` | Re-provisions with new path. Verifies path changed. |
| `TestIAMGroupProvision_AddInlinePolicy` | Re-provisions with new policy. Verifies both exist. |
| `TestIAMGroupProvision_AttachManagedPolicy` | Re-provisions with new ARN. Verifies attachment. |
| `TestIAMGroupImport_ExistingGroup` | Creates via IAM API, imports via driver. Verifies Observed mode. |
| `TestIAMGroupDelete_RemovesGroup` | Provisions, adds a user to group via IAM API, deletes. Verifies member removal + group deletion. |
| `TestIAMGroupReconcile_DetectsInlinePolicyDrift` | Adds inline policy directly. Reconcile removes it. |
| `TestIAMGroupGetStatus_ReturnsReady` | Provisions and checks status. |

---

## IAM-Group-Specific Design Decisions

### 1. No Tags Support

IAM groups are one of the few IAM resource types that do not support the AWS tagging
API. The driver omits tags entirely from the spec, observed state, and drift
detection. This is simpler than other IAM drivers but means groups cannot be labeled
for organizational purposes at the AWS level.

### 2. No Members Management

Group membership is intentionally managed by the user driver, not the group driver.
This avoids:
- **Bidirectional management**: If both user and group drivers manage membership,
  they would fight during reconciliation.
- **Circular dependencies**: A user template listing groups and a group template
  listing members would create a dependency cycle.
- **The AWS API model**: `AddUserToGroup` is conceptually "user joins group", not
  "group adds user". The user is the active party.

The group driver's Delete handler does remove all members as a pre-deletion cleanup
step, but this is a one-time destructive operation, not ongoing management.

### 3. Pre-Deletion Member Removal

AWS requires a group to have zero members before it can be deleted. The Delete
handler calls `RemoveAllMembers` before deleting the group. This is a potentially
disruptive operation — removing users from a group revokes their group-inherited
permissions immediately. The Delete mode guard (ModeObserved blocks delete) protects
imported groups from accidental deletion.

### 4. Import Defaults to ModeObserved

IAM groups may have members that depend on the group's policies for access. Accidental
deletion or policy changes could disrupt multiple users. Import defaults to
ModeObserved.

### 5. Path is Mutable

Like IAM users, group paths can be changed via `UpdateGroup`. The driver converges
path during re-provision and reconciliation.

---

## Design Decisions (Resolved)

1. **Should the group driver manage members?**
   No. The user driver manages group membership. See "No Members Management" above.

2. **Should Delete fail if the group has members?**
   No. The Delete handler removes all members automatically. The user's intent is
   "delete this group" — requiring manual member removal is poor UX. The mode guard
   prevents accidental deletion of imported groups.

3. **Should the driver detect member drift?**
   No. Member drift is the user driver's concern. If a user is added to the group
   externally, the user driver's reconciliation (not the group driver's) would detect
   and correct it.

4. **Should inline policies use the same canonicalization as IAM Role/Policy?**
   Yes. Same `canonicalizePolicyDoc` pattern for URL-decode + JSON normalization.

---

## Checklist

- [ ] **Schema**: `schemas/aws/iam/group.cue` created
- [ ] **Types**: `internal/drivers/iamgroup/types.go` created
- [ ] **AWS API**: `internal/drivers/iamgroup/aws.go` created
- [ ] **Drift**: `internal/drivers/iamgroup/drift.go` created
- [ ] **Driver**: `internal/drivers/iamgroup/driver.go` created with all 6 handlers
- [ ] **Adapter**: `internal/core/provider/iamgroup_adapter.go` created
- [ ] **Registry**: `internal/core/provider/registry.go` updated
- [ ] **Entry point**: IAMGroup driver bound in `cmd/praxis-identity/main.go`
- [ ] **Unit tests (drift)**: `internal/drivers/iamgroup/drift_test.go`
- [ ] **Unit tests (aws)**: `internal/drivers/iamgroup/aws_test.go`
- [ ] **Unit tests (driver)**: `internal/drivers/iamgroup/driver_test.go`
- [ ] **Unit tests (adapter)**: `internal/core/provider/iamgroup_adapter_test.go`
- [ ] **Integration tests**: `tests/integration/iamgroup_driver_test.go`
- [ ] **No tags**: Confirmed groups don't support tagging
- [ ] **Pre-deletion cleanup**: Remove members, detach policies, delete inline policies
- [ ] **No member management**: Verified as user driver concern
- [ ] **Import default mode**: `ModeObserved`
- [ ] **Delete mode guard**: Delete handler blocks for ModeObserved (409)
- [ ] **Build passes**: `go build ./...` succeeds
- [ ] **Unit tests pass**: `go test ./internal/drivers/iamgroup/... -race`
- [ ] **Integration tests pass**: `go test ./tests/integration/ -run TestIAMGroup -tags=integration`
