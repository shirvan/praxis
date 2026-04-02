# SQS Queue Policy Driver — Implementation Plan

> Target: A Restate Virtual Object driver that manages SQS Queue Policies, providing
> full lifecycle management including creation, import, deletion, drift detection,
> and drift correction for the resource-based access policy attached to an SQS queue.
>
> Key scope: `KeyScopeRegion` — key format is `region~queueName`, permanent and
> immutable for the lifetime of the Virtual Object. The queue URL is resolved from
> the queue name and stored in state.

---

## Table of Contents

1. [Overview & Scope](#1-overview--scope)
2. [Key Strategy](#2-key-strategy)
3. [File Inventory](#3-file-inventory)
4. [Step 1 — CUE Schema](#step-1--cue-schema)
5. [Step 2 — Driver Types](#step-2--driver-types)
6. [Step 3 — AWS API Abstraction Layer](#step-3--aws-api-abstraction-layer)
7. [Step 4 — Drift Detection](#step-4--drift-detection)
8. [Step 5 — Driver Implementation](#step-5--driver-implementation)
9. [Step 6 — Provider Adapter](#step-6--provider-adapter)
10. [Step 7 — Registry Integration](#step-7--registry-integration)
11. [Step 8 — Storage Driver Pack Entry Point](#step-8--storage-driver-pack-entry-point)
12. [Step 9 — Docker Compose & Justfile](#step-9--docker-compose--justfile)
13. [Step 10 — Unit Tests](#step-10--unit-tests)
14. [Step 11 — Integration Tests](#step-11--integration-tests)
15. [SQS-Queue-Policy-Specific Design Decisions](#sqs-queue-policy-specific-design-decisions)
16. [Design Decisions (Resolved)](#design-decisions-resolved)
17. [Checklist](#checklist)

---

## 1. Overview & Scope

The SQS Queue Policy driver manages the resource-based **access policy** on an SQS
queue. The policy is a single JSON document that controls which AWS services,
accounts, or IAM principals can perform SQS actions (`sqs:SendMessage`,
`sqs:ReceiveMessage`, etc.) on the queue.

Queue policies are essential for cross-service integration. When an SNS topic, S3
bucket, or EventBridge rule needs to deliver messages to an SQS queue, a queue
policy must grant the appropriate `sqs:SendMessage` permission to the source
service's principal. Without a policy, cross-service delivery fails with access
denied errors.

The policy is an attribute of the queue (not a separate AWS resource), managed via
`SetQueueAttributes` with the `Policy` attribute name. The driver treats it as a
logically separate resource for Praxis lifecycle purposes — this separation allows
templates to define the policy independently from the queue's core configuration.

**Out of scope**: Queue lifecycle (create, delete) — that's the SQS Queue driver's
responsibility. Message operations, dead-letter queue configuration, encryption
settings, and queue attributes other than `Policy` are all managed by the SQS Queue
driver.

### Resource Scope for This Plan

| In Scope | Out of Scope (SQS Queue Driver) |
|---|---|
| Set queue policy (resource-based IAM policy) | Queue creation / deletion |
| Update queue policy | Queue attributes (visibility, retention, etc.) |
| Remove queue policy | Dead-letter queue (redrive policy) |
| Import existing policy | Encryption (SSE-SQS, SSE-KMS) |
| Drift detection for policy changes | FIFO settings |
| | Tags |

### Driver Contract

| Handler | Context | Purpose |
|---|---|---|
| `Provision` | `ObjectContext` (exclusive) | Set or update the queue policy |
| `Import` | `ObjectContext` (exclusive) | Adopt an existing queue policy |
| `Delete` | `ObjectContext` (exclusive) | Remove the queue policy |
| `Reconcile` | `ObjectContext` (exclusive) | Detect/correct drift (report-only for Observed mode) |
| `GetStatus` | `ObjectSharedContext` (shared) | Return current status |
| `GetOutputs` | `ObjectSharedContext` (shared) | Return policy outputs |

### Mutable vs Immutable Attributes

| Attribute | Mutability | Notes |
|---|---|---|
| `queueName` | Immutable | Part of the Virtual Object key; references the target queue |
| `region` | Immutable | Queue's region; part of the key |
| `policy` | Mutable | The JSON policy document; replaced atomically via `SetQueueAttributes` |

### Downstream Consumers

```text
${resources.my-queue-policy.outputs.queueUrl}    → Informational cross-reference
${resources.my-queue-policy.outputs.queueArn}    → Informational cross-reference
${resources.my-queue-policy.outputs.queueName}   → Informational cross-reference
```

Queue policies do not produce outputs that other resources typically depend on. The
queue URL and ARN are passed through from the queue for informational purposes.

---

## 2. Key Strategy

### Key Scope: `KeyScopeRegion`

Queue policies are 1:1 with queues. There is exactly one policy per queue. The key
follows the same format as the SQS Queue driver: `region~queueName` (e.g.,
`us-east-1~order-processing`).

This means the policy Virtual Object has the same key as its parent queue Virtual
Object. They are separate Restate services (`SQSQueuePolicy` vs `SQSQueue`) but
addressed by the same key. This is intentional and follows the same pattern as other
1:1 resource relationships in the system.

### BuildKey vs BuildImportKey

- **`BuildKey(resourceDoc)`**: Extracts `spec.region` and `spec.queueName` (or
  `metadata.name` if queueName is not specified). Returns `region~queueName`.

- **`BuildImportKey(region, resourceID)`**: Returns `region~resourceID`. The
  `resourceID` is the queue name. If a queue URL is provided, the adapter extracts
  the queue name from the URL's last segment.

### No Ownership Tags

The queue policy is an attribute of the queue, not a separate taggable resource.
There is no tag API for queue policies. Conflict detection relies on the 1:1
relationship — each queue has exactly one policy.

---

## 3. File Inventory

```text
✦ schemas/aws/sqs/queue_policy.cue                       — CUE schema for SQSQueuePolicy
✦ internal/drivers/sqspolicy/types.go                     — Spec, Outputs, ObservedState, State structs
✦ internal/drivers/sqspolicy/aws.go                       — PolicyAPI interface + realPolicyAPI
✦ internal/drivers/sqspolicy/drift.go                     — HasDrift(), ComputeFieldDiffs()
✦ internal/drivers/sqspolicy/driver.go                    — SQSQueuePolicyDriver Virtual Object
✦ internal/drivers/sqspolicy/driver_test.go               — Unit tests for driver (mocked AWS)
✦ internal/drivers/sqspolicy/aws_test.go                  — Unit tests for error classification
✦ internal/drivers/sqspolicy/drift_test.go                — Unit tests for drift detection
✦ internal/core/provider/sqspolicy_adapter.go             — SQSQueuePolicyAdapter implementing provider.Adapter
✦ internal/core/provider/sqspolicy_adapter_test.go        — Unit tests for adapter
✦ tests/integration/sqs_queue_policy_driver_test.go       — Integration tests
✎ internal/infra/awsclient/client.go                      — Uses existing NewSQSClient factory (shared with Queue)
✎ cmd/praxis-storage/main.go                              — Bind SQSQueuePolicy driver
✎ internal/core/provider/registry.go                      — Add NewSQSQueuePolicyAdapter to NewRegistry()
```

---

## Step 1 — CUE Schema

**File**: `schemas/aws/sqs/queue_policy.cue`

```cue
package sqs

#SQSQueuePolicy: {
    apiVersion: "praxis.io/v1"
    kind:       "SQSQueuePolicy"

    metadata: {
        // name is the logical name for this policy within the Praxis template.
        name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9._-]{0,254}$"
        labels: [string]: string
    }

    spec: {
        // region is the AWS region where the queue resides.
        region: string

        // queueName is the name of the SQS queue to attach the policy to.
        // Must reference an existing SQS queue (typically created by the SQSQueue driver).
        queueName: string & =~"^[a-zA-Z0-9_-]{1,80}(\\.fifo)?$"

        // policy is the resource-based access policy document.
        // Standard IAM policy format with Version, Statement array, etc.
        // Controls which principals can perform SQS actions on this queue.
        policy: {
            Version: "2012-10-17" | "2008-10-17" | *"2012-10-17"
            Id?:     string
            Statement: [...{
                Sid?:       string
                Effect:     "Allow" | "Deny"
                Principal:  _  // string | { AWS: string | [...string] } | { Service: string | [...string] } | "*"
                Action:     string | [...string]
                Resource:   string | [...string]
                Condition?: _  // arbitrary condition block
            }]
        }
    }

    outputs?: {
        queueUrl:  string
        queueArn:  string
        queueName: string
    }
}
```

### Key Design Decisions

- **`policy` as structured CUE object**: Unlike the SQS Queue driver's `redrivePolicy`
  (which is a simple struct), the queue policy is a full IAM policy document. The CUE
  schema defines the structure for type safety at template validation time. The driver
  serializes the CUE-validated struct to JSON when calling the SQS API.

- **`queueName` references the target queue**: The policy is applied to a queue
  identified by name. The driver resolves the queue name to a URL at runtime via
  `GetQueueUrl`. This allows the policy and queue to be defined independently in
  templates with DAG-resolved ordering.

- **No `queueUrl` in spec**: The queue URL is an output of the SQS Queue driver,
  not a user-provided input. The policy driver resolves the URL from the queue name.
  This avoids circular dependencies and keeps the template simpler.

- **Policy Version defaults to `2012-10-17`**: The latest IAM policy version. The
  older `2008-10-17` is an outdated version and should not be used.

---

## Step 2 — Driver Types

**File**: `internal/drivers/sqspolicy/types.go`

```go
package sqspolicy

import "github.com/shirvan/praxis/pkg/types"

const ServiceName = "SQSQueuePolicy"

// SQSQueuePolicySpec is the desired state for an SQS queue policy.
type SQSQueuePolicySpec struct {
    Account   string `json:"account,omitempty"`
    Region    string `json:"region"`
    QueueName string `json:"queueName"`
    Policy    string `json:"policy"` // JSON-encoded IAM policy document
    ManagedKey string `json:"managedKey,omitempty"`
}

// SQSQueuePolicyOutputs is produced after provisioning and stored in Restate K/V.
type SQSQueuePolicyOutputs struct {
    QueueUrl  string `json:"queueUrl"`
    QueueArn  string `json:"queueArn"`
    QueueName string `json:"queueName"`
}

// ObservedState captures the actual policy from AWS.
type ObservedState struct {
    QueueUrl string `json:"queueUrl"`
    QueueArn string `json:"queueArn"`
    Policy   string `json:"policy"` // JSON-encoded policy as returned by AWS
}

// SQSQueuePolicyState is the single atomic state object stored under drivers.StateKey.
type SQSQueuePolicyState struct {
    Desired            SQSQueuePolicySpec    `json:"desired"`
    Observed           ObservedState         `json:"observed"`
    Outputs            SQSQueuePolicyOutputs `json:"outputs"`
    Status             types.ResourceStatus  `json:"status"`
    Mode               types.Mode            `json:"mode"`
    Error              string                `json:"error,omitempty"`
    Generation         int64                 `json:"generation"`
    LastReconcile      string                `json:"lastReconcile,omitempty"`
    ReconcileScheduled bool                  `json:"reconcileScheduled"`
}
```

### Why These Fields

- **`Policy` as JSON string in spec**: The CUE schema validates the structure at
  template time. The adapter serializes the validated CUE object to a JSON string
  for the driver spec. Storing as a string simplifies drift detection (JSON semantic
  comparison) and matches the SQS API format.
- **`QueueUrl` in ObservedState**: The URL is needed for all SQS API calls. It's
  resolved once (during provision or import) and cached for subsequent operations.
- **`QueueArn` in ObservedState**: Retrieved from `GetQueueAttributes`. Passed
  through to outputs for informational purposes.

---

## Step 3 — AWS API Abstraction Layer

**File**: `internal/drivers/sqspolicy/aws.go`

### PolicyAPI Interface

```go
type PolicyAPI interface {
    // GetQueueUrl resolves a queue name to its URL.
    GetQueueUrl(ctx context.Context, queueName string) (string, error)

    // GetQueuePolicy returns the current policy on the queue.
    // Returns empty string if no policy is set.
    GetQueuePolicy(ctx context.Context, queueUrl string) (ObservedState, error)

    // SetQueuePolicy sets the resource-based policy on the queue.
    SetQueuePolicy(ctx context.Context, queueUrl, policy string) error

    // RemoveQueuePolicy removes the policy from the queue.
    RemoveQueuePolicy(ctx context.Context, queueUrl string) error
}
```

### realPolicyAPI Implementation

```go
type realPolicyAPI struct {
    client  *sqs.Client
    limiter *ratelimit.Limiter
}

func NewPolicyAPI(client *sqs.Client) PolicyAPI {
    return &realPolicyAPI{
        client:  client,
        limiter: ratelimit.New("sqs", 50, 20),
    }
}
```

### Key Implementation Details

#### `GetQueueUrl`

```go
func (r *realPolicyAPI) GetQueueUrl(ctx context.Context, queueName string) (string, error) {
    out, err := r.client.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{
        QueueName: aws.String(queueName),
    })
    if err != nil {
        return "", err
    }
    return aws.ToString(out.QueueUrl), nil
}
```

#### `GetQueuePolicy`

```go
func (r *realPolicyAPI) GetQueuePolicy(ctx context.Context, queueUrl string) (ObservedState, error) {
    out, err := r.client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
        QueueUrl:       aws.String(queueUrl),
        AttributeNames: []sqstypes.QueueAttributeName{
            sqstypes.QueueAttributeName("Policy"),
            sqstypes.QueueAttributeName("QueueArn"),
        },
    })
    if err != nil {
        return ObservedState{}, err
    }

    return ObservedState{
        QueueUrl: queueUrl,
        QueueArn: out.Attributes["QueueArn"],
        Policy:   out.Attributes["Policy"],
    }, nil
}
```

> **Targeted attribute fetch**: Unlike the SQS Queue driver which requests all
> attributes, the policy driver requests only `Policy` and `QueueArn`. This is more
> efficient and avoids parsing attributes the driver doesn't manage.

#### `SetQueuePolicy`

```go
func (r *realPolicyAPI) SetQueuePolicy(ctx context.Context, queueUrl, policy string) error {
    _, err := r.client.SetQueueAttributes(ctx, &sqs.SetQueueAttributesInput{
        QueueUrl: aws.String(queueUrl),
        Attributes: map[string]string{
            "Policy": policy,
        },
    })
    return err
}
```

> **Atomic policy replacement**: The SQS API replaces the entire policy document
> atomically. There is no statement-level granularity — the driver always writes the
> complete policy.

#### `RemoveQueuePolicy`

```go
func (r *realPolicyAPI) RemoveQueuePolicy(ctx context.Context, queueUrl string) error {
    _, err := r.client.SetQueueAttributes(ctx, &sqs.SetQueueAttributesInput{
        QueueUrl: aws.String(queueUrl),
        Attributes: map[string]string{
            "Policy": "",
        },
    })
    return err
}
```

> **Empty string removes policy**: Setting the `Policy` attribute to an empty string
> removes the queue policy entirely. This is the SQS API's mechanism for policy
> deletion.

### Error Classification

```go
func isNotFound(err error) bool {
    var qne *sqstypes.QueueDoesNotExist
    if errors.As(err, &qne) {
        return true
    }
    return strings.Contains(err.Error(), "QueueDoesNotExist") ||
        strings.Contains(err.Error(), "NonExistentQueue") ||
        strings.Contains(err.Error(), "AWS.SimpleQueueService.NonExistentQueue")
}

func isInvalidInput(err error) bool {
    var iae *sqstypes.InvalidAttributeName
    if errors.As(err, &iae) {
        return true
    }
    var iave *sqstypes.InvalidAttributeValue
    if errors.As(err, &iave) {
        return true
    }
    return strings.Contains(err.Error(), "InvalidAttribute") ||
        strings.Contains(err.Error(), "InvalidParameterValue")
}
```

> **Shared `isNotFound`**: Both SQS drivers use the same `QueueDoesNotExist` error
> for not-found. The policy driver encounters it when the queue itself is deleted
> (the policy disappears with the queue).

---

## Step 4 — Drift Detection

**File**: `internal/drivers/sqspolicy/drift.go`

### Drift-Detectable Fields

| Field | Drift Source | Notes |
|---|---|---|
| `policy` | External change via console/CLI | JSON semantic comparison |

> **Single field drift**: The queue policy is a single JSON document. Drift detection
> compares the desired policy against the observed policy using JSON semantic
> comparison (parse → marshal → compare), identical to the SNS Topic driver's
> `policiesEqual` function.

### HasDrift

```go
func HasDrift(desired SQSQueuePolicySpec, observed ObservedState) bool {
    return !policiesEqual(desired.Policy, observed.Policy)
}
```

### policiesEqual

```go
// policiesEqual compares two JSON policy strings semantically.
// Handles whitespace and key ordering differences.
func policiesEqual(a, b string) bool {
    if a == b {
        return true
    }
    if a == "" || b == "" {
        return false
    }
    var aObj, bObj interface{}
    if json.Unmarshal([]byte(a), &aObj) != nil {
        return a == b
    }
    if json.Unmarshal([]byte(b), &bObj) != nil {
        return a == b
    }
    aNorm, _ := json.Marshal(aObj)
    bNorm, _ := json.Marshal(bObj)
    return string(aNorm) == string(bNorm)
}
```

### ComputeFieldDiffs

```go
func ComputeFieldDiffs(desired SQSQueuePolicySpec, observed ObservedState) []types.FieldDiff {
    var diffs []types.FieldDiff
    if !policiesEqual(desired.Policy, observed.Policy) {
        diffs = append(diffs, types.FieldDiff{
            Field:    "policy",
            Desired:  desired.Policy,
            Observed: observed.Policy,
        })
    }
    return diffs
}
```

---

## Step 5 — Driver Implementation

**File**: `internal/drivers/sqspolicy/driver.go`

### Constructor

```go
type SQSQueuePolicyDriver struct {
    accounts   *auth.Registry
    apiFactory func(aws.Config) PolicyAPI
}

func NewSQSQueuePolicyDriver(accounts *auth.Registry) *SQSQueuePolicyDriver {
    return &SQSQueuePolicyDriver{accounts: accounts}
}

func NewSQSQueuePolicyDriverWithFactory(accounts *auth.Registry, factory func(aws.Config) PolicyAPI) *SQSQueuePolicyDriver {
    return &SQSQueuePolicyDriver{accounts: accounts, apiFactory: factory}
}

func (SQSQueuePolicyDriver) ServiceName() string { return ServiceName }
```

### Provision

Provision handles three cases:

1. **New policy**: Resolve queue URL, set the policy.
2. **Unchanged policy**: Return existing outputs (idempotent).
3. **Changed policy**: Replace the policy document.

```go
func (d *SQSQueuePolicyDriver) Provision(ctx restate.ObjectContext, spec SQSQueuePolicySpec) (SQSQueuePolicyOutputs, error) {
    state, _ := restate.Get[*SQSQueuePolicyState](ctx, drivers.StateKey)
    api := d.buildAPI(spec.Account, spec.Region)

    // If existing state and spec hasn't changed, return early
    if state != nil && state.Outputs.QueueUrl != "" && !specChanged(spec, state.Desired) {
        return state.Outputs, nil
    }

    // Write pending state
    newState := &SQSQueuePolicyState{
        Desired:    spec,
        Status:     types.StatusProvisioning,
        Mode:       drivers.DefaultMode(""),
        Generation: stateGeneration(state) + 1,
    }
    restate.Set(ctx, drivers.StateKey, newState)

    // Resolve queue URL
    var queueUrl string
    if state != nil && state.Outputs.QueueUrl != "" {
        queueUrl = state.Outputs.QueueUrl
    } else {
        url, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
            return api.GetQueueUrl(rc, spec.QueueName)
        })
        if err != nil {
            if isNotFound(err) {
                return SQSQueuePolicyOutputs{}, restate.TerminalError(
                    fmt.Errorf("queue %q not found in %s — create the queue before setting its policy", spec.QueueName, spec.Region), 404)
            }
            return SQSQueuePolicyOutputs{}, err
        }
        queueUrl = url
    }

    // Set the policy
    if _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
        return restate.Void{}, api.SetQueuePolicy(rc, queueUrl, spec.Policy)
    }); err != nil {
        if isInvalidInput(err) {
            return SQSQueuePolicyOutputs{}, restate.TerminalError(
                fmt.Errorf("invalid policy document: %w", err), 400)
        }
        return SQSQueuePolicyOutputs{}, err
    }

    // Get observed state (confirms policy was set and gets queue ARN)
    observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
        return api.GetQueuePolicy(rc, queueUrl)
    })
    if err != nil {
        observed = ObservedState{QueueUrl: queueUrl}
    }

    outputs := SQSQueuePolicyOutputs{
        QueueUrl:  queueUrl,
        QueueArn:  observed.QueueArn,
        QueueName: spec.QueueName,
    }

    newState.Observed = observed
    newState.Outputs = outputs
    newState.Status = types.StatusReady
    newState.Error = ""
    restate.Set(ctx, drivers.StateKey, newState)

    d.scheduleReconcile(ctx)
    return outputs, nil
}
```

### Import

```go
func (d *SQSQueuePolicyDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (SQSQueuePolicyOutputs, error) {
    api := d.buildAPI(ref.Account, ref.Region)

    // ResourceID is the queue name
    queueUrl, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
        return api.GetQueueUrl(rc, ref.ResourceID)
    })
    if err != nil {
        if isNotFound(err) {
            return SQSQueuePolicyOutputs{}, restate.TerminalError(
                fmt.Errorf("queue %q not found in %s", ref.ResourceID, ref.Region), 404)
        }
        return SQSQueuePolicyOutputs{}, err
    }

    observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
        return api.GetQueuePolicy(rc, queueUrl)
    })
    if err != nil {
        if isNotFound(err) {
            return SQSQueuePolicyOutputs{}, restate.TerminalError(
                fmt.Errorf("queue %q not found", ref.ResourceID), 404)
        }
        return SQSQueuePolicyOutputs{}, err
    }

    if observed.Policy == "" {
        return SQSQueuePolicyOutputs{}, restate.TerminalError(
            fmt.Errorf("queue %q has no policy to import", ref.ResourceID), 404)
    }

    spec := SQSQueuePolicySpec{
        Account:   ref.Account,
        Region:    ref.Region,
        QueueName: ref.ResourceID,
        Policy:    observed.Policy,
    }

    outputs := SQSQueuePolicyOutputs{
        QueueUrl:  queueUrl,
        QueueArn:  observed.QueueArn,
        QueueName: ref.ResourceID,
    }

    mode := types.ModeObserved
    if ref.Mode != "" {
        mode = ref.Mode
    }

    restate.Set(ctx, drivers.StateKey, &SQSQueuePolicyState{
        Desired:    spec,
        Observed:   observed,
        Outputs:    outputs,
        Status:     types.StatusReady,
        Mode:       mode,
        Generation: 1,
    })

    d.scheduleReconcile(ctx)
    return outputs, nil
}
```

### Delete

```go
func (d *SQSQueuePolicyDriver) Delete(ctx restate.ObjectContext) error {
    state, err := restate.Get[*SQSQueuePolicyState](ctx, drivers.StateKey)
    if err != nil {
        return err
    }
    if state == nil {
        return nil
    }
    if state.Mode == types.ModeObserved {
        return restate.TerminalError(fmt.Errorf("cannot delete observed resource"), 403)
    }

    state.Status = types.StatusDeleting
    restate.Set(ctx, drivers.StateKey, state)

    api := d.buildAPI(state.Desired.Account, state.Desired.Region)

    if _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
        return restate.Void{}, api.RemoveQueuePolicy(rc, state.Outputs.QueueUrl)
    }); err != nil {
        if !isNotFound(err) {
            return err
        }
        // Queue was deleted externally — policy is already gone
    }

    state.Status = types.StatusDeleted
    restate.Set(ctx, drivers.StateKey, state)
    restate.Clear(ctx, drivers.StateKey)

    return nil
}
```

### Reconcile

```go
func (d *SQSQueuePolicyDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
    state, err := restate.Get[*SQSQueuePolicyState](ctx, drivers.StateKey)
    if err != nil {
        return types.ReconcileResult{}, err
    }
    if state == nil {
        return types.ReconcileResult{Status: "no-state"}, nil
    }

    state.ReconcileScheduled = false
    api := d.buildAPI(state.Desired.Account, state.Desired.Region)

    observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
        return api.GetQueuePolicy(rc, state.Outputs.QueueUrl)
    })
    if err != nil {
        if isNotFound(err) {
            state.Status = types.StatusError
            state.Error = "queue not found — may have been deleted externally"
            state.Observed = ObservedState{}
            restate.Set(ctx, drivers.StateKey, state)
            d.scheduleReconcile(ctx)
            return types.ReconcileResult{Status: "error", Error: state.Error}, nil
        }
        return types.ReconcileResult{}, err
    }

    state.Observed = observed
    state.LastReconcile = time.Now().UTC().Format(time.RFC3339)

    if !HasDrift(state.Desired, observed) {
        state.Status = types.StatusReady
        state.Error = ""
        restate.Set(ctx, drivers.StateKey, state)
        d.scheduleReconcile(ctx)
        return types.ReconcileResult{Status: "ok"}, nil
    }

    diffs := ComputeFieldDiffs(state.Desired, observed)
    result := types.ReconcileResult{
        Status: "drift-detected",
        Drifts: diffs,
    }

    if state.Mode == types.ModeManaged {
        // Correct drift: replace the policy
        if _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
            return restate.Void{}, api.SetQueuePolicy(rc, state.Outputs.QueueUrl, state.Desired.Policy)
        }); err != nil {
            state.Error = fmt.Sprintf("drift correction failed: %v", err)
            state.Status = types.StatusError
            restate.Set(ctx, drivers.StateKey, state)
            d.scheduleReconcile(ctx)
            return types.ReconcileResult{Status: "error", Error: state.Error}, nil
        }

        result.Status = "drift-corrected"
        state.Status = types.StatusReady
        state.Error = ""
    }

    restate.Set(ctx, drivers.StateKey, state)
    d.scheduleReconcile(ctx)
    return result, nil
}
```

### GetStatus / GetOutputs (Shared Handlers)

Follow the standard pattern (identical to all other drivers).

---

## Step 6 — Provider Adapter

**File**: `internal/core/provider/sqspolicy_adapter.go`

```go
type SQSQueuePolicyAdapter struct {
    accounts *auth.Registry
}

func NewSQSQueuePolicyAdapterWithRegistry(accounts *auth.Registry) *SQSQueuePolicyAdapter {
    return &SQSQueuePolicyAdapter{accounts: accounts}
}

func (a *SQSQueuePolicyAdapter) Kind() string        { return sqspolicy.ServiceName }
func (a *SQSQueuePolicyAdapter) ServiceName() string  { return sqspolicy.ServiceName }
func (a *SQSQueuePolicyAdapter) Scope() KeyScope      { return KeyScopeRegion }

func (a *SQSQueuePolicyAdapter) BuildKey(doc json.RawMessage) (string, error) {
    var parsed struct {
        Spec struct {
            Region    string `json:"region"`
            QueueName string `json:"queueName"`
        } `json:"spec"`
        Metadata struct {
            Name string `json:"name"`
        } `json:"metadata"`
    }
    if err := json.Unmarshal(doc, &parsed); err != nil {
        return "", err
    }
    region := parsed.Spec.Region
    queueName := parsed.Spec.QueueName
    if queueName == "" {
        queueName = parsed.Metadata.Name
    }
    if region == "" || queueName == "" {
        return "", fmt.Errorf("SQSQueuePolicy requires spec.region and spec.queueName (or metadata.name)")
    }
    return region + "~" + queueName, nil
}

func (a *SQSQueuePolicyAdapter) BuildImportKey(region, resourceID string) (string, error) {
    // resourceID is the queue name or URL
    queueName := resourceID
    if strings.HasPrefix(resourceID, "https://") || strings.HasPrefix(resourceID, "http://") {
        parts := strings.Split(resourceID, "/")
        if len(parts) > 0 {
            queueName = parts[len(parts)-1]
        }
    }
    return region + "~" + queueName, nil
}
```

### DecodeSpec

The adapter's `DecodeSpec` method deserializes the template document and converts the
structured policy object to a JSON string for the driver spec:

```go
func (a *SQSQueuePolicyAdapter) DecodeSpec(doc json.RawMessage) (any, error) {
    var parsed struct {
        Spec struct {
            Region    string          `json:"region"`
            QueueName string          `json:"queueName"`
            Policy    json.RawMessage `json:"policy"`
        } `json:"spec"`
        Metadata struct {
            Name string `json:"name"`
        } `json:"metadata"`
    }
    if err := json.Unmarshal(doc, &parsed); err != nil {
        return nil, err
    }

    queueName := parsed.Spec.QueueName
    if queueName == "" {
        queueName = parsed.Metadata.Name
    }

    return sqspolicy.SQSQueuePolicySpec{
        Region:    parsed.Spec.Region,
        QueueName: queueName,
        Policy:    string(parsed.Spec.Policy),
    }, nil
}
```

> **Policy serialization**: The CUE schema defines `policy` as a structured object.
> The adapter receives it as `json.RawMessage` and passes the raw JSON string to the
> driver spec. This avoids intermediate parsing — the JSON is validated by CUE at
> template time and stored verbatim.

---

## Step 7 — Registry Integration

**File**: `internal/core/provider/registry.go` — **MODIFY**

Add `NewSQSQueuePolicyAdapterWithRegistry(accounts)` to `NewRegistry()`.

---

## Step 8 — Storage Driver Pack Entry Point

**File**: `cmd/praxis-storage/main.go` — **MODIFY**

```go
import "github.com/shirvan/praxis/internal/drivers/sqspolicy"

Bind(restate.Reflect(sqspolicy.NewSQSQueuePolicyDriver(cfg.Auth())))
```

---

## Step 9 — Docker Compose & Justfile

### Docker Compose

No additional changes needed — `sqs` is already added to LocalStack's `SERVICES`
by the SQS Queue driver. The queue policy driver shares the same SQS API endpoint.

### Justfile Additions

```just
test-sqspolicy:
    go test ./internal/drivers/sqspolicy/... -v -count=1 -race

test-sqspolicy-integration:
    go test ./tests/integration/ -run TestSQSQueuePolicy -v -count=1 -tags=integration -timeout=5m

test-sqs-all:
    go test ./internal/drivers/sqs/... ./internal/drivers/sqspolicy/... \
            -v -count=1 -race
```

---

## Step 10 — Unit Tests

**File**: `internal/drivers/sqspolicy/driver_test.go`

| Test | Description |
|---|---|
| `TestProvision_NewPolicy` | Sets policy on queue; verifies outputs and state |
| `TestProvision_NoChange` | Same spec; verifies idempotent return |
| `TestProvision_UpdatePolicy` | Changed policy; verifies policy replacement |
| `TestProvision_QueueNotFound` | Queue doesn't exist; verifies 404 |
| `TestProvision_InvalidPolicy` | Malformed policy; verifies 400 |
| `TestImport_Success` | Imports existing policy; verifies state |
| `TestImport_NoPolicyExists` | Queue has no policy; verifies 404 |
| `TestImport_QueueNotFound` | Queue doesn't exist; verifies 404 |
| `TestDelete_Managed` | Removes policy; verifies cleanup |
| `TestDelete_Observed` | Cannot delete observed; verifies 403 |
| `TestDelete_QueueGone` | Queue already deleted; verifies idempotent |
| `TestReconcile_NoDrift` | Policy matches; verifies ok |
| `TestReconcile_PolicyDrifted` | Policy changed externally; verifies drift correction |
| `TestReconcile_PolicyRemoved` | Policy removed externally; verifies drift correction |
| `TestReconcile_QueueDeleted` | Queue deleted externally; verifies error state |
| `TestServiceName` | Returns `"SQSQueuePolicy"` |

**File**: `internal/drivers/sqspolicy/drift_test.go`

| Test | Description |
|---|---|
| `TestHasDrift_NoDrift` | Policies match; no drift |
| `TestHasDrift_PolicyChanged` | Policy JSON differs (semantic comparison); drift detected |
| `TestHasDrift_PolicyWhitespace` | Policy JSON differs only in whitespace; no drift |
| `TestHasDrift_PolicyKeyOrder` | Policy JSON differs in key ordering; no drift |
| `TestHasDrift_PolicyRemoved` | Observed policy empty; drift detected |
| `TestHasDrift_PolicyAdded` | Desired has policy, observed empty; drift detected |
| `TestPoliciesEqual_BothEmpty` | Both empty → equal |
| `TestPoliciesEqual_OneEmpty` | One empty → not equal |
| `TestPoliciesEqual_InvalidJson` | Invalid JSON → falls back to string comparison |
| `TestComputeFieldDiffs_NoDrift` | No diffs → empty slice |
| `TestComputeFieldDiffs_PolicyDrifted` | Policy differs → single diff entry |

**File**: `internal/drivers/sqspolicy/aws_test.go`

| Test | Description |
|---|---|
| `TestIsNotFound_QueueDoesNotExist` | Validates QueueDoesNotExist classification |
| `TestIsNotFound_StringFallback` | Validates string fallback for NonExistentQueue |
| `TestIsInvalidInput_AttributeName` | Validates InvalidAttributeName classification |
| `TestIsInvalidInput_AttributeValue` | Validates InvalidAttributeValue classification |

---

## Step 11 — Integration Tests

**File**: `tests/integration/sqs_queue_policy_driver_test.go`

| Test | Description |
|---|---|
| `TestSQSQueuePolicy_SetPolicy` | Create queue, set policy, verify policy in AWS |
| `TestSQSQueuePolicy_UpdatePolicy` | Set policy, update it, verify new policy |
| `TestSQSQueuePolicy_ImportExisting` | Set policy via AWS API, import, verify state |
| `TestSQSQueuePolicy_ImportNoPolicy` | Queue with no policy; import fails with 404 |
| `TestSQSQueuePolicy_Delete` | Set policy then delete, verify policy removed |
| `TestSQSQueuePolicy_Reconcile` | Set policy, externally change it, reconcile in managed mode |
| `TestSQSQueuePolicy_SNSAccess` | Create queue + SNS topic, set policy allowing SNS, verify configuration |
| `TestSQSQueuePolicy_S3Access` | Create queue, set policy allowing S3 notifications, verify configuration |

### LocalStack Considerations

- LocalStack supports the `Policy` attribute via `GetQueueAttributes` and
  `SetQueueAttributes`.
- Setting an empty string policy should remove the policy attribute.
- Policy validation may be less strict in LocalStack compared to real AWS — invalid
  principal ARNs or actions may be accepted. Integration tests should focus on
  round-trip verification (set → get → compare) rather than policy evaluation.
- LocalStack already includes `sqs` in the shared `SERVICES` list used by the integration suite.

---

## SQS-Queue-Policy-Specific Design Decisions

### 1. Policy as a Single Atomic Document

**Decision**: The driver replaces the entire policy document atomically. There is no
statement-level API for adding, removing, or modifying individual statements.

**Rationale**: The SQS API provides only `SetQueueAttributes` with the `Policy`
attribute — a single string containing the entire JSON policy. There is no
`AddStatement` or `RemoveStatement` API. The driver always writes the complete
policy. Partial updates must be handled at the template level (by modifying the
`Statement` array in CUE).

### 2. Empty String Removes Policy

**Decision**: The `Delete` handler sets the `Policy` attribute to an empty string
to remove the policy.

**Rationale**: SQS does not have a dedicated `DeleteQueuePolicy` API. Setting the
`Policy` attribute to an empty string effectively removes the resource-based policy,
reverting the queue to its default access model (only the queue owner has access).
This is the standard SQS mechanism for policy removal.

### 3. Queue Must Exist Before Policy

**Decision**: `Provision` fails with 404 if the target queue does not exist.

**Rationale**: The policy is an attribute of the queue — it cannot exist without the
queue. The DAG scheduler ensures queue creation before policy creation in templates.
If the queue doesn't exist, it's a configuration error, not a transient condition.
A terminal 404 error surfaces this clearly.

### 4. JSON Semantic Policy Comparison

**Decision**: Drift detection uses JSON semantic comparison (parse → normalize →
compare), identical to the SNS Topic driver's policy comparison.

**Rationale**: AWS may return the policy with different key ordering or whitespace
than the user-provided JSON. String comparison would produce false drift detections.
Semantic comparison normalizes both sides before comparing.

### 5. No Ownership Tags

**Decision**: The queue policy driver does not use `praxis:managed-key` tags.

**Rationale**: A queue policy is not a separate taggable resource — it's an attribute
of the queue. There is no tag API for the policy itself. The 1:1 relationship between
queue and policy (enforced by the key format) prevents conflicts.

### 6. Shared SQS Rate Limiter Namespace

**Decision**: The policy driver shares the `"sqs"` rate limiter namespace with the
SQS Queue driver.

**Rationale**: Both drivers call the same SQS API endpoints (`GetQueueAttributes`,
`SetQueueAttributes`). Separate rate limiter namespaces could allow aggregate
throttling to exceed SQS account limits. A shared namespace ensures total SQS API
usage stays within bounds.

### 7. Import Requires Existing Policy

**Decision**: `Import` fails with 404 if the queue has no policy.

**Rationale**: Importing a "no policy" state is meaningless — there's nothing to
observe or manage. If the user wants to manage a queue's policy that doesn't exist
yet, they should use `Provision`, not `Import`.

### 8. Queue Deletion Cascades Policy

**Decision**: If the queue is deleted externally, the policy driver enters error
state ("queue not found") during reconciliation.

**Rationale**: The policy cannot exist without the queue. When the queue is deleted,
the policy is implicitly removed. The driver should not attempt to recreate the
queue — that's the SQS Queue driver's responsibility. The error state alerts the
operator that the dependent queue is missing.

---

## Design Decisions (Resolved)

### Key Scope

**Decision**: `KeyScopeRegion` with key format `region~queueName`.

**Rationale**: The policy is 1:1 with the queue. Using the same key format as the
SQS Queue driver is the natural choice. The queue name is unique per account+region.

### Runtime Pack

**Decision**: SQS Queue Policy is hosted in `praxis-storage`.

**Rationale**: Same runtime pack as the SQS Queue driver. Both use the same SQS
SDK client and share rate limiter namespace.

### Default Import Mode

**Decision**: Import defaults to `ModeObserved`.

**Rationale**: Queue policies often control access for multiple services (SNS, S3,
Lambda). Importing as observed prevents accidental policy changes that could break
cross-service integrations. Users can override to `ModeManaged` if they want Praxis
to take ownership and correct drift.

---

## Checklist

### Implementation

- [x] `schemas/aws/sqs/queue_policy.cue`
- [x] `internal/drivers/sqspolicy/types.go`
- [x] `internal/drivers/sqspolicy/aws.go`
- [x] `internal/drivers/sqspolicy/drift.go`
- [x] `internal/drivers/sqspolicy/driver.go`
- [x] `internal/core/provider/sqspolicy_adapter.go`

### Tests

- [x] `internal/drivers/sqspolicy/driver_test.go`
- [x] `internal/drivers/sqspolicy/aws_test.go`
- [x] `internal/drivers/sqspolicy/drift_test.go`
- [x] `internal/core/provider/sqspolicy_adapter_test.go`
- [x] `tests/integration/sqs_queue_policy_driver_test.go`

### Integration

- [x] `cmd/praxis-storage/main.go` — Bind driver
- [x] `internal/core/provider/registry.go` — Register adapter
