# IAM User Driver — Implementation Plan

> ✅ Implemented
> Target: A Restate Virtual Object driver that manages IAM Users, providing full
> lifecycle management including creation, import, deletion, drift detection, and
> drift correction for user properties, inline policies, managed policy attachments,
> group memberships, and tags.
>
> Key scope: `KeyScopeGlobal` — key format is `userName`, permanent and immutable
> for the lifetime of the Virtual Object. IAM user names are unique within an AWS
> account (IAM is a global service with no region scoping).

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
16. [IAM-User-Specific Design Decisions](#iam-user-specific-design-decisions)
17. [Design Decisions (Resolved)](#design-decisions-resolved)
18. [Checklist](#checklist)

---

## 1. Overview & Scope

The IAM User driver manages the lifecycle of IAM **users** only. It creates, imports,
updates, and deletes IAM users along with their inline policies, managed policy
attachments, group memberships, and tags.

**Out of scope**:
- **Login profiles** (Console passwords) — sensitive credential management that
  should be handled separately or via AWS SSO.
- **Access keys** — programmatic credential management. Access key rotation is a
  distinct operational concern not suited for declarative infrastructure management.
- **MFA devices** — per-user security configuration that is operational, not
  infrastructure.
- **SSH public keys / service-specific credentials** — niche use cases handled
  outside the core user lifecycle.

The driver manages the user *resource* and its policy/group associations, not the
user's credentials or authentication artifacts.

### Driver Contract

| Handler | Context | Purpose |
|---|---|---|
| `Provision` | `ObjectContext` (exclusive) | Create or converge an IAM user |
| `Import` | `ObjectContext` (exclusive) | Adopt an existing IAM user |
| `Delete` | `ObjectContext` (exclusive) | Remove an IAM user (full cleanup) |
| `Reconcile` | `ObjectContext` (exclusive) | Detect/correct drift (report-only for Observed mode) |
| `GetStatus` | `ObjectSharedContext` (shared) | Return current status |
| `GetOutputs` | `ObjectSharedContext` (shared) | Return IAM user outputs |

### Mutable vs Immutable Attributes

| Attribute | Mutability | Notes |
|---|---|---|
| `userName` | Immutable | Part of the Virtual Object key; cannot change after creation |
| `path` | Mutable | Updated via `UpdateUser` (unlike IAM roles, user paths CAN be changed) |
| `permissionsBoundary` | Mutable | Updated via `PutUserPermissionsBoundary` / `DeleteUserPermissionsBoundary` |
| `inlinePolicies` | Mutable | Updated via `PutUserPolicy` / `DeleteUserPolicy` |
| `managedPolicyArns` | Mutable | Updated via `AttachUserPolicy` / `DetachUserPolicy` |
| `groups` | Mutable | Updated via `AddUserToGroup` / `RemoveUserFromGroup` |
| `tags` | Mutable | Full replace via `TagUser` / `UntagUser` |

### Downstream Consumers

```
${resources.my-user.outputs.arn}            → Policy conditions, cross-account trust
${resources.my-user.outputs.userId}         → Audit references, resource policies
${resources.my-user.outputs.userName}       → CLI references, group membership
```

---

## 2. Key Strategy

### Key Scope: `KeyScopeGlobal`

IAM is a global service. User names are unique within an account. Key is `userName`.

### BuildKey vs BuildImportKey

- **`BuildKey(resourceDoc)`**: Extracts `metadata.name`. Returns user name.
- **`BuildImportKey(region, resourceID)`**: Returns `resourceID` (the user name).
  Same key as `BuildKey` — matches S3/IAMRole/IAMPolicy pattern.

### No Ownership Tags

IAM user names are unique within an account. `CreateUser` returns
`EntityAlreadyExists` if the name exists. No ownership tags needed.

---

## 3. File Inventory

```text
✦ schemas/aws/iam/user.cue                          — CUE schema for IAMUser resource
✦ internal/drivers/iamuser/types.go                  — Spec, Outputs, ObservedState, State structs
✦ internal/drivers/iamuser/aws.go                    — IAMUserAPI interface + realIAMUserAPI
✦ internal/drivers/iamuser/drift.go                  — HasDrift(), ComputeFieldDiffs()
✦ internal/drivers/iamuser/driver.go                 — IAMUserDriver Virtual Object
✦ internal/drivers/iamuser/driver_test.go            — Unit tests for driver
✦ internal/drivers/iamuser/aws_test.go               — Unit tests for error classification
✦ internal/drivers/iamuser/drift_test.go             — Unit tests for drift detection
✦ internal/core/provider/iamuser_adapter.go          — IAMUserAdapter implementing provider.Adapter
✦ internal/core/provider/iamuser_adapter_test.go     — Unit tests for adapter
✦ tests/integration/iamuser_driver_test.go           — Integration tests
✎ cmd/praxis-identity/main.go                            — Add IAMUser driver .Bind()
✎ internal/core/provider/registry.go                 — Add NewIAMUserAdapter to NewRegistry()
```

---

## Step 1 — CUE Schema

**File**: `schemas/aws/iam/user.cue`

```cue
package iam

#IAMUser: {
    apiVersion: "praxis.io/v1"
    kind:       "IAMUser"

    metadata: {
        // name is used as the IAM user name in AWS.
        name: string & =~"^[a-zA-Z0-9+=,.@_-]{1,64}$"
        labels: [string]: string
    }

    spec: {
        // path is the IAM path prefix.
        path: string | *"/"

        // permissionsBoundary is the ARN of a managed policy used as the
        // permissions boundary for the user.
        permissionsBoundary?: string

        // inlinePolicies are policy documents embedded directly in the user.
        // Key is the policy name, value is the JSON policy document.
        inlinePolicies: [string]: string

        // managedPolicyArns is a list of managed policy ARNs to attach.
        managedPolicyArns: [...string] | *[]

        // groups is a list of IAM group names the user should belong to.
        groups: [...string] | *[]

        // tags applied to the IAM user.
        tags: [string]: string
    }

    outputs?: {
        arn:      string
        userId:   string
        userName: string
    }
}
```

### Key Design Decisions

- **`groups` as list of names**: Group membership is managed by the user driver,
  not the group driver. This follows the AWS API model where `AddUserToGroup` takes
  a user name and group name. The group must exist before the user can be added;
  in compound templates, the DAG enforces this ordering.

- **No login profile or access keys**: These are credential artifacts, not
  infrastructure resources. Managing passwords and access keys via declarative
  templates would create security risks (credentials in state/journal). Credentials
  are handled out-of-band.

---

## Step 2 — AWS Client Factory

Uses same IAM client as IAM Role and IAM Policy drivers. No changes needed.

---

## Step 3 — Driver Types

**File**: `internal/drivers/iamuser/types.go`

```go
package iamuser

import "github.com/praxiscloud/praxis/pkg/types"

const ServiceName = "IAMUser"

type IAMUserSpec struct {
    Account             string            `json:"account,omitempty"`
    Path                string            `json:"path"`
    UserName            string            `json:"userName"`
    PermissionsBoundary string            `json:"permissionsBoundary,omitempty"`
    InlinePolicies      map[string]string `json:"inlinePolicies,omitempty"`
    ManagedPolicyArns   []string          `json:"managedPolicyArns,omitempty"`
    Groups              []string          `json:"groups,omitempty"`
    Tags                map[string]string `json:"tags,omitempty"`
}

type IAMUserOutputs struct {
    Arn      string `json:"arn"`
    UserId   string `json:"userId"`
    UserName string `json:"userName"`
}

type ObservedState struct {
    Arn                 string            `json:"arn"`
    UserId              string            `json:"userId"`
    UserName            string            `json:"userName"`
    Path                string            `json:"path"`
    PermissionsBoundary string            `json:"permissionsBoundary"`
    InlinePolicies      map[string]string `json:"inlinePolicies"`
    ManagedPolicyArns   []string          `json:"managedPolicyArns"`
    Groups              []string          `json:"groups"`
    Tags                map[string]string `json:"tags"`
    CreateDate          string            `json:"createDate"`
}

type IAMUserState struct {
    Desired            IAMUserSpec          `json:"desired"`
    Observed           ObservedState        `json:"observed"`
    Outputs            IAMUserOutputs       `json:"outputs"`
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

**File**: `internal/drivers/iamuser/aws.go`

### IAMUserAPI Interface

```go
type IAMUserAPI interface {
    // CreateUser creates a new IAM user.
    CreateUser(ctx context.Context, spec IAMUserSpec) (arn, userId string, err error)

    // DescribeUser returns the observed state of a user by name.
    DescribeUser(ctx context.Context, userName string) (ObservedState, error)

    // DeleteUser deletes a user (must have no attached resources).
    DeleteUser(ctx context.Context, userName string) error

    // UpdateUser updates the user's path.
    UpdateUser(ctx context.Context, userName, newPath string) error

    // PutPermissionsBoundary sets or updates the permissions boundary.
    PutPermissionsBoundary(ctx context.Context, userName, policyArn string) error

    // DeletePermissionsBoundary removes the permissions boundary.
    DeletePermissionsBoundary(ctx context.Context, userName string) error

    // PutInlinePolicy creates or updates an inline policy on the user.
    PutInlinePolicy(ctx context.Context, userName, policyName, policyDocument string) error

    // DeleteInlinePolicy removes an inline policy from the user.
    DeleteInlinePolicy(ctx context.Context, userName, policyName string) error

    // ListInlinePolicies returns the names of all inline policies on the user.
    ListInlinePolicies(ctx context.Context, userName string) ([]string, error)

    // GetInlinePolicy returns the policy document for an inline policy.
    GetInlinePolicy(ctx context.Context, userName, policyName string) (string, error)

    // AttachManagedPolicy attaches a managed policy to the user.
    AttachManagedPolicy(ctx context.Context, userName, policyArn string) error

    // DetachManagedPolicy detaches a managed policy from the user.
    DetachManagedPolicy(ctx context.Context, userName, policyArn string) error

    // ListAttachedPolicies returns the ARNs of all managed policies attached.
    ListAttachedPolicies(ctx context.Context, userName string) ([]string, error)

    // AddToGroup adds the user to an IAM group.
    AddToGroup(ctx context.Context, userName, groupName string) error

    // RemoveFromGroup removes the user from an IAM group.
    RemoveFromGroup(ctx context.Context, userName, groupName string) error

    // ListGroups returns the group names the user belongs to.
    ListGroups(ctx context.Context, userName string) ([]string, error)

    // UpdateTags replaces all user tags on the IAM user.
    UpdateTags(ctx context.Context, userName string, tags map[string]string) error
}
```

### realIAMUserAPI Implementation

```go
type realIAMUserAPI struct {
    client  *iam.Client
    limiter *ratelimit.Limiter
}

func NewIAMUserAPI(client *iam.Client) IAMUserAPI {
    return &realIAMUserAPI{
        client:  client,
        limiter: ratelimit.New("iam", 15, 8),
    }
}
```

### Key Implementation Details

#### `DescribeUser` — Composite Describe

Like IAM roles, describing a user fully requires multiple API calls:

```go
func (r *realIAMUserAPI) DescribeUser(ctx context.Context, userName string) (ObservedState, error) {
    // 1. GetUser — base user attributes
    userOut, err := r.client.GetUser(ctx, &iam.GetUserInput{
        UserName: aws.String(userName),
    })
    if err != nil {
        return ObservedState{}, err
    }
    user := userOut.User

    // 2. ListUserPolicies — inline policy names
    inlineNames, err := r.ListInlinePolicies(ctx, userName)
    if err != nil {
        return ObservedState{}, err
    }

    // 3. GetUserPolicy for each — fetch documents
    inlinePolicies := make(map[string]string, len(inlineNames))
    for _, name := range inlineNames {
        doc, err := r.GetInlinePolicy(ctx, userName, name)
        if err != nil {
            return ObservedState{}, err
        }
        inlinePolicies[name] = doc
    }

    // 4. ListAttachedUserPolicies — managed policy ARNs
    managedArns, err := r.ListAttachedPolicies(ctx, userName)
    if err != nil {
        return ObservedState{}, err
    }

    // 5. ListGroupsForUser — group memberships
    groups, err := r.ListGroups(ctx, userName)
    if err != nil {
        return ObservedState{}, err
    }

    obs := ObservedState{
        Arn:               aws.ToString(user.Arn),
        UserId:            aws.ToString(user.UserId),
        UserName:          aws.ToString(user.UserName),
        Path:              aws.ToString(user.Path),
        InlinePolicies:    inlinePolicies,
        ManagedPolicyArns: managedArns,
        Groups:            groups,
        Tags:              fromIAMTags(user.Tags),
    }
    if user.PermissionsBoundary != nil {
        obs.PermissionsBoundary = aws.ToString(user.PermissionsBoundary.PermissionsBoundaryArn)
    }
    if user.CreateDate != nil {
        obs.CreateDate = user.CreateDate.Format(time.RFC3339)
    }
    return obs, nil
}
```

#### `DeleteUser` — Pre-Cleanup Required

Before deleting a user, AWS requires removal of:
- Login profiles
- Access keys
- MFA devices
- Inline policies
- Managed policy attachments
- Group memberships
- SSH public keys
- Service-specific credentials
- Signing certificates

The driver handles the subset it manages (inline policies, managed policies, groups)
and attempts to clean up login profiles and access keys defensively.

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

**File**: `internal/drivers/iamuser/drift.go`

### Core Functions

**`HasDrift(desired IAMUserSpec, observed ObservedState) bool`**

```go
func HasDrift(desired IAMUserSpec, observed ObservedState) bool {
    if desired.Path != observed.Path {
        return true
    }
    if desired.PermissionsBoundary != observed.PermissionsBoundary {
        return true
    }
    if !inlinePoliciesEqual(desired.InlinePolicies, observed.InlinePolicies) {
        return true
    }
    if !managedPolicyArnsEqual(desired.ManagedPolicyArns, observed.ManagedPolicyArns) {
        return true
    }
    if !groupsEqual(desired.Groups, observed.Groups) {
        return true
    }
    return !tagsMatch(desired.Tags, observed.Tags)
}
```

**Inline policy and managed policy comparison**: Uses the same canonicalized JSON
comparison and sorted set comparison patterns as the IAM Role driver.

**Group membership comparison**: Set-based comparison (order-independent):

```go
func groupsEqual(desired, observed []string) bool {
    if len(desired) != len(observed) {
        return false
    }
    dSet := make(map[string]bool, len(desired))
    for _, g := range desired {
        dSet[g] = true
    }
    for _, g := range observed {
        if !dSet[g] {
            return false
        }
    }
    return true
}
```

**`ComputeFieldDiffs`**: Reports diffs for path, permissions boundary, inline
policies, managed policy ARNs, groups, and tags. All are mutable for IAM users
(unlike roles where path is immutable).

---

## Step 6 — Driver Implementation

**File**: `internal/drivers/iamuser/driver.go`

### Service Registration

```go
const ServiceName = "IAMUser"
```

### Constructor Pattern

```go
func NewIAMUserDriver(accounts *auth.Registry) *IAMUserDriver
func NewIAMUserDriverWithFactory(accounts *auth.Registry, factory func(aws.Config) IAMUserAPI) *IAMUserDriver
```

### Provision Handler

1. **Input validation**: `userName` must be non-empty.

2. **Load current state**: Read, set `Provisioning`, increment generation.

3. **Re-provision check**: If `state.Outputs.Arn` is non-empty, describe the user.
   If gone (404), clear and fall through to creation.

4. **Create user**: Calls `api.CreateUser`. Classifies errors:
   - `IsAlreadyExists` → `TerminalError(409)`

5. **Converge mutable attributes** (re-provision path):
   - Path: update if changed via `api.UpdateUser`.
   - Permissions boundary: add/update/remove.
   - Inline policies: add-before-remove convergence (same as IAM Role).
   - Managed policies: attach-before-detach convergence.
   - Groups: add-before-remove convergence.
   - Tags: update if changed.

6. **Describe final state**: Composite describe.

7. **Commit state**: Set `Ready`, save atomically, schedule reconcile.

### Import Handler

1. Describes user by `ref.ResourceID` (user name).
2. Synthesizes `IAMUserSpec` from observed state.
3. Sets mode to `ModeObserved`.
4. Schedules reconciliation.

### Delete Handler

Comprehensive pre-cleanup before deletion:

1. Sets status to `Deleting`.
2. **Remove from all groups**: Lists groups, removes from each.
3. **Detach all managed policies**: Lists attached policies, detaches each.
4. **Delete all inline policies**: Lists inline policies, deletes each.
5. **Delete login profile** (defensive): Attempts to delete, ignores `NoSuchEntity`.
6. **Delete access keys** (defensive): Lists and deletes all access keys.
7. **Delete user**: Calls `api.DeleteUser`.
8. Error classification:
   - `IsDeleteConflict` → `TerminalError(409)` — still has attached resources.
   - `IsNotFound` → silent success.
9. Sets status to `StatusDeleted`.

### Reconcile Handler

Standard pattern with group membership, policy, and tag convergence:

1. Describe current state.
2. **Managed + drift**: Converge path, permissions boundary, inline policies,
   managed policies, groups, tags.
3. **Observed + drift**: Report only.
4. Re-schedule.

**Group membership convergence during reconciliation**:
- Groups in desired but not observed → `AddToGroup`.
- Groups in observed but not desired → `RemoveFromGroup`.

### GetStatus / GetOutputs

Standard shared handlers.

---

## Step 7 — Provider Adapter

**File**: `internal/core/provider/iamuser_adapter.go`

### Methods

**`Scope() KeyScope`** → `KeyScopeGlobal`

**`Kind() string`** → `"IAMUser"`

**`ServiceName() string`** → `"IAMUser"`

**`BuildKey(resourceDoc)`**: Returns `metadata.name` (user name).

**`BuildImportKey(region, resourceID)`**: Returns `resourceID` (user name).

**`Plan(ctx, key, account, desiredSpec)`**:
Calls `api.DescribeUser(userName)`. Not found → `OpCreate`. Found → compare,
return `OpNoOp` or `OpUpdate`.

---

## Step 8 — Registry Integration

Add `NewIAMUserAdapterWithRegistry(accounts)` to `NewRegistry()`.

---

## Step 9 — Identity Driver Pack Entry Point

**File**: `cmd/praxis-identity/main.go` (modified)

Add `.Bind(restate.Reflect(iamuser.NewIAMUserDriver(cfg.Auth())))`.

---

## Step 10 — Docker Compose & Justfile

No Docker Compose changes. Add justfile targets:

| Target | Command |
|---|---|
| `test-iamuser` | `go test ./internal/drivers/iamuser/... -v -count=1 -race` |
| `test-iamuser-integration` | `go test ./tests/integration/ -run TestIAMUser -v -count=1 -tags=integration -timeout=5m` |

---

## Step 11 — Unit Tests

### `internal/drivers/iamuser/drift_test.go`

| Test | Purpose |
|---|---|
| `TestHasDrift_NoDrift` | Matching state → no drift |
| `TestHasDrift_PathDrift` | Path change → drift |
| `TestHasDrift_PermissionsBoundaryDrift` | Boundary change → drift |
| `TestHasDrift_InlinePolicyAdded` | New inline policy → drift |
| `TestHasDrift_InlinePolicyRemoved` | Missing policy → drift |
| `TestHasDrift_InlinePolicyChanged` | Changed document → drift |
| `TestHasDrift_ManagedPolicyAdded` | New managed policy → drift |
| `TestHasDrift_ManagedPolicyRemoved` | Missing policy → drift |
| `TestHasDrift_GroupAdded` | New group membership → drift |
| `TestHasDrift_GroupRemoved` | Missing group → drift |
| `TestHasDrift_GroupOrderIndependent` | Same groups, different order → no drift |
| `TestHasDrift_TagDrift` | Tag change → drift |
| `TestComputeFieldDiffs_AllMutableFields` | Full diff with all changes |

### `internal/drivers/iamuser/aws_test.go`

| Test | Purpose |
|---|---|
| `TestIsNotFound_NoSuchEntity` | True for NoSuchEntity |
| `TestIsAlreadyExists_True` | True for EntityAlreadyExists |
| `TestIsDeleteConflict_True` | True for DeleteConflict |
| `TestIsNotFound_WrappedRestateError` | String fallback |

### `internal/drivers/iamuser/driver_test.go`

| Test | Purpose |
|---|---|
| `TestSpecFromObserved_RoundTrip` | Observed → spec round-trip |
| `TestServiceName` | Returns "IAMUser" |
| `TestOutputsFromObserved` | Correct output mapping |

### `internal/core/provider/iamuser_adapter_test.go`

| Test | Purpose |
|---|---|
| `TestIAMUserAdapter_BuildKey` | Returns user name |
| `TestIAMUserAdapter_BuildImportKey` | Returns user name (same) |
| `TestIAMUserAdapter_Kind` | Returns "IAMUser" |
| `TestIAMUserAdapter_Scope` | Returns `KeyScopeGlobal` |
| `TestIAMUserAdapter_NormalizeOutputs` | Converts struct to map |

---

## Step 12 — Integration Tests

**File**: `tests/integration/iamuser_driver_test.go`

| Test | Description |
|---|---|
| `TestIAMUserProvision_CreatesUser` | Creates user with policies, groups, tags. Verifies via `GetUser`. |
| `TestIAMUserProvision_Idempotent` | Two provisions, same ARN. |
| `TestIAMUserProvision_UpdatePath` | Re-provisions with new path. Verifies path changed. |
| `TestIAMUserProvision_AddGroup` | Re-provisions with additional group. Verifies membership. |
| `TestIAMUserProvision_AddManagedPolicy` | Re-provisions with additional policy. Verifies attachment. |
| `TestIAMUserImport_ExistingUser` | Creates via IAM API, imports via driver. Verifies Observed mode. |
| `TestIAMUserDelete_RemovesUser` | Provisions with policies/groups, deletes. Verifies full cleanup. |
| `TestIAMUserReconcile_DetectsGroupDrift` | Adds user to extra group directly. Reconcile removes it. |
| `TestIAMUserGetStatus_ReturnsReady` | Provisions and checks status. |

---

## IAM-User-Specific Design Decisions

### 1. No Credential Management

The driver explicitly excludes login profiles, access keys, MFA devices, and SSH
keys from scope. These are credential artifacts that:
- Should not be stored in Restate state (security risk).
- Should not be managed via declarative templates (rotation lifecycle is different).
- Belong to operational tooling (AWS SSO, credential rotation scripts, security
  automation).

The Delete handler does defensively clean up login profiles and access keys (to
avoid `DeleteConflict` errors), but never creates or manages them.

### 2. Group Membership Managed by User Driver

Group membership is managed as a property of the user, not the group. This is
consistent with the AWS API model (`AddUserToGroup` is user-centric) and avoids
circular dependencies between user and group drivers. The group driver manages the
group resource itself; the user driver manages which groups a user belongs to.

### 3. Path is Mutable (Unlike IAM Roles)

IAM users support path changes via `UpdateUser`, unlike IAM roles where path is
immutable. The driver treats path as a mutable field and converges it during
re-provision and reconciliation.

### 4. Import Defaults to ModeObserved

IAM users can have credentials (access keys, passwords) that are not visible to the
driver. Accidental deletion of an imported user could revoke all access for a human
operator. Import defaults to ModeObserved.

### 5. Comprehensive Delete Cleanup

The Delete handler performs exhaustive cleanup:
1. Group membership removal
2. Managed policy detachment
3. Inline policy deletion
4. Login profile deletion (defensive)
5. Access key deletion (defensive)
6. User deletion

Each cleanup step is in its own `restate.Run` block for journal safety. Failures
in cleanup steps are retriable (Restate retries the handler from the last
successful journal entry).

### 6. Add-Before-Remove for Groups and Policies

When converging group memberships and policy attachments:
1. Add new memberships/attachments first.
2. Remove stale memberships/attachments second.

This prevents a window where the user lacks required permissions during convergence.

---

## Design Decisions (Resolved)

1. **Should the driver manage login profiles?**
   No. Passwords are sensitive credentials. Storing them in Restate state is a
   security risk. AWS SSO or external credential management is preferred.

2. **Should the driver manage access keys?**
   No. Access keys have a rotation lifecycle that doesn't fit declarative
   infrastructure management. Key rotation requires creating a new key, distributing
   it, then deleting the old key — a multi-step operational workflow.

3. **Should the user driver or group driver manage group membership?**
   The user driver. This matches the AWS API model and avoids circular dependencies.
   The group driver manages the group *resource*; membership is a user property.

4. **Should path changes require delete + recreate?**
   No. AWS supports `UpdateUser` with a new path. The driver updates in-place.

5. **Should the Delete handler fail if the user has access keys?**
   No. The Delete handler defensively deletes access keys. The user asked to delete
   the user — leaving orphaned access keys would be worse than cleaning them up.

---

## Checklist

- [ ] **Schema**: `schemas/aws/iam/user.cue` created
- [ ] **Types**: `internal/drivers/iamuser/types.go` created
- [ ] **AWS API**: `internal/drivers/iamuser/aws.go` created
- [ ] **Drift**: `internal/drivers/iamuser/drift.go` created
- [ ] **Driver**: `internal/drivers/iamuser/driver.go` created with all 6 handlers
- [ ] **Adapter**: `internal/core/provider/iamuser_adapter.go` created
- [ ] **Registry**: `internal/core/provider/registry.go` updated
- [ ] **Entry point**: IAMUser driver bound in `cmd/praxis-identity/main.go`
- [ ] **Unit tests (drift)**: `internal/drivers/iamuser/drift_test.go`
- [ ] **Unit tests (aws)**: `internal/drivers/iamuser/aws_test.go`
- [ ] **Unit tests (driver)**: `internal/drivers/iamuser/driver_test.go`
- [ ] **Unit tests (adapter)**: `internal/core/provider/iamuser_adapter_test.go`
- [ ] **Integration tests**: `tests/integration/iamuser_driver_test.go`
- [ ] **Composite describe**: GetUser + ListPolicies + GetPolicy + ListAttached + ListGroups
- [ ] **Pre-deletion cleanup**: Groups, policies, login profile, access keys
- [ ] **Group convergence**: Add-before-remove
- [ ] **Path update**: Mutable via UpdateUser
- [ ] **Import default mode**: `ModeObserved`
- [ ] **Delete mode guard**: Delete handler blocks for ModeObserved (409)
- [ ] **Build passes**: `go build ./...` succeeds
- [ ] **Unit tests pass**: `go test ./internal/drivers/iamuser/... -race`
- [ ] **Integration tests pass**: `go test ./tests/integration/ -run TestIAMUser -tags=integration`
