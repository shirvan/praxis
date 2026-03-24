# IAM Policy Driver — Implementation Plan

> ✅ Implemented
> Target: A Restate Virtual Object driver that manages IAM Policies (customer-managed
> policies), providing full lifecycle management including creation, import, deletion,
> versioning, drift detection, and drift correction for policy documents and tags.
>
> Key scope: `KeyScopeGlobal` — key format is `policyName`, permanent and immutable
> for the lifetime of the Virtual Object. IAM policy names are unique within an AWS
> account (IAM is a global service with no region scoping). The policy ARN is stored
> in state/outputs.

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
16. [IAM-Policy-Specific Design Decisions](#iam-policy-specific-design-decisions)
17. [Design Decisions (Resolved)](#design-decisions-resolved)
18. [Checklist](#checklist)

---

## 1. Overview & Scope

The IAM Policy driver manages the lifecycle of **customer-managed IAM policies**
only. It creates, imports, updates (via versioning), and deletes IAM policies.

**Out of scope**:

- **AWS-managed policies** (e.g., `arn:aws:iam::aws:policy/ReadOnlyAccess`) — these
  are read-only AWS resources that cannot be created, modified, or deleted.
- **Inline policies** — managed by the IAM Role/User/Group drivers as properties of
  those resources.
- **Policy attachment/detachment** — managed by the Role/User/Group drivers. This
  driver manages the policy *resource* itself, not its attachment to principals.

IAM policies are the core permission primitive. They are referenced by IAM roles,
users, and groups via `managedPolicyArns`. In compound templates, policy resources
are dependencies of role resources — the DAG ensures policy creation before role
attachment.

### Driver Contract

| Handler | Context | Purpose |
|---|---|---|
| `Provision` | `ObjectContext` (exclusive) | Create or update an IAM policy |
| `Import` | `ObjectContext` (exclusive) | Adopt an existing customer-managed policy |
| `Delete` | `ObjectContext` (exclusive) | Delete a policy (detaches from all principals first) |
| `Reconcile` | `ObjectContext` (exclusive) | Detect/correct drift (report-only for Observed mode) |
| `GetStatus` | `ObjectSharedContext` (shared) | Return current status |
| `GetOutputs` | `ObjectSharedContext` (shared) | Return IAM policy outputs |

### Mutable vs Immutable Attributes

| Attribute | Mutability | Notes |
|---|---|---|
| `policyName` | Immutable | Part of the Virtual Object key; cannot change after creation |
| `path` | Immutable | IAM path prefix; cannot change after creation |
| `policyDocument` | Mutable | Updated via `CreatePolicyVersion` (replaces default version) |
| `description` | Immutable | AWS does not support updating policy description |
| `tags` | Mutable | Full replace via `TagPolicy` / `UntagPolicy` |

### Downstream Consumers

```text
${resources.my-policy.outputs.arn}          → IAMRole spec.managedPolicyArns
${resources.my-policy.outputs.policyId}     → Audit references
${resources.my-policy.outputs.policyName}   → CLI references
```

---

## 2. Key Strategy

### Key Scope: `KeyScopeGlobal`

IAM is a global AWS service. Policy names are unique within an account (scoped by
path). The key is `policyName` — no region prefix needed. Matches the IAM Role and
S3 key strategy.

### BuildKey vs BuildImportKey

- **`BuildKey(resourceDoc)`**: Extracts `metadata.name` from the resource document.
  Returns the policy name directly.

- **`BuildImportKey(region, resourceID)`**: Returns `resourceID`. For IAM policies,
  `resourceID` is the policy name (not the ARN). Import and template management
  produce the **same key** for the same policy.

### No Ownership Tags

IAM policy names are unique within an account. `CreatePolicy` returns
`EntityAlreadyExists` if the name already exists. This natural conflict signal
eliminates the need for ownership tags.

---

## 3. File Inventory

```text
✦ schemas/aws/iam/policy.cue                         — CUE schema for IAMPolicy resource
✦ internal/drivers/iampolicy/types.go                 — Spec, Outputs, ObservedState, State structs
✦ internal/drivers/iampolicy/aws.go                   — IAMPolicyAPI interface + realIAMPolicyAPI
✦ internal/drivers/iampolicy/drift.go                 — HasDrift(), ComputeFieldDiffs()
✦ internal/drivers/iampolicy/driver.go                — IAMPolicyDriver Virtual Object
✦ internal/drivers/iampolicy/driver_test.go           — Unit tests for driver
✦ internal/drivers/iampolicy/aws_test.go              — Unit tests for error classification
✦ internal/drivers/iampolicy/drift_test.go            — Unit tests for drift detection
✦ internal/core/provider/iampolicy_adapter.go         — IAMPolicyAdapter implementing provider.Adapter
✦ internal/core/provider/iampolicy_adapter_test.go    — Unit tests for adapter
✦ tests/integration/iampolicy_driver_test.go          — Integration tests
✎ cmd/praxis-identity/main.go                             — Add IAMPolicy driver .Bind()
✎ internal/core/provider/registry.go                  — Add NewIAMPolicyAdapter to NewRegistry()
```

---

## Step 1 — CUE Schema

**File**: `schemas/aws/iam/policy.cue`

```cue
package iam

#IAMPolicy: {
    apiVersion: "praxis.io/v1"
    kind:       "IAMPolicy"

    metadata: {
        // name is used as the IAM policy name in AWS.
        name: string & =~"^[a-zA-Z0-9+=,.@_-]{1,128}$"
        labels: [string]: string
    }

    spec: {
        // path is the IAM path prefix (e.g., "/app/").
        // Defaults to "/" if omitted.
        path: string | *"/"

        // policyDocument is the JSON IAM policy document defining permissions.
        policyDocument: string

        // description is a human-readable description of the policy.
        // Immutable after creation.
        description?: string

        // tags applied to the IAM policy.
        tags: [string]: string
    }

    outputs?: {
        arn:        string
        policyId:   string
        policyName: string
    }
}
```

### Key Design Decisions

- **`policyDocument` as string**: The policy document is a JSON string, matching the
  AWS API format. Complex policy grammar validation is delegated to AWS.

- **`description` is immutable**: AWS does not provide an API to update
  the description of a managed policy after creation. The CUE schema marks it as
  optional; drift detection reports it as "(immutable, ignored)" if it differs.

---

## Step 2 — AWS Client Factory

**File**: `internal/infra/awsclient/client.go` — Uses same IAM client as IAM Role.

No additional changes needed if the IAM Role driver has already been implemented.

---

## Step 3 — Driver Types

**File**: `internal/drivers/iampolicy/types.go`

```go
package iampolicy

import "github.com/shirvan/praxis/pkg/types"

const ServiceName = "IAMPolicy"

type IAMPolicySpec struct {
    Account        string            `json:"account,omitempty"`
    Path           string            `json:"path"`
    PolicyName     string            `json:"policyName"`
    PolicyDocument string            `json:"policyDocument"`
    Description    string            `json:"description,omitempty"`
    Tags           map[string]string `json:"tags,omitempty"`
}

type IAMPolicyOutputs struct {
    Arn        string `json:"arn"`
    PolicyId   string `json:"policyId"`
    PolicyName string `json:"policyName"`
}

type ObservedState struct {
    Arn              string            `json:"arn"`
    PolicyId         string            `json:"policyId"`
    PolicyName       string            `json:"policyName"`
    Path             string            `json:"path"`
    Description      string            `json:"description"`
    PolicyDocument   string            `json:"policyDocument"`
    DefaultVersionId string            `json:"defaultVersionId"`
    AttachmentCount  int32             `json:"attachmentCount"`
    Tags             map[string]string `json:"tags"`
    CreateDate       string            `json:"createDate"`
    UpdateDate       string            `json:"updateDate"`
}

type IAMPolicyState struct {
    Desired            IAMPolicySpec        `json:"desired"`
    Observed           ObservedState        `json:"observed"`
    Outputs            IAMPolicyOutputs     `json:"outputs"`
    Status             types.ResourceStatus `json:"status"`
    Mode               types.Mode           `json:"mode"`
    Error              string               `json:"error,omitempty"`
    Generation         int64                `json:"generation"`
    LastReconcile      string               `json:"lastReconcile,omitempty"`
    ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
```

### AttachmentCount in ObservedState

The observed state includes `AttachmentCount` from `GetPolicy`. This is used by the
Delete handler to warn about attached principals and by the plan renderer to show
usage information.

---

## Step 4 — AWS API Abstraction Layer

**File**: `internal/drivers/iampolicy/aws.go`

### IAMPolicyAPI Interface

```go
type IAMPolicyAPI interface {
    // CreatePolicy creates a new customer-managed policy.
    CreatePolicy(ctx context.Context, spec IAMPolicySpec) (arn, policyId string, err error)

    // DescribePolicy returns the observed state of a policy by ARN.
    DescribePolicy(ctx context.Context, policyArn string) (ObservedState, error)

    // DescribePolicyByName finds a policy by name and path, returns observed state.
    DescribePolicyByName(ctx context.Context, policyName, path string) (ObservedState, error)

    // DeletePolicy deletes a policy by ARN (must have no attachments).
    DeletePolicy(ctx context.Context, policyArn string) error

    // CreatePolicyVersion creates a new version of the policy document
    // and sets it as the default. Cleans up oldest non-default version
    // if the 5-version limit would be exceeded.
    CreatePolicyVersion(ctx context.Context, policyArn, policyDocument string) error

    // GetPolicyDocument returns the policy document for the default version.
    GetPolicyDocument(ctx context.Context, policyArn, versionId string) (string, error)

    // ListPolicyVersions returns all version IDs and which is default.
    ListPolicyVersions(ctx context.Context, policyArn string) ([]PolicyVersionInfo, error)

    // DeletePolicyVersion deletes a non-default policy version.
    DeletePolicyVersion(ctx context.Context, policyArn, versionId string) error

    // DetachAllPrincipals detaches the policy from all users, groups, and roles.
    DetachAllPrincipals(ctx context.Context, policyArn string) error

    // UpdateTags replaces all user tags on the policy.
    UpdateTags(ctx context.Context, policyArn string, tags map[string]string) error
}

type PolicyVersionInfo struct {
    VersionId        string
    IsDefaultVersion bool
    CreateDate       string
}
```

### realIAMPolicyAPI Implementation

```go
type realIAMPolicyAPI struct {
    client  *iam.Client
    limiter *ratelimit.Limiter
}

func NewIAMPolicyAPI(client *iam.Client) IAMPolicyAPI {
    return &realIAMPolicyAPI{
        client:  client,
        limiter: ratelimit.New("iam", 15, 8),
    }
}
```

### Key Implementation Details

#### `CreatePolicy`

```go
func (r *realIAMPolicyAPI) CreatePolicy(ctx context.Context, spec IAMPolicySpec) (string, string, error) {
    input := &iam.CreatePolicyInput{
        PolicyName:     aws.String(spec.PolicyName),
        Path:           aws.String(spec.Path),
        PolicyDocument: aws.String(spec.PolicyDocument),
    }
    if spec.Description != "" {
        input.Description = aws.String(spec.Description)
    }
    if len(spec.Tags) > 0 {
        input.Tags = toIAMTags(spec.Tags)
    }

    out, err := r.client.CreatePolicy(ctx, input)
    if err != nil {
        return "", "", err
    }
    return aws.ToString(out.Policy.Arn),
           aws.ToString(out.Policy.PolicyId),
           nil
}
```

#### `DescribePolicy`

Composite describe: `GetPolicy` + `GetPolicyVersion` (for the policy document):

```go
func (r *realIAMPolicyAPI) DescribePolicy(ctx context.Context, policyArn string) (ObservedState, error) {
    // 1. GetPolicy — base policy attributes
    policyOut, err := r.client.GetPolicy(ctx, &iam.GetPolicyInput{
        PolicyArn: aws.String(policyArn),
    })
    if err != nil {
        return ObservedState{}, err
    }
    pol := policyOut.Policy

    // 2. GetPolicyVersion — default version document
    doc, err := r.GetPolicyDocument(ctx, policyArn, aws.ToString(pol.DefaultVersionId))
    if err != nil {
        return ObservedState{}, err
    }

    obs := ObservedState{
        Arn:              aws.ToString(pol.Arn),
        PolicyId:         aws.ToString(pol.PolicyId),
        PolicyName:       aws.ToString(pol.PolicyName),
        Path:             aws.ToString(pol.Path),
        Description:      aws.ToString(pol.Description),
        PolicyDocument:   doc,
        DefaultVersionId: aws.ToString(pol.DefaultVersionId),
        AttachmentCount:  aws.ToInt32(pol.AttachmentCount),
        Tags:             fromIAMTags(pol.Tags),
    }
    if pol.CreateDate != nil {
        obs.CreateDate = pol.CreateDate.Format(time.RFC3339)
    }
    if pol.UpdateDate != nil {
        obs.UpdateDate = pol.UpdateDate.Format(time.RFC3339)
    }
    return obs, nil
}
```

#### `DescribePolicyByName`

For Plan: find a policy by name to determine if it exists:

```go
func (r *realIAMPolicyAPI) DescribePolicyByName(ctx context.Context, policyName, path string) (ObservedState, error) {
    // ListPolicies with PathPrefix to find by name
    paginator := iam.NewListPoliciesPaginator(r.client, &iam.ListPoliciesInput{
        PathPrefix: aws.String(path),
        Scope:      iamtypes.PolicyScopeTypeLocal, // customer-managed only
    })
    for paginator.HasMorePages() {
        page, err := paginator.NextPage(ctx)
        if err != nil {
            return ObservedState{}, err
        }
        for _, pol := range page.Policies {
            if aws.ToString(pol.PolicyName) == policyName {
                return r.DescribePolicy(ctx, aws.ToString(pol.Arn))
            }
        }
    }
    return ObservedState{}, fmt.Errorf("policy %q not found at path %q: %w",
        policyName, path, errNotFound)
}
```

#### `CreatePolicyVersion` — Version Management

IAM policies support up to 5 versions. When updating the policy document, the driver
creates a new version and sets it as default. If 5 versions already exist, the oldest
non-default version is deleted first.

```go
func (r *realIAMPolicyAPI) CreatePolicyVersion(ctx context.Context, policyArn, policyDocument string) error {
    // Check version count — max 5 allowed
    versions, err := r.ListPolicyVersions(ctx, policyArn)
    if err != nil {
        return err
    }
    if len(versions) >= 5 {
        // Delete oldest non-default version
        oldest := findOldestNonDefault(versions)
        if oldest != "" {
            if err := r.DeletePolicyVersion(ctx, policyArn, oldest); err != nil {
                return err
            }
        }
    }

    _, err = r.client.CreatePolicyVersion(ctx, &iam.CreatePolicyVersionInput{
        PolicyArn:      aws.String(policyArn),
        PolicyDocument: aws.String(policyDocument),
        SetAsDefault:   true,
    })
    return err
}
```

#### `DetachAllPrincipals` — Pre-Deletion Cleanup

```go
func (r *realIAMPolicyAPI) DetachAllPrincipals(ctx context.Context, policyArn string) error {
    // 1. List entities for policy
    entOut, err := r.client.ListEntitiesForPolicy(ctx, &iam.ListEntitiesForPolicyInput{
        PolicyArn: aws.String(policyArn),
    })
    if err != nil {
        return err
    }

    // 2. Detach from all roles
    for _, role := range entOut.PolicyRoles {
        _, err := r.client.DetachRolePolicy(ctx, &iam.DetachRolePolicyInput{
            PolicyArn: aws.String(policyArn),
            RoleName:  role.RoleName,
        })
        if err != nil {
            return err
        }
    }

    // 3. Detach from all users
    for _, user := range entOut.PolicyUsers {
        _, err := r.client.DetachUserPolicy(ctx, &iam.DetachUserPolicyInput{
            PolicyArn: aws.String(policyArn),
            UserName:  user.UserName,
        })
        if err != nil {
            return err
        }
    }

    // 4. Detach from all groups
    for _, group := range entOut.PolicyGroups {
        _, err := r.client.DetachGroupPolicy(ctx, &iam.DetachGroupPolicyInput{
            PolicyArn: aws.String(policyArn),
            GroupName: group.GroupName,
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

func IsMalformedPolicy(err error) bool {
    if err == nil {
        return false
    }
    var apiErr smithy.APIError
    if errors.As(err, &apiErr) {
        return apiErr.ErrorCode() == "MalformedPolicyDocument"
    }
    return strings.Contains(err.Error(), "MalformedPolicyDocument")
}

func IsVersionLimitExceeded(err error) bool {
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

**File**: `internal/drivers/iampolicy/drift.go`

IAM policies have relatively simple drift: the policy document and tags.

### Core Functions

**`HasDrift(desired IAMPolicySpec, observed ObservedState) bool`**

```go
func HasDrift(desired IAMPolicySpec, observed ObservedState) bool {
    if !policyDocumentsEqual(desired.PolicyDocument, observed.PolicyDocument) {
        return true
    }
    return !tagsMatch(desired.Tags, observed.Tags)
}
```

**`ComputeFieldDiffs(desired IAMPolicySpec, observed ObservedState) []FieldDiffEntry`**

- Immutable fields: `path`, `description` — reported with "(immutable, ignored)".
- Mutable: `policyDocument` (canonicalized comparison), `tags`.

### Policy Document Comparison

Uses the same `policyDocumentsEqual` / `canonicalizePolicyDoc` pattern as the IAM
Role driver. URL-decodes and canonicalizes JSON before comparison.

```go
func policyDocumentsEqual(a, b string) bool {
    return canonicalizePolicyDoc(a) == canonicalizePolicyDoc(b)
}

func canonicalizePolicyDoc(doc string) string {
    decoded, err := url.QueryUnescape(doc)
    if err != nil {
        return doc
    }
    var parsed any
    if err := json.Unmarshal([]byte(decoded), &parsed); err != nil {
        return decoded
    }
    canonical, err := json.Marshal(parsed)
    if err != nil {
        return decoded
    }
    return string(canonical)
}
```

> **Note**: The `canonicalizePolicyDoc` utility is duplicated between `iamrole` and
> `iampolicy`. If a third IAM driver needs it, extract to a shared `internal/drivers/iamcommon/`
> package. Two occurrences does not warrant an abstraction.

---

## Step 6 — Driver Implementation

**File**: `internal/drivers/iampolicy/driver.go`

### Service Registration

```go
const ServiceName = "IAMPolicy"
```

### Constructor Pattern

```go
func NewIAMPolicyDriver(accounts *auth.Registry) *IAMPolicyDriver
func NewIAMPolicyDriverWithFactory(accounts *auth.Registry, factory func(aws.Config) IAMPolicyAPI) *IAMPolicyDriver
```

### Provision Handler

1. **Input validation**: `policyName` and `policyDocument` must be non-empty.
   Returns `TerminalError(400)` on failure.

2. **Load current state**: Reads `IAMPolicyState` from Restate K/V. Sets status to
   `Provisioning`, increments generation.

3. **Re-provision check**: If `state.Outputs.Arn` is non-empty, describes the policy.
   If it's been deleted externally (404), clears ARN and falls through to creation.

4. **Create policy** (new): Calls `api.CreatePolicy`. Classifies errors:
   - `IsAlreadyExists` → `TerminalError(409)`
   - `IsMalformedPolicy` → `TerminalError(400)`

5. **Update policy document** (re-provision): If the policy document changed,
   creates a new version via `api.CreatePolicyVersion`. Automatically handles
   the 5-version limit by deleting the oldest non-default version.

6. **Update tags**: If tags changed, calls `api.UpdateTags`.

7. **Describe final state**: Calls `api.DescribePolicy`.

8. **Commit state**: Sets status to `Ready`, saves atomically, schedules reconcile.

### Import Handler

1. Describes the policy by `ref.ResourceID` (policy name). Must find the policy ARN
   first via `DescribePolicyByName`.
2. Synthesizes `IAMPolicySpec` from observed state.
3. Sets mode to `ModeObserved`.
4. Schedules reconciliation.

### Delete Handler

IAM policy deletion requires removing all versions and detaching from all principals:

1. Sets status to `Deleting`.
2. **Detach from all principals**: Calls `api.DetachAllPrincipals` — lists all
   attached roles, users, groups and detaches from each.
3. **Delete all non-default versions**: Lists versions, deletes each non-default one.
4. **Delete policy**: Calls `api.DeletePolicy`.
5. Error classification:
   - `IsDeleteConflict` → `TerminalError(409)` — still has attachments.
   - `IsNotFound` → silent success.
6. Sets status to `StatusDeleted`.

### Reconcile Handler

Standard pattern:

1. Describe current state.
2. **Managed + drift**: Update policy document via new version, update tags.
3. **Observed + drift**: Report only.
4. Re-schedule.

### GetStatus / GetOutputs

Standard shared handlers.

---

## Step 7 — Provider Adapter

**File**: `internal/core/provider/iampolicy_adapter.go`

```go
type IAMPolicyAdapter struct {
    auth              *auth.Registry
    staticPlanningAPI iampolicy.IAMPolicyAPI
    apiFactory        func(aws.Config) iampolicy.IAMPolicyAPI
}
```

### Methods

**`Scope() KeyScope`** → `KeyScopeGlobal`

**`Kind() string`** → `"IAMPolicy"`

**`ServiceName() string`** → `"IAMPolicy"`

**`BuildKey(resourceDoc json.RawMessage) (string, error)`**:
Decodes resource document, extracts `metadata.name`. Returns policy name.

**`BuildImportKey(region, resourceID string) (string, error)`**:
Returns `resourceID` (policy name). Same key as `BuildKey`.

**`Plan(ctx, key, account, desiredSpec)`**:
Calls `api.DescribePolicyByName(policyName, path)`. If not found → `OpCreate`.
If found → `ComputeFieldDiffs()`. If no diffs → `OpNoOp`. If diffs → `OpUpdate`.

---

## Step 8 — Registry Integration

Add `NewIAMPolicyAdapterWithRegistry(accounts)` to `NewRegistry()`.

---

## Step 9 — Identity Driver Pack Entry Point

**File**: `cmd/praxis-identity/main.go` (modified)

Add `.Bind(restate.Reflect(iampolicy.NewIAMPolicyDriver(cfg.Auth())))`.

---

## Step 10 — Docker Compose & Justfile

No Docker Compose changes — IAMPolicy joins the existing `praxis-identity` service.

Add justfile targets:

| Target | Command |
|---|---|
| `test-iampolicy` | `go test ./internal/drivers/iampolicy/... -v -count=1 -race` |
| `test-iampolicy-integration` | `go test ./tests/integration/ -run TestIAMPolicy -v -count=1 -tags=integration -timeout=5m` |

---

## Step 11 — Unit Tests

### `internal/drivers/iampolicy/drift_test.go`

| Test | Purpose |
|---|---|
| `TestHasDrift_NoDrift` | Matching state → no drift |
| `TestHasDrift_PolicyDocumentDrift` | Document change → drift |
| `TestHasDrift_PolicyDocumentWhitespace` | Same doc, different whitespace → no drift |
| `TestHasDrift_PolicyDocumentURLEncoded` | URL-encoded vs decoded → no drift |
| `TestHasDrift_TagDrift` | Tag change → drift |
| `TestComputeFieldDiffs_ImmutablePath` | Reports path as "(immutable, ignored)" |
| `TestComputeFieldDiffs_ImmutableDescription` | Reports description as "(immutable, ignored)" |
| `TestComputeFieldDiffs_DocumentChange` | Full document diff |
| `TestCanonicalizePolicyDoc` | Whitespace + URL encoding normalization |

### `internal/drivers/iampolicy/aws_test.go`

| Test | Purpose |
|---|---|
| `TestIsNotFound_NoSuchEntity` | True for NoSuchEntity |
| `TestIsAlreadyExists_True` | True for EntityAlreadyExists |
| `TestIsDeleteConflict_True` | True for DeleteConflict |
| `TestIsMalformedPolicy_True` | True for MalformedPolicyDocument |
| `TestIsVersionLimitExceeded_True` | True for LimitExceeded |
| `TestIsNotFound_WrappedRestateError` | String fallback |

### `internal/drivers/iampolicy/driver_test.go`

| Test | Purpose |
|---|---|
| `TestSpecFromObserved_RoundTrip` | Observed → spec preserves all fields |
| `TestServiceName` | Returns "IAMPolicy" |
| `TestOutputsFromObserved` | Correct output mapping |
| `TestFindOldestNonDefault` | Correct version selection for cleanup |

### `internal/core/provider/iampolicy_adapter_test.go`

| Test | Purpose |
|---|---|
| `TestIAMPolicyAdapter_BuildKey` | Returns policy name |
| `TestIAMPolicyAdapter_BuildImportKey` | Returns policy name (same) |
| `TestIAMPolicyAdapter_Kind` | Returns "IAMPolicy" |
| `TestIAMPolicyAdapter_Scope` | Returns `KeyScopeGlobal` |
| `TestIAMPolicyAdapter_NormalizeOutputs` | Converts struct to map |

---

## Step 12 — Integration Tests

**File**: `tests/integration/iampolicy_driver_test.go`

| Test | Description |
|---|---|
| `TestIAMPolicyProvision_CreatesPolicy` | Creates a policy with document and tags. Verifies via `GetPolicy`. |
| `TestIAMPolicyProvision_Idempotent` | Provisions same spec twice. Same ARN returned. |
| `TestIAMPolicyProvision_UpdateDocument` | Re-provisions with new document. Verifies new version created and is default. |
| `TestIAMPolicyProvision_VersionRotation` | Creates 5 versions, then updates again. Verifies oldest non-default version was deleted. |
| `TestIAMPolicyImport_ExistingPolicy` | Creates via IAM API, imports via driver. Verifies Observed mode. |
| `TestIAMPolicyDelete_RemovesPolicy` | Provisions, attaches to a role via IAM API, deletes. Verifies cleanup of attachments, versions, and policy. |
| `TestIAMPolicyReconcile_DetectsDrift` | Provisions, updates document directly via IAM API. Triggers reconcile, verifies drift detected and corrected. |
| `TestIAMPolicyGetStatus_ReturnsReady` | Provisions and checks status. |

---

## IAM-Policy-Specific Design Decisions

### 1. Policy Document Versioning

IAM policies support up to 5 versions. When the policy document changes, the driver
creates a new version set as default, rather than deleting and recreating the policy.
This preserves the policy ARN (which may be referenced by roles, users, and groups)
and avoids a brief period where the policy doesn't exist.

The driver automatically manages the 5-version limit by deleting the oldest
non-default version before creating a new one. This is transparent to the user.

### 2. Pre-Deletion Full Cleanup

The Delete handler performs comprehensive cleanup before deleting the policy:

1. Detach from all roles, users, and groups (via `ListEntitiesForPolicy`).
2. Delete all non-default policy versions.
3. Delete the policy itself.

This ensures that deleting a policy never fails with `DeleteConflict` due to
remaining attachments. The user expectation is "delete my policy" — they shouldn't
need to manually detach it from all principals first.

### 3. Import Defaults to ModeObserved

IAM policies control permissions across the AWS account. Accidental modification
(e.g., drift correction that adds missing deny statements) could cause service
disruptions. Import defaults to ModeObserved for safety.

### 4. Description is Immutable

AWS does not provide an API to update a managed policy's description. If the desired
description differs from observed, `ComputeFieldDiffs` reports it as
"(immutable, ignored)" and the driver takes no corrective action.

### 5. DescribePolicyByName Uses ListPolicies

Unlike `GetPolicy` (which requires the ARN), finding a policy by name requires
listing policies with a path prefix filter and scanning results. This is acceptable
for Plan (which runs at most once per apply) but would be expensive for Reconcile.
The Reconcile handler uses the stored ARN instead.

### 6. Shared Rate Limiter with IAM Role

The IAM Policy driver uses the same rate limiter service name (`"iam"`) as the IAM
Role driver. When both drivers are in the same process (praxis-identity pack), the rate
limiter instance is per-driver (each has its own), but the service name allows
future coordination via the Central Rate Limit Advisor.

---

## Design Decisions (Resolved)

1. **Should policy document updates use CreatePolicyVersion or delete+recreate?**
   `CreatePolicyVersion`. This preserves the ARN and existing attachments. Delete
   and recreate would break all references to the policy ARN.

2. **Should the driver manage the 5-version limit automatically?**
   Yes. The driver deletes the oldest non-default version when the limit would be
   exceeded. This is transparent and prevents `LimitExceeded` errors on update.

3. **Should the driver track individual policy versions?**
   No. The driver manages a single "current" policy document. Version history is an
   implementation detail of how AWS handles policy updates. The driver always creates
   a new version set as default and does not expose version management to the user.

4. **Should the driver refuse to delete a policy with attachments?**
   No. The driver automatically detaches from all principals before deletion,
   matching the role driver's pre-cleanup pattern. The user's intent is to remove the
   policy — requiring manual detachment is poor UX.

5. **Should imported policies capture the full version history?**
   No. Import captures the current default version's document. Historical versions
   are not relevant for drift detection or state management.

---

## Checklist

- [ ] **Schema**: `schemas/aws/iam/policy.cue` created
- [ ] **Types**: `internal/drivers/iampolicy/types.go` created
- [ ] **AWS API**: `internal/drivers/iampolicy/aws.go` created
- [ ] **Drift**: `internal/drivers/iampolicy/drift.go` created
- [ ] **Driver**: `internal/drivers/iampolicy/driver.go` created with all 6 handlers
- [ ] **Adapter**: `internal/core/provider/iampolicy_adapter.go` created
- [ ] **Registry**: `internal/core/provider/registry.go` updated
- [ ] **Entry point**: IAMPolicy driver bound in `cmd/praxis-identity/main.go`
- [ ] **Unit tests (drift)**: `internal/drivers/iampolicy/drift_test.go`
- [ ] **Unit tests (aws)**: `internal/drivers/iampolicy/aws_test.go`
- [ ] **Unit tests (driver)**: `internal/drivers/iampolicy/driver_test.go`
- [ ] **Unit tests (adapter)**: `internal/core/provider/iampolicy_adapter_test.go`
- [ ] **Integration tests**: `tests/integration/iampolicy_driver_test.go`
- [ ] **Policy doc canonicalization**: URL-decode + JSON normalize
- [ ] **Version rotation**: Auto-delete oldest non-default at 5-version limit
- [ ] **Pre-deletion cleanup**: Detach all principals, delete all versions
- [ ] **Import default mode**: `ModeObserved`
- [ ] **Delete mode guard**: Delete handler blocks for ModeObserved (409)
- [ ] **Build passes**: `go build ./...` succeeds
- [ ] **Unit tests pass**: `go test ./internal/drivers/iampolicy/... -race`
- [ ] **Integration tests pass**: `go test ./tests/integration/ -run TestIAMPolicy -tags=integration`
