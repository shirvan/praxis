# Drivers

> **See also:** [Architecture](ARCHITECTURE.md) | [Orchestrator](ORCHESTRATOR.md) | [Templates](TEMPLATES.md) | [Developers](DEVELOPERS.md)

---

## Overview

A Praxis driver manages the lifecycle of a single cloud resource type. The S3 driver manages S3 buckets. The SecurityGroup driver manages EC2 security groups. Each driver is a standalone Restate Virtual Object service — an independent binary that registers with Restate and communicates with Praxis Core via durable RPC.

Drivers are intentionally simple. They know how to create, read, update, delete, and reconcile one type of resource. They have zero knowledge of other drivers, dependency graphs, or deployment workflows. All coordination happens in [Core's orchestrator](ORCHESTRATOR.md).

---

## Driver Model

Every cloud resource instance is modeled as a **Restate Virtual Object** keyed by a natural identifier:

- S3 Bucket: `my-bucket` (bucket names are globally unique)
- SecurityGroup: `vpc-123~web-sg` (VPC-scoped, using `~` as separator)

Each Virtual Object holds:

| Field | Description |
|-------|-------------|
| **Desired State** | The user's declared configuration (from template evaluation) |
| **Observed State** | What actually exists in the cloud provider |
| **Outputs** | Values produced after provisioning (ARNs, endpoints, IDs) |
| **Status** | `Pending`, `Provisioning`, `Ready`, `Error`, `Deleting`, `Deleted` |
| **Mode** | `Managed` (full lifecycle) or `Observed` (read-only tracking) |
| **Generation** | Counter incremented on each spec change |
| **Last Reconcile** | Timestamp of the last drift detection run |

---

## Handler Contract

Every driver MUST implement these six handlers:

### Exclusive Handlers (Single-Writer)

These run one-at-a-time per object key. While a `Provision` is running for `my-bucket`, no other exclusive handler can execute on `my-bucket`.

| Handler | Signature | Purpose |
|---------|-----------|---------|
| `Provision` | `(ObjectContext, Spec) → (Outputs, error)` | Idempotent create-or-converge. If the resource exists and matches the spec, succeed. If it differs, converge. |
| `Import` | `(ObjectContext, ImportRef) → (Outputs, error)` | Adopt an existing cloud resource. Captures observed state as desired baseline. |
| `Delete` | `(ObjectContext) → error` | Remove the resource. Fail terminally if unsafe (e.g., non-empty bucket). |
| `Reconcile` | `(ObjectContext) → (ReconcileResult, error)` | Periodic drift detection. Managed mode: correct drift. Observed mode: report only. |

### Shared Handlers (Concurrent Reads)

These can run concurrently and never block exclusive handlers.

| Handler | Signature | Purpose |
|---------|-----------|---------|
| `GetStatus` | `(ObjectSharedContext) → (StatusResponse, error)` | Return lifecycle status, mode, generation. |
| `GetOutputs` | `(ObjectSharedContext) → (Outputs, error)` | Return resource outputs (ARN, endpoint, etc.). |

The Restate SDK discovers handlers automatically via reflection (`restate.Reflect`) — there is no Go interface to implement.

---

## State Model

All driver state is stored as a **single atomic K/V entry** under the key `"state"`. A single `restate.Set()` call writes the entire state struct, ensuring no torn state if the handler crashes between operations.

```go
type S3BucketState struct {
    Desired            S3BucketSpec           `json:"desired"`
    Observed           ObservedState          `json:"observed"`
    Outputs            S3BucketOutputs        `json:"outputs"`
    Status             types.ResourceStatus   `json:"status"`
    Mode               types.Mode             `json:"mode"`
    Error              string                 `json:"error,omitempty"`
    Generation         int64                  `json:"generation"`
    LastReconcile      string                 `json:"lastReconcile,omitempty"`
    ReconcileScheduled bool                   `json:"reconcileScheduled"`
}
```

Every driver follows this exact pattern. The concrete types (`S3BucketSpec`, `S3BucketOutputs`, `ObservedState`) vary per driver, but the structure is always the same.

---

## Provision Flow

Provision follows a check-then-converge pattern:

```
1. Load existing state from Restate K/V (if any)
2. Resolve account → AWS credentials via auth.Registry
3. Check if resource exists in AWS (restate.Run → AWS Describe)
4. If absent: create it (restate.Run → AWS Create)
5. Apply full configuration (restate.Run → AWS Configure calls)
6. Describe actual state (restate.Run → AWS Describe)
7. Build outputs from observed state
8. Write state (desired + observed + outputs + status=Ready)
9. Schedule reconciliation timer
10. Return outputs
```

Key properties:
- **Idempotent** — calling Provision twice with the same spec produces the same result
- **Convergent** — calling Provision with an updated spec adjusts the resource to match
- **Crash-safe** — every AWS call is wrapped in `restate.Run()`, journaled by Restate. On replay, completed calls return their journaled result without re-executing.

---

## Reconciliation

Reconciliation is Praxis's drift detection and correction mechanism. It replaces the polling-watch model used by Kubernetes controllers with Restate's durable timers.

### How It Works

1. After each Provision or Reconcile, the driver schedules a delayed self-invocation:

   ```go
   restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").
       Send(restate.Void{}, restate.WithDelay(5 * time.Minute))
   ```

2. When the timer fires, the `Reconcile` handler runs:
   - Describes the actual cloud resource state
   - Compares it against the desired spec

3. Based on mode:
   - **Managed**: corrects drift by re-applying configuration, updates observed state
   - **Observed**: reports drift (sets `ReconcileResult.Drift = true`) but does not modify the resource

4. Schedules the next timer.

### Timer Deduplication

Drivers use a `ReconcileScheduled` boolean guard in state to prevent timer fan-out. Without this, each Provision call would stack additional timers:

```go
func (d *Driver) scheduleReconcile(ctx restate.ObjectContext, state *State) {
    if state.ReconcileScheduled {
        return
    }
    state.ReconcileScheduled = true
    restate.Set(ctx, drivers.StateKey, *state)
    restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").
        Send(restate.Void{}, restate.WithDelay(drivers.ReconcileInterval))
}
```

### Properties

- **Crash-proof** — the timer is a durable Restate invocation. It fires even if the driver restarts.
- **Deduplicated** — no duplicate AWS calls on replay (all wrapped in `restate.Run()`).
- **Self-healing** — managed resources converge back to desired state automatically.

---

## Import

Import adopts an existing cloud resource into Praxis without modifying it:

1. Resolve account credentials
2. Describe the resource in AWS → observed state
3. Set desired = observed (so the first reconcile sees no drift)
4. Build outputs from observed state
5. Apply the requested mode (`Managed` or `Observed`)
6. Schedule reconciliation

Users can later call Provision with a new spec to update the desired state, and reconciliation will converge the resource.

---

## Delete

1. Load current state
2. Check safety constraints (e.g., S3 bucket must be empty)
3. Delete the resource in AWS (`restate.Run()`)
4. Write tombstone state (`Status: Deleted`)
5. Do NOT schedule reconciliation

Properties:
- **Safe** — non-empty resources fail with a terminal error
- **Idempotent** — deleting an already-deleted resource succeeds
- **Tombstone** — `GetStatus` still returns meaningful data after deletion

---

## Resource Keys

Each driver owns its key format, producing the shortest natural key for its resource type:

| Resource Type | Scope | Format | Example |
|---------------|-------|--------|---------|
| S3Bucket | Global | `<name>` | `my-bucket` |
| SecurityGroup | Custom (VPC-scoped) | `<vpcId>~<groupName>` | `vpc-123~web-sg` |

The `~` separator is URL-safe and does not collide with characters valid in AWS resource names.

### Key Scopes

| Scope | Format | Description |
|-------|--------|-------------|
| **Global** | `<name>` | Resource name is globally unique (S3) |
| **Region** | `<region>~<name>` | Resource name is unique within a region |
| **Custom** | adapter-defined | Resource has domain-specific scoping (SecurityGroup = VPC) |

The CLI uses key scope metadata to assemble keys from user input and ambient context (e.g., `PRAXIS_REGION`).

---

## Error Classification

Drivers must classify errors into two categories:

### Terminal Errors (No Retry)

```go
return restate.TerminalError(fmt.Errorf("bucket is not empty"), 409)
```

Use for permanent failures: validation errors, conflicts, not-found during import, non-empty on delete. Restate stops retrying immediately.

### Retryable Errors (Automatic Retry)

```go
return fmt.Errorf("AWS API timeout: %w", err)
```

Use for transient failures: throttling, timeouts, 5xx responses. Restate retries automatically with backoff.

**Critical rule:** error classification MUST happen inside `restate.Run()` callbacks. If a `restate.Run()` callback returns a non-terminal error, Restate retries the entire callback. Terminal errors must be returned from inside the callback to signal that the failure is permanent.

---

## AWS Wrapper Pattern

Each driver wraps the AWS SDK behind a testable interface:

```go
type S3API interface {
    HeadBucket(ctx context.Context, name string) error
    CreateBucket(ctx context.Context, name, region string) error
    ConfigureBucket(ctx context.Context, spec S3BucketSpec) error
    DescribeBucket(ctx context.Context, name string) (ObservedState, error)
    DeleteBucket(ctx context.Context, name string) error
}
```

The concrete implementation translates to AWS SDK calls and provides error classification helpers (`IsNotFound`, `IsBucketNotEmpty`, `IsConflict`).

### Rate Limiting

All AWS API wrappers include a per-service token bucket rate limiter (from `internal/infra/ratelimit/`), wired transparently at construction time. Drivers never interact with the rate limiter directly — it sits inside the AWS wrapper layer.

| Service | Default Rate | Burst |
|---------|-------------|-------|
| S3 (control plane) | 100 rps | 20 |
| EC2 (describe/create) | 50 rps | 10 |

---

## Driver File Layout

Each driver follows the same directory structure:

```
internal/drivers/<kind>/
├── types.go       # Spec, Outputs, ObservedState, State structs
├── aws.go         # AWS SDK wrapper behind a testable interface
├── drift.go       # Pure-function drift detection
├── driver.go      # Restate Virtual Object with lifecycle handlers
└── drift_test.go  # Drift detection tests

cmd/praxis-<kind>/
├── main.go        # Binary entrypoint
└── Dockerfile     # Multi-stage distroless build

schemas/aws/<service>/<kind>.cue  # CUE schema for user-facing spec
```

---

## Provider Adapter Registry

Core doesn't call drivers directly. It uses a provider adapter registry (`internal/core/provider/`) that maps resource kinds to typed dispatch logic:

```go
type Adapter interface {
    Kind() string                    // "S3Bucket"
    ServiceName() string             // Restate service name
    Scope() KeyScope                 // Global, Region, Custom
    BuildKey(doc json.RawMessage) (string, error)
    DecodeSpec(doc json.RawMessage) (any, error)
    Provision(ctx, key, account, spec) (ProvisionInvocation, error)
    Delete(ctx, key) (DeleteInvocation, error)
    Plan(ctx, key, account, spec) (DiffOperation, []FieldDiff, error)
    Import(ctx, key, account, ref) (ResourceStatus, map[string]any, error)
    NormalizeOutputs(raw any) (map[string]any, error)
}
```

Each adapter converts between the generic JSON resource documents that templates produce and the typed Go structs that drivers expect. The orchestrator calls `adapter.Provision()` which returns a Restate future; it then waits on that future alongside others for maximum parallelism.

---

## Current Drivers

### S3Bucket

Manages AWS S3 buckets. Spec fields: `bucketName`, `region`, `versioning`, `encryption` (enabled + algorithm), `acl`, `tags`.

Outputs: `arn`, `bucketName`, `region`, `domainName`.

Key: bucket name (globally unique). Scope: Global.

### SecurityGroup

Manages AWS EC2 Security Groups. Spec fields: `groupName`, `description`, `vpcId`, `ingressRules`, `egressRules`, `tags`.

Outputs: `groupId`, `groupArn`, `groupName`, `vpcId`.

Key: `<vpcId>~<groupName>`. Scope: Custom.
