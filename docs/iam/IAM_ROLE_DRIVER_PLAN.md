# IAM Role Driver — Implementation Plan

> NYI
> Target: A Restate Virtual Object driver that manages IAM Roles, providing full
> lifecycle management including creation, import, deletion, drift detection, and
> drift correction for role properties, assume-role policy documents, inline policies,
> managed policy attachments, and tags.
>
> Key scope: `KeyScopeGlobal` — key format is `roleName`, permanent and immutable
> for the lifetime of the Virtual Object. IAM role names are globally unique within
> an AWS account (IAM is a global service with no region scoping).

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
12. [Step 9 — IAM Driver Pack Entry Point](#step-9--iam-driver-pack-entry-point)
13. [Step 10 — Docker Compose & Justfile](#step-10--docker-compose--justfile)
14. [Step 11 — Unit Tests](#step-11--unit-tests)
15. [Step 12 — Integration Tests](#step-12--integration-tests)
16. [IAM-Role-Specific Design Decisions](#iam-role-specific-design-decisions)
17. [Design Decisions (Resolved)](#design-decisions-resolved)
18. [Checklist](#checklist)

---

## 1. Overview & Scope

The IAM Role driver manages the lifecycle of IAM **roles** only. It creates, imports,
updates, and deletes IAM roles along with their trust policies (assume-role policy
document), inline policies, managed policy attachments, and tags.

IAM roles are foundational — almost every other AWS resource depends on IAM roles for
permissions. In compound templates, IAM roles are typically a dependency of EC2
instances (via instance profiles), Lambda functions, ECS tasks, and other compute
resources. The DAG ensures role creation before dependent resources.

**Out of scope**: IAM policies (separate driver), IAM users (separate driver), IAM
groups (separate driver), instance profiles (separate driver). Each is a distinct
resource type with its own lifecycle and Virtual Object key space.

### Driver Contract

| Handler | Context | Purpose |
|---|---|---|
| `Provision` | `ObjectContext` (exclusive) | Create or converge an IAM role |
| `Import` | `ObjectContext` (exclusive) | Adopt an existing IAM role |
| `Delete` | `ObjectContext` (exclusive) | Remove an IAM role (detaches policies first) |
| `Reconcile` | `ObjectContext` (exclusive) | Detect/correct drift (report-only for Observed mode) |
| `GetStatus` | `ObjectSharedContext` (shared) | Return current status |
| `GetOutputs` | `ObjectSharedContext` (shared) | Return IAM role outputs |

### Mutable vs Immutable Attributes

| Attribute | Mutability | Notes |
|---|---|---|
| `roleName` | Immutable | Part of the Virtual Object key; cannot change after creation |
| `path` | Immutable | IAM path prefix; cannot change after creation (requires delete + recreate) |
| `assumeRolePolicyDocument` | Mutable | Updated via `UpdateAssumeRolePolicy` |
| `description` | Mutable | Updated via `UpdateRole` |
| `maxSessionDuration` | Mutable | Updated via `UpdateRole` |
| `permissionsBoundary` | Mutable | Updated via `PutRolePermissionsBoundary` / `DeleteRolePermissionsBoundary` |
| `inlinePolicies` | Mutable | Updated via `PutRolePolicy` / `DeleteRolePolicy` |
| `managedPolicyArns` | Mutable | Updated via `AttachRolePolicy` / `DetachRolePolicy` |
| `tags` | Mutable | Full replace via `TagRole` / `UntagRole` |

### Downstream Consumers

```
${resources.my-role.outputs.arn}              → EC2 instance profile, Lambda function role
${resources.my-role.outputs.roleId}           → Policy conditions, audit references
${resources.my-role.outputs.roleName}         → CLI references, CloudWatch log groups
```

---

## 2. Key Strategy

### Key Scope: `KeyScopeGlobal`

IAM is a global service — role names are unique within an AWS account regardless of
region. This matches the S3 pattern (`KeyScopeGlobal`, key = bucket name).

```
roleName
```

### BuildKey vs BuildImportKey

- **`BuildKey(resourceDoc)`**: Extracts `metadata.name` from the resource document.
  The `metadata.name` is used as the IAM role name. Returns the role name directly.

- **`BuildImportKey(region, resourceID)`**: Returns `resourceID` as-is. The
  `resourceID` is the IAM role name (e.g., `MyAppRole`). Since role names are
  globally unique within an account, import and template management produce the
  **same key** for the same role — matching the S3 and KeyPair patterns.

### BuildImportKey Produces the Same Key as BuildKey

This is the same pattern as S3: the AWS resource identifier (role name) is the
same as the Praxis logical name. Import and template management converge on the same
Virtual Object. This is correct because:

- IAM role names are unique within an account (AWS-enforced).
- `CreateRole` returns `EntityAlreadyExists` if the role name already exists.
- Importing a role by name should produce the same VO as managing it via a template
  with the same name — preventing dual-control issues.

### No Ownership Tags

IAM roles use AWS-enforced unique names within an account. `CreateRole` returns
`EntityAlreadyExists` error if the name already exists. This natural conflict signal
eliminates the need for `praxis:managed-key` ownership tags. The duplicate error
maps to a terminal 409 in the Provision handler.

---

## 3. File Inventory

```text
✦ schemas/aws/iam/role.cue                        — CUE schema for IAMRole resource
✦ internal/drivers/iamrole/types.go                — Spec, Outputs, ObservedState, State structs
✦ internal/drivers/iamrole/aws.go                  — IAMRoleAPI interface + realIAMRoleAPI
✦ internal/drivers/iamrole/drift.go                — HasDrift(), ComputeFieldDiffs()
✦ internal/drivers/iamrole/driver.go               — IAMRoleDriver Virtual Object
✦ internal/drivers/iamrole/driver_test.go          — Unit tests for driver (mocked AWS)
✦ internal/drivers/iamrole/aws_test.go             — Unit tests for error classification
✦ internal/drivers/iamrole/drift_test.go           — Unit tests for drift detection
✦ internal/core/provider/iamrole_adapter.go        — IAMRoleAdapter implementing provider.Adapter
✦ internal/core/provider/iamrole_adapter_test.go   — Unit tests for adapter
✦ tests/integration/iamrole_driver_test.go         — Integration tests
✦ cmd/praxis-iam/main.go                           — IAM driver pack entry point (NEW pack)
✦ cmd/praxis-iam/Dockerfile                        — Multi-stage Docker build
✎ internal/core/provider/registry.go               — Add NewIAMRoleAdapter to NewRegistry()
✎ docker-compose.yaml                              — Add praxis-iam service on port 9085
✎ justfile                                         — Add IAM build/test/register targets
```

> **Note**: IAM drivers live in a new `praxis-iam` driver pack. IAM is foundational
> and cross-cutting — roles, policies, users, and groups are consumed by compute,
> network, and storage resources. A dedicated pack keeps the dependency graph clean
> and allows IAM to be scaled/deployed independently.

---

## Step 1 — CUE Schema

**File**: `schemas/aws/iam/role.cue`

```cue
package iam

#IAMRole: {
    apiVersion: "praxis.io/v1"
    kind:       "IAMRole"

    metadata: {
        // name is used as the IAM role name in AWS.
        // Must match IAM naming rules: alphanumeric plus +=,.@_-
        name: string & =~"^[a-zA-Z0-9+=,.@_-]{1,64}$"
        labels: [string]: string
    }

    spec: {
        // path is the IAM path prefix (e.g., "/app/", "/service-role/").
        // Defaults to "/" if omitted.
        path: string | *"/"

        // assumeRolePolicyDocument is the trust policy (JSON string).
        // Defines which principals can assume this role.
        assumeRolePolicyDocument: string

        // description is a human-readable description of the role.
        description?: string

        // maxSessionDuration is the maximum session duration in seconds (3600-43200).
        maxSessionDuration: int & >=3600 & <=43200 | *3600

        // permissionsBoundary is the ARN of a managed policy used as the
        // permissions boundary for the role.
        permissionsBoundary?: string

        // inlinePolicies are policy documents embedded directly in the role.
        // Key is the policy name, value is the JSON policy document.
        inlinePolicies: [string]: string

        // managedPolicyArns is a list of managed policy ARNs to attach.
        managedPolicyArns: [...string] | *[]

        // tags applied to the IAM role.
        tags: [string]: string
    }

    outputs?: {
        arn:      string
        roleId:   string
        roleName: string
    }
}
```

### Key Design Decisions

- **`assumeRolePolicyDocument` as string**: The trust policy is a JSON string, not
  a structured CUE object. This matches the AWS API's expected format and avoids
  a complex nested CUE schema for IAM policy grammar. Semantic validation (valid
  principals, actions, conditions) is left to the AWS API.

- **`inlinePolicies` as map[string]string**: Each entry maps a policy name to a JSON
  policy document string. This supports multiple named inline policies per role
  (common pattern: one inline policy per concern — e.g., "s3-access", "sqs-publish").

- **`managedPolicyArns` as list**: Simple ARN references to separately-managed IAM
  policies. The IAM Policy driver manages the policy lifecycle; the role driver only
  manages the attachment.

- **`path` defaults to `/`**: Most roles use the root path. Path-based organization
  is optional and used primarily for policy-based access control (resource-based
  policies that scope by path prefix).

- **`maxSessionDuration` defaults to 3600**: AWS default is 1 hour. Range is
  1–12 hours (3600–43200 seconds).

---

## Step 2 — AWS Client Factory

**File**: `internal/infra/awsclient/client.go` — **NEEDS NEW IAM CLIENT FACTORY**

IAM operations use the IAM SDK client, not the EC2 SDK client. A new factory
function is needed:

```go
func NewIAMClient(cfg aws.Config) *iam.Client {
    return iam.NewFromConfig(cfg)
}
```

This requires adding `github.com/aws/aws-sdk-go-v2/service/iam` to `go.mod`.

---

## Step 3 — Driver Types

**File**: `internal/drivers/iamrole/types.go`

```go
package iamrole

import "github.com/praxiscloud/praxis/pkg/types"

const ServiceName = "IAMRole"

type IAMRoleSpec struct {
    Account                  string            `json:"account,omitempty"`
    Path                     string            `json:"path"`
    RoleName                 string            `json:"roleName"`
    AssumeRolePolicyDocument string            `json:"assumeRolePolicyDocument"`
    Description              string            `json:"description,omitempty"`
    MaxSessionDuration       int32             `json:"maxSessionDuration"`
    PermissionsBoundary      string            `json:"permissionsBoundary,omitempty"`
    InlinePolicies           map[string]string `json:"inlinePolicies,omitempty"`
    ManagedPolicyArns        []string          `json:"managedPolicyArns,omitempty"`
    Tags                     map[string]string `json:"tags,omitempty"`
}

type IAMRoleOutputs struct {
    Arn      string `json:"arn"`
    RoleId   string `json:"roleId"`
    RoleName string `json:"roleName"`
}

type ObservedState struct {
    Arn                      string            `json:"arn"`
    RoleId                   string            `json:"roleId"`
    RoleName                 string            `json:"roleName"`
    Path                     string            `json:"path"`
    AssumeRolePolicyDocument string            `json:"assumeRolePolicyDocument"`
    Description              string            `json:"description"`
    MaxSessionDuration       int32             `json:"maxSessionDuration"`
    PermissionsBoundary      string            `json:"permissionsBoundary"`
    InlinePolicies           map[string]string `json:"inlinePolicies"`
    ManagedPolicyArns        []string          `json:"managedPolicyArns"`
    Tags                     map[string]string `json:"tags"`
    CreateDate               string            `json:"createDate"`
}

type IAMRoleState struct {
    Desired            IAMRoleSpec          `json:"desired"`
    Observed           ObservedState        `json:"observed"`
    Outputs            IAMRoleOutputs       `json:"outputs"`
    Status             types.ResourceStatus `json:"status"`
    Mode               types.Mode           `json:"mode"`
    Error              string               `json:"error,omitempty"`
    Generation         int64                `json:"generation"`
    LastReconcile      string               `json:"lastReconcile,omitempty"`
    ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
```

### Policy Documents as Strings

Both `assumeRolePolicyDocument` and `inlinePolicies` values are stored as JSON
strings, not parsed structures. This matches the AWS API's input/output format and
avoids complex policy document diffing with struct comparison. Drift detection
compares policy documents as canonical JSON (whitespace-normalized, keys sorted).

### ManagedPolicyArns as Sorted List

The `ManagedPolicyArns` slice is sorted before comparison in drift detection. AWS
returns attached policies in arbitrary order; sorting produces deterministic
comparison.

---

## Step 4 — AWS API Abstraction Layer

**File**: `internal/drivers/iamrole/aws.go`

### IAMRoleAPI Interface

```go
type IAMRoleAPI interface {
    // CreateRole creates a new IAM role.
    CreateRole(ctx context.Context, spec IAMRoleSpec) (arn, roleId string, err error)

    // DescribeRole returns the observed state of a role by name.
    DescribeRole(ctx context.Context, roleName string) (ObservedState, error)

    // DeleteRole deletes a role (must have no attached policies or instance profiles).
    DeleteRole(ctx context.Context, roleName string) error

    // UpdateAssumeRolePolicy updates the trust policy document.
    UpdateAssumeRolePolicy(ctx context.Context, roleName, policyDocument string) error

    // UpdateRole updates description and max session duration.
    UpdateRole(ctx context.Context, roleName, description string, maxSessionDuration int32) error

    // PutPermissionsBoundary sets or updates the permissions boundary.
    PutPermissionsBoundary(ctx context.Context, roleName, policyArn string) error

    // DeletePermissionsBoundary removes the permissions boundary.
    DeletePermissionsBoundary(ctx context.Context, roleName string) error

    // PutInlinePolicy creates or updates an inline policy on the role.
    PutInlinePolicy(ctx context.Context, roleName, policyName, policyDocument string) error

    // DeleteInlinePolicy removes an inline policy from the role.
    DeleteInlinePolicy(ctx context.Context, roleName, policyName string) error

    // ListInlinePolicies returns the names of all inline policies on the role.
    ListInlinePolicies(ctx context.Context, roleName string) ([]string, error)

    // GetInlinePolicy returns the policy document for an inline policy.
    GetInlinePolicy(ctx context.Context, roleName, policyName string) (string, error)

    // AttachManagedPolicy attaches a managed policy to the role.
    AttachManagedPolicy(ctx context.Context, roleName, policyArn string) error

    // DetachManagedPolicy detaches a managed policy from the role.
    DetachManagedPolicy(ctx context.Context, roleName, policyArn string) error

    // ListAttachedPolicies returns the ARNs of all managed policies attached to the role.
    ListAttachedPolicies(ctx context.Context, roleName string) ([]string, error)

    // UpdateTags replaces all user tags on the role.
    UpdateTags(ctx context.Context, roleName string, tags map[string]string) error
}
```

### realIAMRoleAPI Implementation

```go
type realIAMRoleAPI struct {
    client  *iam.Client
    limiter *ratelimit.Limiter
}

func NewIAMRoleAPI(client *iam.Client) IAMRoleAPI {
    return &realIAMRoleAPI{
        client:  client,
        limiter: ratelimit.New("iam", 15, 8),
    }
}
```

**Rate limiting**: IAM API has conservative rate limits (especially for
`CreateRole`, `AttachRolePolicy`). 15 sustained RPS with burst of 8 is conservative
and avoids throttling across concurrent driver invocations.

### Key Implementation Details

#### `CreateRole`

```go
func (r *realIAMRoleAPI) CreateRole(ctx context.Context, spec IAMRoleSpec) (string, string, error) {
    input := &iam.CreateRoleInput{
        RoleName:                 aws.String(spec.RoleName),
        Path:                     aws.String(spec.Path),
        AssumeRolePolicyDocument: aws.String(spec.AssumeRolePolicyDocument),
        MaxSessionDuration:       aws.Int32(spec.MaxSessionDuration),
    }
    if spec.Description != "" {
        input.Description = aws.String(spec.Description)
    }
    if spec.PermissionsBoundary != "" {
        input.PermissionsBoundary = aws.String(spec.PermissionsBoundary)
    }
    if len(spec.Tags) > 0 {
        input.Tags = toIAMTags(spec.Tags)
    }

    out, err := r.client.CreateRole(ctx, input)
    if err != nil {
        return "", "", err
    }
    return aws.ToString(out.Role.Arn),
           aws.ToString(out.Role.RoleId),
           nil
}
```

#### `DescribeRole`

The describe operation is composite — it requires multiple API calls to assemble
the full observed state:

```go
func (r *realIAMRoleAPI) DescribeRole(ctx context.Context, roleName string) (ObservedState, error) {
    // 1. GetRole — base role attributes + trust policy
    roleOut, err := r.client.GetRole(ctx, &iam.GetRoleInput{
        RoleName: aws.String(roleName),
    })
    if err != nil {
        return ObservedState{}, err
    }
    role := roleOut.Role

    // 2. ListRolePolicies — inline policy names
    inlineNames, err := r.ListInlinePolicies(ctx, roleName)
    if err != nil {
        return ObservedState{}, err
    }

    // 3. GetRolePolicy for each inline policy — fetch policy documents
    inlinePolicies := make(map[string]string, len(inlineNames))
    for _, name := range inlineNames {
        doc, err := r.GetInlinePolicy(ctx, roleName, name)
        if err != nil {
            return ObservedState{}, err
        }
        inlinePolicies[name] = doc
    }

    // 4. ListAttachedRolePolicies — managed policy ARNs
    managedArns, err := r.ListAttachedPolicies(ctx, roleName)
    if err != nil {
        return ObservedState{}, err
    }

    obs := ObservedState{
        Arn:                      aws.ToString(role.Arn),
        RoleId:                   aws.ToString(role.RoleId),
        RoleName:                 aws.ToString(role.RoleName),
        Path:                     aws.ToString(role.Path),
        AssumeRolePolicyDocument: aws.ToString(role.AssumeRolePolicyDocument),
        Description:              aws.ToString(role.Description),
        MaxSessionDuration:       aws.ToInt32(role.MaxSessionDuration),
        InlinePolicies:           inlinePolicies,
        ManagedPolicyArns:        managedArns,
        Tags:                     fromIAMTags(role.Tags),
    }
    if role.PermissionsBoundary != nil {
        obs.PermissionsBoundary = aws.ToString(role.PermissionsBoundary.PermissionsBoundaryArn)
    }
    if role.CreateDate != nil {
        obs.CreateDate = role.CreateDate.Format(time.RFC3339)
    }
    return obs, nil
}
```

**Composite describe**: Unlike EC2/S3 drivers where a single API call returns the
full resource state, IAM roles require 3+ API calls (GetRole + ListRolePolicies +
GetRolePolicy per inline policy + ListAttachedRolePolicies). All calls are made
within a single `restate.Run` block in the driver to ensure atomic journaling.

#### `DeleteRole`

```go
func (r *realIAMRoleAPI) DeleteRole(ctx context.Context, roleName string) error {
    _, err := r.client.DeleteRole(ctx, &iam.DeleteRoleInput{
        RoleName: aws.String(roleName),
    })
    return err
}
```

**Pre-deletion cleanup**: AWS requires that all inline policies, managed policy
attachments, and instance profile associations are removed before deleting a role.
The driver's Delete handler orchestrates this cleanup before calling `DeleteRole`.

#### `UpdateTags`

```go
func (r *realIAMRoleAPI) UpdateTags(ctx context.Context, roleName string, tags map[string]string) error {
    // 1. Get current tags
    roleOut, err := r.client.GetRole(ctx, &iam.GetRoleInput{
        RoleName: aws.String(roleName),
    })
    if err != nil {
        return err
    }

    // 2. Remove all existing tags
    existingKeys := make([]string, 0, len(roleOut.Role.Tags))
    for _, tag := range roleOut.Role.Tags {
        existingKeys = append(existingKeys, aws.ToString(tag.Key))
    }
    if len(existingKeys) > 0 {
        _, err = r.client.UntagRole(ctx, &iam.UntagRoleInput{
            RoleName: aws.String(roleName),
            TagKeys:  existingKeys,
        })
        if err != nil {
            return err
        }
    }

    // 3. Apply new tags
    if len(tags) > 0 {
        _, err = r.client.TagRole(ctx, &iam.TagRoleInput{
            RoleName: aws.String(roleName),
            Tags:     toIAMTags(tags),
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

All classifiers include string fallback for Restate-wrapped panic errors, following
the SG driver pattern.

### Helper Functions

```go
func toIAMTags(tags map[string]string) []iamtypes.Tag {
    out := make([]iamtypes.Tag, 0, len(tags))
    for k, v := range tags {
        out = append(out, iamtypes.Tag{Key: aws.String(k), Value: aws.String(v)})
    }
    return out
}

func fromIAMTags(tags []iamtypes.Tag) map[string]string {
    out := make(map[string]string, len(tags))
    for _, t := range tags {
        out[aws.ToString(t.Key)] = aws.ToString(t.Value)
    }
    return out
}
```

---

## Step 5 — Drift Detection

**File**: `internal/drivers/iamrole/drift.go`

IAM roles have significant mutable state: trust policies, inline policies, managed
policy attachments, description, session duration, permissions boundary, and tags.

### Core Functions

**`HasDrift(desired IAMRoleSpec, observed ObservedState) bool`**

```go
func HasDrift(desired IAMRoleSpec, observed ObservedState) bool {
    if !policyDocumentsEqual(desired.AssumeRolePolicyDocument, observed.AssumeRolePolicyDocument) {
        return true
    }
    if desired.Description != observed.Description {
        return true
    }
    if desired.MaxSessionDuration != observed.MaxSessionDuration {
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
    return !tagsMatch(desired.Tags, observed.Tags)
}
```

**`ComputeFieldDiffs(desired IAMRoleSpec, observed ObservedState) []FieldDiffEntry`**

Produces human-readable diffs for the plan renderer:

- Immutable fields: `path` — reported with "(immutable, ignored)" suffix.
- Mutable scalar fields: `description`, `maxSessionDuration`, `permissionsBoundary`.
- Trust policy: compared as canonical JSON. Diff rendered as old/new document strings.
- Inline policies: per-policy-name diffs (added, removed, changed).
- Managed policy ARNs: set difference (added, removed).
- Tags: per-key diffs (added, changed, removed).

### Policy Document Comparison

```go
// policyDocumentsEqual compares two IAM policy documents as canonical JSON.
// AWS URL-encodes policy documents in responses; this function decodes and
// normalizes before comparison.
func policyDocumentsEqual(a, b string) bool {
    canonA := canonicalizePolicyDoc(a)
    canonB := canonicalizePolicyDoc(b)
    return canonA == canonB
}

// canonicalizePolicyDoc URL-decodes, parses as JSON, and re-marshals with
// sorted keys and no extra whitespace.
func canonicalizePolicyDoc(doc string) string {
    decoded, err := url.QueryUnescape(doc)
    if err != nil {
        return doc // fallback: compare as-is
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

**Why URL-decode**: AWS returns policy documents as URL-encoded strings in
`GetRole` and `GetRolePolicy` responses. Direct string comparison would always
detect false drift. The driver decodes and canonicalizes before comparison.

### Inline Policy Comparison

```go
func inlinePoliciesEqual(desired, observed map[string]string) bool {
    if len(desired) != len(observed) {
        return false
    }
    for name, desiredDoc := range desired {
        observedDoc, ok := observed[name]
        if !ok {
            return false
        }
        if !policyDocumentsEqual(desiredDoc, observedDoc) {
            return false
        }
    }
    return true
}
```

### Managed Policy ARN Comparison

```go
func managedPolicyArnsEqual(desired, observed []string) bool {
    if len(desired) != len(observed) {
        return false
    }
    dSet := make(map[string]bool, len(desired))
    for _, arn := range desired {
        dSet[arn] = true
    }
    for _, arn := range observed {
        if !dSet[arn] {
            return false
        }
    }
    return true
}
```

---

## Step 6 — Driver Implementation

**File**: `internal/drivers/iamrole/driver.go`

### Service Registration

```go
const ServiceName = "IAMRole"
```

### Constructor Pattern

```go
func NewIAMRoleDriver(accounts *auth.Registry) *IAMRoleDriver
func NewIAMRoleDriverWithFactory(accounts *auth.Registry, factory func(aws.Config) IAMRoleAPI) *IAMRoleDriver
```

- `NewIAMRoleDriver`: Production constructor. Creates `IAMRoleAPI` from
  `awsclient.NewIAMClient()` for each resolved AWS config.
- `NewIAMRoleDriverWithFactory`: Test constructor. Injects mock `IAMRoleAPI`.

### Provision Handler

1. **Input validation**: `roleName` and `assumeRolePolicyDocument` must be non-empty.
   Returns `TerminalError(400)` on failure.

2. **Load current state**: Reads `IAMRoleState` from Restate K/V. Sets status to
   `Provisioning`, increments generation.

3. **Re-provision check**: If `state.Outputs.Arn` is non-empty, describes the role.
   If it's been deleted externally (404), clears ARN and falls through to creation.

4. **Create role**: Calls `api.CreateRole`. Classifies errors inside `restate.Run()`:
   - `IsAlreadyExists` → `TerminalError(409)`
   - `IsMalformedPolicy` → `TerminalError(400)`
   - `IsLimitExceeded` → `TerminalError(429)`

5. **Apply inline policies**: Iterates `spec.InlinePolicies` and calls
   `api.PutInlinePolicy` for each. On re-provision, computes diff and only
   adds/updates/removes changed policies.

6. **Attach managed policies**: Iterates `spec.ManagedPolicyArns` and calls
   `api.AttachManagedPolicy` for each. On re-provision, computes diff and
   attaches/detaches as needed.

7. **Converge mutable attributes** (re-provision path):
   - Trust policy: compare canon, update if changed.
   - Description + max session duration: update if changed.
   - Permissions boundary: add/update/remove as needed.
   - Tags: update if changed.

8. **Describe final state**: Calls `api.DescribeRole` to populate observed state.

9. **Commit state**: Sets status to `Ready`, saves state atomically, schedules
   reconciliation.

### Import Handler

1. Describes the role by `ref.ResourceID` (the IAM role name).
2. Synthesizes a `IAMRoleSpec` from the observed state via `specFromObserved()`.
3. Commits state with the observed state as both desired baseline and observed
   snapshot.
4. Sets mode to `ModeObserved` (IAM roles are high-value resources — import defaults
   to read-only tracking until the user explicitly switches to Managed mode).
5. Schedules reconciliation.

### Delete Handler

IAM role deletion requires pre-cleanup of all attached resources:

1. Sets status to `Deleting`.
2. **Detach all managed policies**: Lists attached policies, detaches each one.
3. **Delete all inline policies**: Lists inline policies, deletes each one.
4. **Remove from instance profiles**: Lists instance profiles for role, removes role
   from each profile.
5. **Delete role**: Calls `api.DeleteRole`.
6. **Error classification inside the callback**:
   - `IsDeleteConflict` → `TerminalError(409)` with message directing user to
     check remaining associations.
   - `IsNotFound` → silent success (already gone).
7. On success, sets status to `StatusDeleted`.

```go
func (d *IAMRoleDriver) Delete(ctx restate.ObjectContext) error {
    // ... load state, set Deleting ...

    // Pre-cleanup: detach all managed policies
    managedArns, err := restate.Run(ctx, func(rc restate.RunContext) ([]string, error) {
        return api.ListAttachedPolicies(rc, roleName)
    })
    // ... detach each ...

    // Pre-cleanup: delete all inline policies
    inlineNames, err := restate.Run(ctx, func(rc restate.RunContext) ([]string, error) {
        return api.ListInlinePolicies(rc, roleName)
    })
    // ... delete each ...

    // Pre-cleanup: remove from instance profiles
    // ... list instance profiles for role, remove from each ...

    // Delete the role
    _, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
        err := api.DeleteRole(rc, roleName)
        if err != nil {
            if IsNotFound(err) {
                return restate.Void{}, nil
            }
            if IsDeleteConflict(err) {
                return restate.Void{}, restate.TerminalError(
                    fmt.Errorf("cannot delete role %q: still has attached resources; "+
                        "check instance profiles and policy attachments", roleName), 409)
            }
            return restate.Void{}, err
        }
        return restate.Void{}, nil
    })
    // ...
}
```

### Reconcile Handler

Reconcile runs on a 5-minute timer and follows the standard pattern:

1. Clears `ReconcileScheduled` flag.
2. Skips if status is not `Ready` or `Error`.
3. Describes current AWS state (composite: role + policies + attachments).
4. **Managed + drift**: Corrects trust policy, inline policies, managed policy
   attachments, description, session duration, permissions boundary, tags.
5. **Observed + drift**: Reports only.
6. Re-schedules.

**Inline policy convergence** during reconciliation:
- Policies in desired but not observed → `PutInlinePolicy` (add).
- Policies in observed but not desired → `DeleteInlinePolicy` (remove).
- Policies in both but documents differ → `PutInlinePolicy` (update).

**Managed policy convergence**:
- ARNs in desired but not observed → `AttachManagedPolicy`.
- ARNs in observed but not desired → `DetachManagedPolicy`.

### GetStatus / GetOutputs

Standard shared handlers — read state and return projections.

---

## Step 7 — Provider Adapter

**File**: `internal/core/provider/iamrole_adapter.go`

```go
type IAMRoleAdapter struct {
    auth              *auth.Registry
    staticPlanningAPI iamrole.IAMRoleAPI
    apiFactory        func(aws.Config) iamrole.IAMRoleAPI
}
```

### Methods

**`Scope() KeyScope`** → `KeyScopeGlobal`

**`Kind() string`** → `"IAMRole"`

**`ServiceName() string`** → `"IAMRole"`

**`BuildKey(resourceDoc json.RawMessage) (string, error)`**:
Decodes the resource document, extracts `metadata.name`. Returns the role name
directly (no region prefix — IAM is global).

**`BuildImportKey(region, resourceID string) (string, error)`**:
Returns `resourceID` directly (the IAM role name). Same key as `BuildKey` —
matches S3 pattern.

**`Plan(ctx, key, account, desiredSpec) (DiffOperation, []FieldDiff, error)`**:
Calls `api.DescribeRole(roleName)` via `restate.Run()`. If not found →
`OpCreate`. If found → `ComputeFieldDiffs()`. If no diffs → `OpNoOp`.
If diffs → `OpUpdate`.

---

## Step 8 — Registry Integration

**File**: `internal/core/provider/registry.go` (modified)

Add `NewIAMRoleAdapterWithRegistry(accounts)` to `NewRegistry()`.

---

## Step 9 — IAM Driver Pack Entry Point

**File**: `cmd/praxis-iam/main.go`

The IAM driver pack hosts all IAM-related drivers (Role, Policy, User, Group,
Instance Profile). IAM is foundational — it is consumed by all other resource
types across compute, network, and storage domains.

```go
func main() {
    cfg := config.Load()

    srv := server.NewRestate().
        Bind(restate.Reflect(iamrole.NewIAMRoleDriver(cfg.Auth())))
        // Future: .Bind(restate.Reflect(iampolicy.NewIAMPolicyDriver(...)))
        // Future: .Bind(restate.Reflect(iamuser.NewIAMUserDriver(...)))
        // Future: .Bind(restate.Reflect(iamgroup.NewIAMGroupDriver(...)))
        // Future: .Bind(restate.Reflect(iaminstanceprofile.NewInstanceProfileDriver(...)))

    slog.Info("starting iam driver pack", "addr", cfg.ListenAddr)
    if err := srv.Start(context.Background(), cfg.ListenAddr); err != nil {
        slog.Error("iam driver pack exited", "err", err.Error())
        os.Exit(1)
    }
}
```

### Dockerfile

**File**: `cmd/praxis-iam/Dockerfile`

```dockerfile
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /praxis-iam ./cmd/praxis-iam

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /praxis-iam /praxis-iam
ENTRYPOINT ["/praxis-iam"]
```

---

## Step 10 — Docker Compose & Justfile

### Docker Compose

**File**: `docker-compose.yaml` (modified)

```yaml
praxis-iam:
    build:
      context: .
      dockerfile: cmd/praxis-iam/Dockerfile
    container_name: praxis-iam
    env_file:
      - .env
    depends_on:
      restate:
        condition: service_healthy
      localstack-init:
        condition: service_completed_successfully
    ports:
      - "9085:9080"
    environment:
      - PRAXIS_LISTEN_ADDR=0.0.0.0:9080
```

Port 9085 (Storage: 9081, Network: 9082, Core: 9083, Compute: 9084, IAM: 9085).

### Justfile Targets

| Target | Command |
|---|---|
| `logs-iam` | `docker compose logs -f praxis-iam` |
| `test-iamrole` | `go test ./internal/drivers/iamrole/... -v -count=1 -race` |
| `test-iamrole-integration` | `go test ./tests/integration/ -run TestIAMRole -v -count=1 -tags=integration -timeout=5m` |
| `build` (shared) | Add `go build -o bin/praxis-iam ./cmd/praxis-iam` |
| `register` (shared) | Register IAM pack at `http://praxis-iam:9080` |
| `up` (shared) | Add `praxis-iam` to the list of services |

---

## Step 11 — Unit Tests

### `internal/drivers/iamrole/drift_test.go`

| Test | Purpose |
|---|---|
| `TestHasDrift_NoDrift` | Matching state → no drift |
| `TestHasDrift_TrustPolicyDrift` | Trust policy change → drift |
| `TestHasDrift_TrustPolicyWhitespace` | Same policy, different whitespace → no drift |
| `TestHasDrift_TrustPolicyURLEncoded` | URL-encoded vs decoded → no drift |
| `TestHasDrift_DescriptionDrift` | Description change → drift |
| `TestHasDrift_MaxSessionDrift` | Session duration change → drift |
| `TestHasDrift_PermissionsBoundaryDrift` | Boundary added/changed → drift |
| `TestHasDrift_InlinePolicyAdded` | New inline policy → drift |
| `TestHasDrift_InlinePolicyRemoved` | Missing inline policy → drift |
| `TestHasDrift_InlinePolicyChanged` | Changed policy document → drift |
| `TestHasDrift_ManagedPolicyAdded` | New managed policy ARN → drift |
| `TestHasDrift_ManagedPolicyRemoved` | Missing managed policy ARN → drift |
| `TestHasDrift_ManagedPolicyOrderIndependent` | Same ARNs, different order → no drift |
| `TestHasDrift_TagDrift` | Tag change → drift |
| `TestCanonicalizePolicyDoc_Whitespace` | Whitespace normalization |
| `TestCanonicalizePolicyDoc_URLEncoded` | URL decoding |
| `TestComputeFieldDiffs_ImmutablePath` | Reports path change as "(immutable, ignored)" |
| `TestComputeFieldDiffs_AllMutableFields` | Full diff with all mutable fields changed |

### `internal/drivers/iamrole/aws_test.go`

| Test | Purpose |
|---|---|
| `TestIsNotFound_NoSuchEntity` | IAM NoSuchEntity error → true |
| `TestIsNotFound_OtherError` | Other error → false |
| `TestIsAlreadyExists_EntityAlreadyExists` | Duplicate name → true |
| `TestIsDeleteConflict_True` | DeleteConflict error → true |
| `TestIsMalformedPolicy_True` | MalformedPolicyDocument → true |
| `TestIsLimitExceeded_True` | LimitExceeded → true |
| `TestIsNotFound_WrappedRestateError` | String fallback for Restate-wrapped errors |

### `internal/drivers/iamrole/driver_test.go`

| Test | Purpose |
|---|---|
| `TestSpecFromObserved_RoundTrip` | Observed → spec preserves all fields |
| `TestSpecFromObserved_Empty` | Empty observed → minimal spec |
| `TestServiceName` | Returns "IAMRole" |
| `TestOutputsFromObserved` | Correct output mapping |

### `internal/core/provider/iamrole_adapter_test.go`

| Test | Purpose |
|---|---|
| `TestIAMRoleAdapter_BuildKey` | Returns role name |
| `TestIAMRoleAdapter_BuildImportKey` | Returns role name (same as BuildKey) |
| `TestIAMRoleAdapter_Kind` | Returns "IAMRole" |
| `TestIAMRoleAdapter_Scope` | Returns `KeyScopeGlobal` |
| `TestIAMRoleAdapter_NormalizeOutputs` | Converts struct to map |

---

## Step 12 — Integration Tests

**File**: `tests/integration/iamrole_driver_test.go`

Integration tests run against Testcontainers (Restate) + LocalStack (IAM).

### Test Cases

| Test | Description |
|---|---|
| `TestIAMRoleProvision_CreatesRole` | Creates a role with trust policy, inline policies, managed policy attachments, and tags. Verifies the role exists in LocalStack via `GetRole`. |
| `TestIAMRoleProvision_Idempotent` | Provisions the same spec twice on the same key. Verifies same ARN (no duplicate). |
| `TestIAMRoleProvision_UpdateTrustPolicy` | Re-provisions with changed trust policy. Verifies the new policy is active. |
| `TestIAMRoleProvision_AddInlinePolicy` | Re-provisions with an additional inline policy. Verifies both policies exist. |
| `TestIAMRoleProvision_AttachManagedPolicy` | Re-provisions with an additional managed policy ARN. Verifies attachment. |
| `TestIAMRoleImport_ExistingRole` | Creates a role directly via IAM API, then imports via the driver. Verifies Observed mode. |
| `TestIAMRoleDelete_RemovesRole` | Provisions with inline and managed policies, then deletes. Verifies cleanup of attachments and role deletion. |
| `TestIAMRoleReconcile_DetectsDrift` | Provisions, then adds an inline policy directly via IAM API. Triggers reconcile, verifies drift detected and corrected (extra policy removed). |
| `TestIAMRoleGetStatus_ReturnsReady` | Provisions and checks `GetStatus` returns `Ready`, `Managed`, generation > 0. |

---

## IAM-Role-Specific Design Decisions

### 1. Global Key Scope — No Region Prefix

IAM is a global AWS service. Role names are unique within an account, regardless of
region. The key is simply `roleName` — no region prefix needed. This matches S3's
`KeyScopeGlobal` pattern.

### 2. Import Defaults to ModeObserved

Unlike key pairs (which default to ModeManaged because they're lightweight metadata),
IAM roles are high-value resources that control access to the entire AWS account.
Importing defaults to ModeObserved to prevent accidental modification. Users must
explicitly switch to ModeManaged if they want Praxis to converge the role's state.

### 3. Pre-Deletion Cleanup

AWS requires that a role has no inline policies, attached managed policies, or
instance profile associations before it can be deleted. The driver's Delete handler
performs all cleanup automatically:

1. Detach all managed policies
2. Delete all inline policies
3. Remove role from all instance profiles
4. Delete the role

This is a single logical operation from the user's perspective ("delete my role"),
but requires orchestrating 4 cleanup phases. Each phase is wrapped in its own
`restate.Run` block for journal atomicity.

### 4. Policy Document Canonicalization

AWS returns policy documents URL-encoded and with arbitrary whitespace. Direct
string comparison would produce false drift on every reconciliation. The driver
decodes and canonicalizes (JSON parse → re-marshal with sorted keys, no whitespace)
before comparison.

### 5. Composite Describe

Unlike EC2 (one `DescribeInstances` call returns everything), describing an IAM role
requires 3+ API calls: `GetRole`, `ListRolePolicies`, `GetRolePolicy` (per inline
policy), and `ListAttachedRolePolicies`. These are all performed within a single
`restate.Run` to ensure atomic journaling. If any sub-call fails, the entire describe
is retried.

### 6. Inline Policy Convergence: Add-Before-Remove

When converging inline policies during Provision or Reconcile:
1. Add/update new or changed policies first (`PutRolePolicy`).
2. Remove stale policies second (`DeleteRolePolicy`).

This ensures there is never a window where the role lacks a required permission.

### 7. Managed Policy Convergence: Attach-Before-Detach

Same principle as inline policies:
1. Attach new managed policies first.
2. Detach removed managed policies second.

### 8. CUE Schema Placement: `schemas/aws/iam/`

IAM resources get their own CUE package directory (`schemas/aws/iam/`), separate
from `ec2`, `s3`, or `vpc`. All IAM resource types (Role, Policy, User, Group,
Instance Profile) share the `iam` package and can cross-reference each other.

### 9. Driver Pack Placement: praxis-iam

IAM is cross-cutting — consumed by compute, network, and storage resources. Rather
than bolting IAM drivers onto an existing domain pack (e.g., compute), a dedicated
`praxis-iam` pack keeps concerns separated and allows independent scaling. Port 9085.

---

## Design Decisions (Resolved)

1. **Should inline policies and managed policies be separate drivers?**
   No. Inline policies are integral to the role — they have no independent lifecycle
   or ARN. Managed policy *attachments* (attach/detach) are a property of the role,
   not the policy. The IAM Policy driver manages policy *creation*; the role driver
   manages *attachment*. This mirrors the SG pattern where rules are a property of
   the group, not separate resources.

2. **Should the driver handle service-linked roles?**
   No. Service-linked roles are created by AWS services and have special naming
   conventions (`AWSServiceRoleFor*`). They cannot be created or deleted via standard
   `CreateRole`/`DeleteRole` calls. Service-linked roles should be excluded from
   scope and possibly handled by a dedicated driver in the future.

3. **Should `maxSessionDuration` default to 3600 in the schema or let AWS decide?**
   The schema defaults to 3600 (1 hour), matching the AWS default. This makes the
   default explicit in the template and prevents drift if the user updates it via
   the Console and expects Praxis to preserve the change.

4. **Should re-provision remove inline policies not in the spec?**
   Yes. The driver converges to the exact spec — inline policies not in the spec
   are removed. This matches the "desired state" model. If a user removes a policy
   from their template, the next apply removes it from the role.

5. **Should the driver support role name changes?**
   No. IAM role names are immutable after creation. Changing the name requires
   delete + recreate. The `roleName` is the Virtual Object key; changing it would
   require a new VO.

---

## Checklist

- [ ] **Schema**: `schemas/aws/iam/role.cue` created
- [ ] **Types**: `internal/drivers/iamrole/types.go` created
- [ ] **AWS client**: `internal/infra/awsclient/client.go` updated with `NewIAMClient`
- [ ] **AWS API**: `internal/drivers/iamrole/aws.go` created
- [ ] **Drift**: `internal/drivers/iamrole/drift.go` created
- [ ] **Driver**: `internal/drivers/iamrole/driver.go` created with all 6 handlers
- [ ] **Adapter**: `internal/core/provider/iamrole_adapter.go` created
- [ ] **Registry**: `internal/core/provider/registry.go` updated
- [ ] **Entry point**: `cmd/praxis-iam/main.go` created
- [ ] **Dockerfile**: `cmd/praxis-iam/Dockerfile` created
- [ ] **Docker Compose**: `docker-compose.yaml` updated with praxis-iam service
- [ ] **Justfile**: Updated with IAM targets
- [ ] **Unit tests (drift)**: `internal/drivers/iamrole/drift_test.go`
- [ ] **Unit tests (aws helpers)**: `internal/drivers/iamrole/aws_test.go`
- [ ] **Unit tests (driver)**: `internal/drivers/iamrole/driver_test.go`
- [ ] **Unit tests (adapter)**: `internal/core/provider/iamrole_adapter_test.go`
- [ ] **Integration tests**: `tests/integration/iamrole_driver_test.go`
- [ ] **Policy canonicalization**: URL-decode + JSON normalize for drift comparison
- [ ] **Pre-deletion cleanup**: Detach managed, delete inline, remove from profiles
- [ ] **Composite describe**: GetRole + ListRolePolicies + GetRolePolicy + ListAttached
- [ ] **Import default mode**: `ModeObserved` (high-value resource)
- [ ] **Delete mode guard**: Delete handler blocks for ModeObserved (409)
- [ ] **go.mod**: `github.com/aws/aws-sdk-go-v2/service/iam` added
- [ ] **Build passes**: `go build ./...` succeeds
- [ ] **Unit tests pass**: `go test ./internal/drivers/iamrole/... -race`
- [ ] **Integration tests pass**: `go test ./tests/integration/ -run TestIAMRole -tags=integration`
