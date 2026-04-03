# ECR Lifecycle Policy Driver — Implementation Spec

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
12. [Step 9 — Compute Driver Pack Entry Point](#step-9--compute-driver-pack-entry-point)
13. [Step 10 — Docker Compose & Justfile](#step-10--docker-compose--justfile)
14. [Step 11 — Unit Tests](#step-11--unit-tests)
15. [Step 12 — Integration Tests](#step-12--integration-tests)
16. [ECR-LifecyclePolicy-Specific Design Decisions](#ecr-lifecyclepolicy-specific-design-decisions)
17. [Checklist](#checklist)

---

## 1. Overview & Scope

The ECR Lifecycle Policy driver manages the lifecycle policy attached to an ECR
repository. ECR allows exactly one lifecycle policy per repository. The policy is a
JSON document that defines rules for automatically expiring images (e.g., "keep only
the last 30 tagged images" or "expire untagged images older than 14 days").

**Lifecycle policies are a sub-resource of an ECR repository.** They have no
independent name, ARN, or standalone lifecycle. `PutLifecyclePolicy` is an upsert
that creates or fully replaces the policy in a single atomic call. This makes the
driver unusually simple: `Provision` is always a `PutLifecyclePolicy`, drift
detection reduces to JSON semantic equality, and there are no partial update paths.

### Resource Scope for This Plan

| In Scope | Out of Scope |
|---|---|
| Lifecycle policy document (create, update, delete) | Repository management (ECR Repository driver) |
| Import of existing lifecycle policy | Image management (push/pull) |
| Drift detection (JSON semantic equality) | Policy preview / dry-run |

### Lifecycle Policy Document Structure

An ECR lifecycle policy is a JSON document with the following shape:

```json
{
  "rules": [
    {
      "rulePriority": 1,
      "description": "Expire untagged images after 14 days",
      "selection": {
        "tagStatus": "untagged",
        "countType": "sinceImagePushed",
        "countUnit": "days",
        "countNumber": 14
      },
      "action": {
        "type": "expire"
      }
    }
  ]
}
```

Rules are evaluated in ascending `rulePriority` order. Lower priority numbers take
precedence. The driver does not validate the structure of the policy document beyond
checking that it is valid JSON — AWS performs full validation at
`PutLifecyclePolicy` time.

### Driver Contract

| Handler | Context | Purpose |
|---|---|---|
| `Provision` | `ObjectContext` (exclusive) | Create or replace the lifecycle policy |
| `Import` | `ObjectContext` (exclusive) | Adopt an existing lifecycle policy |
| `Delete` | `ObjectContext` (exclusive) | Delete the lifecycle policy |
| `Reconcile` | `ObjectContext` (exclusive) | Detect/correct drift |
| `GetStatus` | `ObjectSharedContext` (shared) | Return current status |
| `GetOutputs` | `ObjectSharedContext` (shared) | Return policy outputs |

### Mutable vs Immutable Attributes

| Attribute | Mutability | Notes |
|---|---|---|
| `repositoryName` | Immutable | Part of the Virtual Object key; the policy belongs to this repository |
| `lifecyclePolicyText` | Mutable | Full JSON policy document; always replaces the existing policy |

### What Is NOT In Scope

- **Repository management**: The ECR Repository driver owns the repository resource.
  This driver only manages the lifecycle policy attached to it.
- **Policy preview**: ECR provides a `StartLifecyclePolicyPreview` API that simulates
  which images a policy would affect. This is a read-only diagnostic operation and
  is not modeled as a Praxis resource.
- **Replication**: ECR replication rules are a separate registry-level configuration.

### Downstream Consumers

Lifecycle policies are terminal resources — no other driver depends on their outputs.
They do not produce consuming outputs. The `repositoryArn` in outputs is informational
only.

```text
${resources.my-lcp.outputs.repositoryArn}   → Informational reference
${resources.my-lcp.outputs.registryId}      → Informational reference
```

---

## 2. Key Strategy

### Key Scope: `KeyScopeCustom`

ECR lifecycle policies have no user-chosen name — they are identified by the
repository they belong to. The Virtual Object key is derived from
`spec.region + "~" + spec.repositoryName`, not from `metadata.name`.

This mirrors the `LambdaPermission` pattern (`region~functionName~statementId`) where
the key is built from spec fields rather than the template resource name.

### BuildKey vs BuildImportKey

- **`BuildKey(resourceDoc)`**: Extracts `spec.region` and `spec.repositoryName`.
  Returns `region~repositoryName`.

- **`BuildImportKey(region, resourceID)`**: Returns `region~resourceID`. For lifecycle
  policies, `resourceID` is the repository name.

### PutLifecyclePolicy as Upsert

`PutLifecyclePolicy` is idempotent in the AWS API — it creates the policy if none
exists, or replaces the existing policy if one is already set. This means:

1. `Provision` always calls `PutLifecyclePolicy` regardless of whether a policy
   already exists. There is no separate create/update path.
2. The driver's `Provision` implementation is functionally a single `restate.Run`
   wrapping a `PutLifecyclePolicy` call.
3. Drift detection is a read-then-compare operation used by `Reconcile`, not by
   `Provision` itself.

---

## 3. File Inventory

Create or modify these files (✦ = new file, ✎ = modify existing):

```text
✦ schemas/aws/ecr/lifecycle_policy.cue                         — CUE schema for ECRLifecyclePolicy
✦ internal/drivers/ecrpolicy/types.go                          — Spec, Outputs, ObservedState, State structs
✦ internal/drivers/ecrpolicy/aws.go                            — LifecyclePolicyAPI interface + realLifecyclePolicyAPI
✦ internal/drivers/ecrpolicy/drift.go                          — HasDrift(), ComputeFieldDiffs()
✦ internal/drivers/ecrpolicy/driver.go                         — ECRLifecyclePolicyDriver Virtual Object
✦ internal/drivers/ecrpolicy/driver_test.go                    — Unit tests for driver
✦ internal/drivers/ecrpolicy/aws_test.go                       — Unit tests for error classification
✦ internal/drivers/ecrpolicy/drift_test.go                     — Unit tests for drift detection
✦ internal/core/provider/ecrlifecyclepolicy_adapter.go         — ECRLifecyclePolicyAdapter implementing provider.Adapter
✦ internal/core/provider/ecrlifecyclepolicy_adapter_test.go    — Adapter unit tests
✦ tests/integration/ecr_lifecycle_policy_driver_test.go        — Integration tests
✎ cmd/praxis-compute/main.go                                   — Bind ECRLifecyclePolicy driver
✎ internal/core/provider/registry.go                           — Add NewECRLifecyclePolicyAdapter() to NewRegistry()
```

Note: No new AWS client factory is needed — `NewECRClient()` is added once for the
ECR Repository driver and shared by this driver.

---

## Step 1 — CUE Schema

**File**: `schemas/aws/ecr/lifecycle_policy.cue`

```cue
package ecr

#ECRLifecyclePolicy: {
    apiVersion: "praxis.io/v1"
    kind:       "ECRLifecyclePolicy"

    metadata: {
        // name is the template-local identifier for this lifecycle policy.
        // Does NOT map to an AWS resource name — the policy is identified by the
        // repository it is attached to.
        name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9._-]{0,254}$"
        labels: [string]: string
    }

    spec: {
        // region is the AWS region of the repository.
        region: string

        // repositoryName is the name of the ECR repository to attach the policy to.
        // Must reference an existing ECR repository in the same region.
        repositoryName: string & =~"^[a-z0-9][a-z0-9/_.-]{1,255}$"

        // lifecyclePolicyText is the JSON lifecycle policy document.
        // Must contain a "rules" array. Each rule defines image selection criteria
        // and an expiration action. Rules are evaluated in ascending rulePriority order.
        // AWS validates the document structure; the schema only checks for non-empty JSON.
        lifecyclePolicyText: string & =~"^\\s*\\{"
    }

    outputs?: {
        // repositoryArn is the ARN of the repository this policy is attached to.
        repositoryArn: string
        // repositoryName is the repository name.
        repositoryName: string
        // registryId is the AWS account ID.
        registryId: string
    }
}
```

### Schema Design Notes

- **`metadata.name` vs `spec.repositoryName`**: The schema follows the
  `LambdaPermission` pattern where `metadata.name` is a free-form template identifier
  and the actual AWS identity comes from spec fields (`repositoryName`). This allows
  templates to reference the policy resource by a human-friendly name while the
  key is constructed from the spec.
- **`lifecyclePolicyText` validation**: Only a lightweight check for a leading `{`
  character is enforced in the schema. Full structural validation happens at the AWS
  API level (`PutLifecyclePolicy`), which returns detailed error messages for
  malformed documents.
- **No tags**: ECR lifecycle policies cannot be tagged independently. Tags belong to
  the repository resource.

### Example Template Usage

```cue
"image-cleanup-policy": {
    apiVersion: "praxis.io/v1"
    kind:       "ECRLifecyclePolicy"
    metadata: name: "image-cleanup-policy"
    spec: {
        region:         "us-east-1"
        repositoryName: "${resources.my-app-repo.outputs.repositoryName}"
        lifecyclePolicyText: """
            {
              "rules": [
                {
                  "rulePriority": 1,
                  "description": "Expire untagged images after 14 days",
                  "selection": {
                    "tagStatus": "untagged",
                    "countType": "sinceImagePushed",
                    "countUnit": "days",
                    "countNumber": 14
                  },
                  "action": { "type": "expire" }
                },
                {
                  "rulePriority": 2,
                  "description": "Keep only the last 30 tagged images",
                  "selection": {
                    "tagStatus": "tagged",
                    "tagPrefixList": ["v"],
                    "countType": "imageCountMoreThan",
                    "countNumber": 30
                  },
                  "action": { "type": "expire" }
                }
              ]
            }
            """
    }
}
```

---

## Step 2 — AWS Client Factory

No new factory function is needed. The ECR Repository driver introduces
`NewECRClient()` in `internal/infra/awsclient/client.go`. This driver shares the
same client factory:

```go
// Already added by ECR Repository driver:
// func NewECRClient(cfg aws.Config) *ecr.Client { ... }
```

---

## Step 3 — Driver Types

**File**: `internal/drivers/ecrpolicy/types.go`

```go
package ecrpolicy

import "github.com/shirvan/praxis/pkg/types"

const ServiceName = "ECRLifecyclePolicy"

// ECRLifecyclePolicySpec is the desired state for an ECR lifecycle policy.
type ECRLifecyclePolicySpec struct {
    Region               string `json:"region"`
    RepositoryName       string `json:"repositoryName"`
    LifecyclePolicyText  string `json:"lifecyclePolicyText"`
}

// ECRLifecyclePolicyOutputs is produced after provisioning.
type ECRLifecyclePolicyOutputs struct {
    RepositoryArn  string `json:"repositoryArn"`
    RepositoryName string `json:"repositoryName"`
    RegistryId     string `json:"registryId"`
}

// ObservedState captures the actual lifecycle policy from AWS.
type ObservedState struct {
    LifecyclePolicyText string `json:"lifecyclePolicyText"`
    RepositoryName      string `json:"repositoryName"`
    RegistryId          string `json:"registryId"`
    RepositoryArn       string `json:"repositoryArn,omitempty"`
}

// ECRLifecyclePolicyState is the single atomic state object stored under drivers.StateKey.
type ECRLifecyclePolicyState struct {
    Desired       ECRLifecyclePolicySpec    `json:"desired"`
    Observed      ObservedState             `json:"observed"`
    Outputs       ECRLifecyclePolicyOutputs `json:"outputs"`
    Status        types.ResourceStatus      `json:"status"`
    Mode          types.Mode                `json:"mode"`
    Error         string                    `json:"error,omitempty"`
    Generation    int64                     `json:"generation"`
    LastReconcile string                    `json:"lastReconcile,omitempty"`
}
```

### Why These Fields

- **`RepositoryArn` in Outputs**: Surfaced as a convenience output for downstream
  references, even though lifecycle policies themselves have no ARN. Resolved by
  calling `DescribeRepositories` during Provision.
- **No `ReconcileScheduled`**: Lifecycle policies are simple upsert resources with
  no async operations. Scheduled reconciliation is not needed.

---

## Step 4 — AWS API Abstraction Layer

**File**: `internal/drivers/ecrpolicy/aws.go`

### LifecyclePolicyAPI Interface

```go
type LifecyclePolicyAPI interface {
    // PutLifecyclePolicy creates or replaces the lifecycle policy for a repository.
    // Returns (registryId, error). AWS returns RepositoryNotFoundException if the
    // target repository does not exist.
    PutLifecyclePolicy(ctx context.Context, repositoryName, policyText string) (string, error)

    // GetLifecyclePolicy retrieves the current lifecycle policy for a repository.
    // Returns (policyText, registryId, error).
    // Returns LifecyclePolicyNotFoundException if no policy is set.
    GetLifecyclePolicy(ctx context.Context, repositoryName string) (ObservedState, error)

    // DeleteLifecyclePolicy removes the lifecycle policy from a repository.
    // Returns LifecyclePolicyNotFoundException if no policy is set (treat as success).
    DeleteLifecyclePolicy(ctx context.Context, repositoryName string) error

    // DescribeRepositoryArn returns the ARN and registryId for a repository.
    // Used to populate outputs.repositoryArn during Provision.
    DescribeRepositoryArn(ctx context.Context, repositoryName string) (arn, registryId string, err error)
}
```

### realLifecyclePolicyAPI Implementation

```go
type realLifecyclePolicyAPI struct {
    client  *ecr.Client
    limiter *ratelimit.Limiter
}

func NewLifecyclePolicyAPI(client *ecr.Client) LifecyclePolicyAPI {
    return &realLifecyclePolicyAPI{
        client:  client,
        limiter: ratelimit.New("ecr-policy", 10, 5),
    }
}
```

### Error Classification

```go
func classifyError(err error) error {
    if err == nil {
        return nil
    }
    var repoNotFound *ecrtypes.RepositoryNotFoundException
    if errors.As(err, &repoNotFound) {
        return restate.TerminalError(fmt.Errorf("repository not found — the ECRRepository must be provisioned before its lifecycle policy: %w", err), 404)
    }
    var policyNotFound *ecrtypes.LifecyclePolicyNotFoundException
    if errors.As(err, &policyNotFound) {
        return restate.TerminalError(fmt.Errorf("lifecycle policy not found: %w", err), 404)
    }
    var invalidParam *ecrtypes.InvalidParameterException
    if errors.As(err, &invalidParam) {
        return restate.TerminalError(fmt.Errorf("invalid lifecycle policy document: %w", err), 400)
    }
    var serverErr *ecrtypes.ServerException
    if errors.As(err, &serverErr) {
        return fmt.Errorf("ECR server error (retryable): %w", err)
    }
    return err
}
```

### Key Implementation Details

#### `PutLifecyclePolicy`

```go
func (r *realLifecyclePolicyAPI) PutLifecyclePolicy(ctx context.Context, repositoryName, policyText string) (string, error) {
    out, err := r.client.PutLifecyclePolicy(ctx, &ecr.PutLifecyclePolicyInput{
        RepositoryName:      aws.String(repositoryName),
        LifecyclePolicyText: aws.String(policyText),
    })
    if err != nil {
        return "", classifyError(err)
    }
    return aws.ToString(out.RegistryId), nil
}
```

#### `GetLifecyclePolicy`

```go
func (r *realLifecyclePolicyAPI) GetLifecyclePolicy(ctx context.Context, repositoryName string) (ObservedState, error) {
    out, err := r.client.GetLifecyclePolicy(ctx, &ecr.GetLifecyclePolicyInput{
        RepositoryName: aws.String(repositoryName),
    })
    if err != nil {
        return ObservedState{}, classifyError(err)
    }
    return ObservedState{
        LifecyclePolicyText: aws.ToString(out.LifecyclePolicyText),
        RepositoryName:      aws.ToString(out.RepositoryName),
        RegistryId:          aws.ToString(out.RegistryId),
    }, nil
}
```

#### `DeleteLifecyclePolicy`

```go
func (r *realLifecyclePolicyAPI) DeleteLifecyclePolicy(ctx context.Context, repositoryName string) error {
    _, err := r.client.DeleteLifecyclePolicy(ctx, &ecr.DeleteLifecyclePolicyInput{
        RepositoryName: aws.String(repositoryName),
    })
    if err != nil {
        var notFound *ecrtypes.LifecyclePolicyNotFoundException
        if errors.As(err, &notFound) {
            return nil // idempotent — already deleted
        }
        return classifyError(err)
    }
    return nil
}
```

---

## Step 5 — Drift Detection

**File**: `internal/drivers/ecrpolicy/drift.go`

### HasDrift

Drift detection is purely a JSON semantic equality check on `lifecyclePolicyText`.
There are no immutable fields beyond `repositoryName` (which is part of the key and
cannot change without targeting a different Virtual Object).

```go
package ecrpolicy

import (
    "encoding/json"
    "fmt"
)

// FieldDiffEntry describes a single detected drift field.
type FieldDiffEntry struct {
    Field   string
    Desired string
    Actual  string
}

// HasDrift returns true if the desired policy text differs from the observed.
// Comparison is JSON-semantic (key ordering does not matter).
func HasDrift(desired ECRLifecyclePolicySpec, observed ObservedState) bool {
    return !jsonEqual(desired.LifecyclePolicyText, observed.LifecyclePolicyText)
}

// ComputeFieldDiffs returns a list of fields with detected drift.
func ComputeFieldDiffs(desired ECRLifecyclePolicySpec, observed ObservedState) []FieldDiffEntry {
    if !jsonEqual(desired.LifecyclePolicyText, observed.LifecyclePolicyText) {
        return []FieldDiffEntry{{
            Field:   "lifecyclePolicyText",
            Desired: desired.LifecyclePolicyText,
            Actual:  observed.LifecyclePolicyText,
        }}
    }
    return nil
}

// jsonEqual performs a semantic JSON equality check by unmarshaling both strings.
func jsonEqual(a, b string) bool {
    if a == b {
        return true
    }
    normalize := func(s string) interface{} {
        if s == "" {
            return nil
        }
        var v interface{}
        if err := json.Unmarshal([]byte(s), &v); err != nil {
            return s
        }
        return v
    }
    ab, _ := json.Marshal(normalize(a))
    bb, _ := json.Marshal(normalize(b))
    return string(ab) == string(bb)
}

// FormatDrift returns a human-readable description of drift.
func FormatDrift(diffs []FieldDiffEntry) string {
    if len(diffs) == 0 {
        return "no drift"
    }
    return fmt.Sprintf("lifecycle policy text differs from desired (%d rule(s) changed)", countRules(diffs[0].Desired))
}

func countRules(policyText string) int {
    var doc struct {
        Rules []interface{} `json:"rules"`
    }
    if err := json.Unmarshal([]byte(policyText), &doc); err != nil {
        return 0
    }
    return len(doc.Rules)
}
```

---

## Step 6 — Driver Implementation

**File**: `internal/drivers/ecrpolicy/driver.go`

### ECRLifecyclePolicyDriver

```go
type ECRLifecyclePolicyDriver struct {
    auth       authservice.AuthClient
    apiFactory func(aws.Config) LifecyclePolicyAPI
}

func NewECRLifecyclePolicyDriver(auth authservice.AuthClient) *ECRLifecyclePolicyDriver {
    return NewECRLifecyclePolicyDriverWithFactory(auth, func(cfg aws.Config) LifecyclePolicyAPI {
        return NewLifecyclePolicyAPI(awsclient.NewECRClient(cfg))
    })
}

func NewECRLifecyclePolicyDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) LifecyclePolicyAPI) *ECRLifecyclePolicyDriver {
    if accounts == nil {
        auth = authservice.NewAuthClient()
    }
    if factory == nil {
        factory = func(cfg aws.Config) LifecyclePolicyAPI {
            return NewLifecyclePolicyAPI(awsclient.NewECRClient(cfg))
        }
    }
    return &ECRLifecyclePolicyDriver{auth: accounts, apiFactory: factory}
}

func (ECRLifecyclePolicyDriver) ServiceName() string { return ServiceName }

func (d *ECRLifecyclePolicyDriver) apiForAccount(account string) (LifecyclePolicyAPI, error) {
    if d == nil || d.auth == nil || d.apiFactory == nil {
        return nil, fmt.Errorf("ECRLifecyclePolicyDriver is not configured with an auth registry")
    }
    awsCfg, err := d.auth.Resolve(account)
    if err != nil {
        return nil, fmt.Errorf("resolve ECR account %q: %w", account, err)
    }
    return d.apiFactory(awsCfg), nil
}
```

### Provision Handler

```go
func (d *ECRLifecyclePolicyDriver) Provision(ctx restate.ObjectContext, spec ECRLifecyclePolicySpec) (ECRLifecyclePolicyOutputs, error) {
    key := restate.Key(ctx)

    restate.Set(ctx, drivers.StateKey, ECRLifecyclePolicyState{
        Desired: spec,
        Status:  types.StatusProvisioning,
    })

    api := d.apiFactory(spec.Region)

    // PutLifecyclePolicy is an upsert — create or replace unconditionally
    registryId, err := restate.Run(ctx, func(ctx restate.RunContext) (string, error) {
        return api.PutLifecyclePolicy(ctx, spec.RepositoryName, spec.LifecyclePolicyText)
    })
    if err != nil {
        return ECRLifecyclePolicyOutputs{}, err
    }

    // Resolve the repository ARN for outputs
    repoArn, _, err := restate.Run(ctx, func(ctx restate.RunContext) ([2]string, error) {
        arn, rid, err := api.DescribeRepositoryArn(ctx, spec.RepositoryName)
        return [2]string{arn, rid}, err
    })
    if err != nil {
        // Non-fatal: ARN resolution failure should not block policy application
        repoArn = [2]string{"", registryId}
    }

    outputs := ECRLifecyclePolicyOutputs{
        RepositoryArn:  repoArn[0],
        RepositoryName: spec.RepositoryName,
        RegistryId:     registryId,
    }

    restate.Set(ctx, drivers.StateKey, ECRLifecyclePolicyState{
        Desired:  spec,
        Outputs:  outputs,
        Status:   types.StatusReady,
        Generation: restate.Get[ECRLifecyclePolicyState](ctx, drivers.StateKey).Generation + 1,
    })

    _ = key
    return outputs, nil
}
```

### Delete Handler

```go
func (d *ECRLifecyclePolicyDriver) Delete(ctx restate.ObjectContext) error {
    state, err := restate.Get[*ECRLifecyclePolicyState](ctx, drivers.StateKey)
    if err != nil {
        return err
    }
    if state == nil {
        return nil // already deleted
    }

    api := d.apiFactory(state.Desired.Region)

    if _, err := restate.Run(ctx, func(ctx restate.RunContext) (struct{}, error) {
        return struct{}{}, api.DeleteLifecyclePolicy(ctx, state.Desired.RepositoryName)
    }); err != nil {
        return err
    }

    restate.Clear(ctx, drivers.StateKey)
    return nil
}
```

### Import Handler

```go
func (d *ECRLifecyclePolicyDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (ECRLifecyclePolicyOutputs, error) {
    key := restate.Key(ctx)
    parts := strings.SplitN(key, "~", 2)
    region := parts[0]
    repositoryName := parts[1]

    api := d.apiFactory(region)

    observed, err := restate.Run(ctx, func(ctx restate.RunContext) (ObservedState, error) {
        return api.GetLifecyclePolicy(ctx, repositoryName)
    })
    if err != nil {
        return ECRLifecyclePolicyOutputs{}, err
    }

    repoArn, _, _ := restate.Run(ctx, func(ctx restate.RunContext) ([2]string, error) {
        arn, rid, err := api.DescribeRepositoryArn(ctx, repositoryName)
        return [2]string{arn, rid}, err
    })

    importedSpec := ECRLifecyclePolicySpec{
        Region:              region,
        RepositoryName:      repositoryName,
        LifecyclePolicyText: observed.LifecyclePolicyText,
    }

    outputs := ECRLifecyclePolicyOutputs{
        RepositoryArn:  repoArn[0],
        RepositoryName: repositoryName,
        RegistryId:     observed.RegistryId,
    }

    restate.Set(ctx, drivers.StateKey, ECRLifecyclePolicyState{
        Desired:  importedSpec,
        Observed: observed,
        Outputs:  outputs,
        Status:   types.StatusReady,
        Mode:     types.ModeManaged,
    })

    return outputs, nil
}
```

### Reconcile Handler

```go
func (d *ECRLifecyclePolicyDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
    state, err := restate.Get[*ECRLifecyclePolicyState](ctx, drivers.StateKey)
    if err != nil || state == nil {
        return types.ReconcileResult{Status: types.StatusNotFound}, err
    }

    api := d.apiFactory(state.Desired.Region)

    observed, err := restate.Run(ctx, func(ctx restate.RunContext) (ObservedState, error) {
        return api.GetLifecyclePolicy(ctx, state.Desired.RepositoryName)
    })
    if err != nil {
        return types.ReconcileResult{Status: types.StatusDriftDetected}, err
    }

    diffs := ComputeFieldDiffs(state.Desired, observed)
    if len(diffs) == 0 {
        state.Observed = observed
        state.LastReconcile = time.Now().UTC().Format(time.RFC3339)
        restate.Set(ctx, drivers.StateKey, *state)
        return types.ReconcileResult{Status: types.StatusReady}, nil
    }

    if state.Mode == types.ModeObserved {
        state.Status = types.StatusDriftDetected
        state.Observed = observed
        restate.Set(ctx, drivers.StateKey, *state)
        return types.ReconcileResult{
            Status: types.StatusDriftDetected,
            Diffs:  formatDrifts(diffs),
        }, nil
    }

    // Managed mode: re-converge via PutLifecyclePolicy
    if _, err := restate.Run(ctx, func(ctx restate.RunContext) (string, error) {
        return api.PutLifecyclePolicy(ctx, state.Desired.RepositoryName, state.Desired.LifecyclePolicyText)
    }); err != nil {
        return types.ReconcileResult{Status: types.StatusError}, err
    }

    state.Observed = observed
    state.Status = types.StatusReady
    state.LastReconcile = time.Now().UTC().Format(time.RFC3339)
    restate.Set(ctx, drivers.StateKey, *state)
    return types.ReconcileResult{Status: types.StatusReady, Diffs: formatDrifts(diffs)}, nil
}
```

---

## Step 7 — Provider Adapter

**File**: `internal/core/provider/ecrlifecyclepolicy_adapter.go`

```go
package provider

import (
    "encoding/json"
    "fmt"

    "github.com/aws/aws-sdk-go-v2/aws"

    "github.com/shirvan/praxis/internal/core/auth"
    "github.com/shirvan/praxis/internal/drivers/ecrpolicy"
    "github.com/shirvan/praxis/internal/infra/awsclient"
)

const ecrLifecyclePolicyKind = "ECRLifecyclePolicy"

type ECRLifecyclePolicyAdapter struct {
    auth       authservice.AuthClient
    apiFactory func(aws.Config) ecrpolicy.LifecyclePolicyAPI
}

func NewECRLifecyclePolicyAdapterWithAuth(auth authservice.AuthClient) *ECRLifecyclePolicyAdapter {
    if accounts == nil {
        auth = authservice.NewAuthClient()
    }
    return &ECRLifecyclePolicyAdapter{
        auth: accounts,
        apiFactory: func(cfg aws.Config) ecrpolicy.LifecyclePolicyAPI {
            return ecrpolicy.NewLifecyclePolicyAPI(awsclient.NewECRClient(cfg))
        },
    }
}

func (a *ECRLifecyclePolicyAdapter) Kind() string        { return ecrLifecyclePolicyKind }
func (a *ECRLifecyclePolicyAdapter) ServiceName() string { return ecrpolicy.ServiceName }
func (a *ECRLifecyclePolicyAdapter) Scope() KeyScope     { return KeyScopeCustom }

// BuildKey builds the Virtual Object key from spec.region and spec.repositoryName.
// metadata.name is NOT used — the key is derived from the repository reference.
func (a *ECRLifecyclePolicyAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
    var doc struct {
        Spec struct {
            Region         string `json:"region"`
            RepositoryName string `json:"repositoryName"`
        } `json:"spec"`
    }
    if err := json.Unmarshal(resourceDoc, &doc); err != nil {
        return "", fmt.Errorf("ECRLifecyclePolicyAdapter.BuildKey: %w", err)
    }
    if doc.Spec.Region == "" || doc.Spec.RepositoryName == "" {
        return "", fmt.Errorf("ECRLifecyclePolicyAdapter.BuildKey: spec.region and spec.repositoryName are required")
    }
    return JoinKey(doc.Spec.Region, doc.Spec.RepositoryName), nil
}

// BuildImportKey builds the key from region and repositoryName (the resourceID).
func (a *ECRLifecyclePolicyAdapter) BuildImportKey(region, resourceID string) (string, error) {
    return JoinKey(region, resourceID), nil
}

func (a *ECRLifecyclePolicyAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
    return decodeSpec[ecrpolicy.ECRLifecyclePolicySpec](resourceDoc)
}

// Provision, Delete, NormalizeOutputs, Plan, Import follow the standard adapter
// pattern — see S3Adapter for the full implementation reference.
```

### Adapter Design Note

The adapter uses `KeyScopeCustom` because the key is not simply `region~metadata.name`.
The repository name comes from `spec.repositoryName` — a cross-reference to another
resource. This distinction ensures the orchestrator correctly resolves the dependency
edge: the `ECRLifecyclePolicy` resource's key depends on `ECRRepository` outputs
before it can be built.

---

## Step 8 — Registry Integration

**File**: `internal/core/provider/registry.go`

```go
func NewRegistry() *Registry {
    auth := authservice.NewAuthClient()
    return NewRegistryWithAdapters(
        // ... existing adapters ...
        NewECRRepositoryAdapterWithAuth(auth),
        NewECRLifecyclePolicyAdapterWithAuth(auth),
    )
}
```

---

## Step 9 — Compute Driver Pack Entry Point

**File**: `cmd/praxis-compute/main.go`

```go
import (
    // ... existing imports ...
    ecrpolicy "github.com/shirvan/praxis/internal/drivers/ecrpolicy"
)

srv := server.NewRestate().
    // ... existing bindings ...
    Bind(restate.Reflect(ecrrepo.NewECRRepositoryDriver(auth))).
    Bind(restate.Reflect(ecrpolicy.NewECRLifecyclePolicyDriver(auth)))
```

---

## Step 10 — Docker Compose & Justfile

### docker-compose.yaml

No new service stanza required — both ECR drivers run in the existing `praxis-compute`
service. Ensure `ecr` appears in LocalStack SERVICES (added once for the ECR
Repository driver).

### justfile

```makefile
test-ecr-lifecycle-policy:
    go test ./internal/drivers/ecrpolicy/... -v -timeout 120s

test-ecr-lcp-integration:
    go test ./tests/integration/ -run TestECRLifecyclePolicy -v -timeout 300s
```

---

## Step 11 — Unit Tests

### driver_test.go — Key Test Cases

```text
TestProvision_CreateNew             — puts policy on a repository (policy does not exist)
TestProvision_Update                — replaces policy on a repository (policy already exists)
TestProvision_IdempotentSamePolicy  — PutLifecyclePolicy called; JSON semantic equality confirmed
TestProvision_InvalidJson           — invalid policy JSON → terminal error from AWS
TestProvision_RepoNotFound          — repository does not exist → terminal error
TestDelete_Existing                 — deletes an existing lifecycle policy
TestDelete_NotFound                 — no-op when policy does not exist
TestDelete_AlreadyDeleted           — no-op when state is nil
TestImport_Existing                 — imports an existing lifecycle policy
TestImport_NotFound                 — terminal error when no policy set
TestReconcile_NoDrift               — no drift → StatusReady
TestReconcile_DriftObservedMode     — drift detected → StatusDriftDetected (no correction)
TestReconcile_DriftManagedMode      — drift detected + managed mode → re-converges
TestGetStatus_Ready                 — returns Ready status
TestGetOutputs_FullOutputs          — returns repositoryArn, repositoryName, registryId
```

### aws_test.go — Error Classification

```text
TestClassifyError_RepoNotFound         — RepositoryNotFoundException → terminal 404
TestClassifyError_PolicyNotFound       — LifecyclePolicyNotFoundException → terminal 404
TestClassifyError_InvalidParam         — InvalidParameterException → terminal 400
TestClassifyError_ServerException      — ServerException → retryable
```

### drift_test.go — Drift Cases

```text
TestHasDrift_NoDrift                  — identical policy text → false
TestHasDrift_DifferentRules           — different rules → true
TestHasDrift_JSONSemanticallyEqual    — reordered JSON keys → false (no drift)
TestHasDrift_EmptyVsNil               — empty string vs empty JSON → false
TestComputeFieldDiffs_PolicyChanged   — returns one diff entry for lifecyclePolicyText
TestComputeFieldDiffs_NoDrift         — returns empty slice
```

---

## Step 12 — Integration Tests

**File**: `tests/integration/ecr_lifecycle_policy_driver_test.go`

```go
func TestECRLifecyclePolicy_FullLifecycle(t *testing.T) {
    // 1. Provision an ECR repository (prerequisite)
    // 2. Provision lifecycle policy with untagged-image expiry rule
    // 3. Verify policy is set via GetLifecyclePolicy
    // 4. Update policy (add a second rule)
    // 5. Verify policy is replaced
    // 6. Delete policy
    // 7. Verify policy no longer exists (GetLifecyclePolicy returns not-found)
}

func TestECRLifecyclePolicy_Idempotency(t *testing.T) {
    // 1. Provision lifecycle policy
    // 2. Provision the same policy again (same JSON)
    // 3. Verify PutLifecyclePolicy was called both times (upsert semantics)
    // 4. Verify resulting policy matches spec
}

func TestECRLifecyclePolicy_Import(t *testing.T) {
    // 1. Create repository + policy directly via AWS SDK
    // 2. Import via driver
    // 3. Verify outputs match AWS state
    // 4. Reconcile — should report no drift
}

func TestECRLifecyclePolicy_RepoNotFoundError(t *testing.T) {
    // 1. Provision lifecycle policy with a non-existent repositoryName
    // 2. Expect terminal error (404)
}

func TestECRLifecyclePolicy_JSONSemanticEquality(t *testing.T) {
    // 1. Provision with JSON policy (keys in one order)
    // 2. Reconcile with same policy (keys in different order — e.g., AWS normalizes)
    // 3. Verify no drift is detected
}
```

---

## ECR-LifecyclePolicy-Specific Design Decisions

### 1. PutLifecyclePolicy as the Universal Write Path

Unlike most AWS resources where create and update are separate API calls,
`PutLifecyclePolicy` is a full replacement upsert. The driver does not need a
"does this policy exist?" check before calling it — `PutLifecyclePolicy` always
succeeds if the repository exists. This significantly simplifies the `Provision`
handler compared to drivers that must branch between `Create` and `Update`.

### 2. KeyScopeCustom with `spec.repositoryName`

The lifecycle policy Virtual Object is keyed on `region~repositoryName`, not
`region~metadata.name`. This means:

- Two templates cannot have two different lifecycle policies for the same repository —
  they would target the same Virtual Object key, and the second Provision would
  overwrite the first policy.
- The orchestrator's DAG will automatically make the lifecycle policy depend on the
  ECR repository (because `spec.repositoryName` references `${resources.*.outputs.repositoryName}`).
- `BuildKey` reads from `spec.repositoryName` (not `metadata.name`), matching the
  `LambdaPermission` and `SNSSubscription` adapter patterns.

### 3. Drift Detection is JSON Semantic Only

AWS may return the lifecycle policy text with normalized formatting (e.g., compacted
JSON, normalized whitespace). The driver uses JSON semantic equality (unmarshal →
re-marshal → compare) rather than string equality. This prevents false drift alerts
from cosmetic differences.

### 4. `RepositoryArn` in Outputs is Derived

Lifecycle policies themselves have no ARN. The `repositoryArn` in outputs is resolved
by calling `DescribeRepositories` during Provision. This provides a convenient
cross-reference for downstream IAM policies or monitoring configurations that need
to reference the repository. If `DescribeRepositoryArn` fails (e.g., due to a race
condition), the driver logs the error but does not fail the Provision — the policy
is already set at this point.

### 5. Delete is Idempotent by Design

`DeleteLifecyclePolicy` returns `LifecyclePolicyNotFoundException` when no policy
exists. The `realLifecyclePolicyAPI.DeleteLifecyclePolicy` implementation treats
this as a success (no-op) to ensure idempotent Delete behavior. This matches the
patterns in the Lambda Permission and SNS Subscription drivers.

---

## Checklist

- [x] CUE schema (`schemas/aws/ecr/lifecycle_policy.cue`)
- [x] Driver types (`internal/drivers/ecrpolicy/types.go`)
- [x] AWS API abstraction (`internal/drivers/ecrpolicy/aws.go`)
- [x] Drift detection (`internal/drivers/ecrpolicy/drift.go`)
- [x] Driver Virtual Object (`internal/drivers/ecrpolicy/driver.go`)
- [x] Unit tests: driver, aws, drift
- [x] Provider adapter (`internal/core/provider/ecrlifecyclepolicy_adapter.go`)
- [x] Adapter unit tests
- [x] Registry integration (`internal/core/provider/registry.go`)
- [x] Entry point bind (`cmd/praxis-compute/main.go`)
- [x] Integration tests (`tests/integration/ecr_lifecycle_policy_driver_test.go`)
- [x] Justfile targets
