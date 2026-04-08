# Orchestrator

---

## Overview

The orchestrator is the deployment execution engine inside Praxis Core. It takes a compiled template — validated resources with a dependency graph — and drives them to completion by dispatching to [drivers](DRIVERS.md) in topological order with maximum parallelism.

The orchestrator never sees [data sources](TEMPLATES.md#data-sources). Data source lookups are resolved during template compilation in the Command Service — before the DAG is built — so by the time the orchestrator receives a plan, all `${data.*}` expressions have been replaced with literal values. The orchestrator only handles `${resources.*}` expressions for dispatch-time hydration.

The orchestrator is built on three Restate primitives:

- **Workflows** — run-once-per-key execution for apply and delete flows
- **Virtual Objects** — durable state for deployments, listing indexes, and event feeds
- **Durable RPC** — exactly-once inter-service calls from workflows to drivers

---

## Restate Services

The orchestrator consists of five Restate services, all hosted in the `praxis-core` container:

| Service | Type | Key | Purpose |
|---------|------|-----|---------|
| `DeploymentWorkflow` | Workflow | `<deploymentKey>-gen-<N>` | Apply/re-apply execution |
| `DeploymentDeleteWorkflow` | Workflow | `<deploymentKey>-delete-<N>` | Delete execution |
| `DeploymentStateObj` | Virtual Object | `<deploymentKey>` | Durable lifecycle record |
| `DeploymentIndex` | Virtual Object | `"global"` (fixed) | Listing index for all deployments |
| `ResourceIndex` | Virtual Object | `"global"` (fixed) | Listing index for resources by Kind |
| `DeploymentEvents` | Virtual Object | `<deploymentKey>` | Append-only event stream |

```mermaid
graph LR
    CS["Command Service"] -->|"Submit"| DW["DeploymentWorkflow<br/>(per generation)"]
    CS -->|"Submit"| DDW["DeploymentDeleteWorkflow<br/>(per generation)"]
    DW -->|"Read/Write"| DS["DeploymentStateObj<br/>(per deployment)"]
    DDW -->|"Read/Write"| DS
    DW -->|"Append"| DE["DeploymentEvents<br/>(per deployment)"]
    DDW -->|"Append"| DE
    DW -->|"Upsert"| DI["DeploymentIndex<br/>(global singleton)"]
    DDW -->|"Upsert"| DI
    DW -->|"Upsert"| RI["ResourceIndex<br/>(global singleton)"]
    DDW -->|"Remove"| RI
    CLI -->|"Read"| DS
    CLI -->|"Poll"| DE
    CLI -->|"List"| DI
    CLI -->|"Query"| RI
```

### Why Separate Workflows and State

Restate Workflows are run-once-per-key — a workflow key can only execute one `Run` once. Deployments need re-apply semantics (apply the same stack again with updated specs). The solution:

- **DeploymentState** is keyed by deployment ID and persists across all runs
- **DeploymentWorkflow** is keyed by `<deploymentID>-gen-<N>`, where N is the generation counter
- Each apply increments the generation and spawns a new workflow
- Both the current and historical workflows share access to the same DeploymentState

This separation also means:

- Direct reads (CLI `get`, `list`) query DeploymentState without coupling to workflow internals
- Apply and delete workflows both read/write the same state object
- State survives workflow completion

---

## Apply Flow

### Input: DeploymentPlan

The [Command Service](TEMPLATES.md#evaluation-pipeline) builds a `DeploymentPlan` after template evaluation:

```go
type DeploymentPlan struct {
    Key          string            // stable deployment identifier
    Account      string            // resolved AWS account name
    Resources    []PlanResource    // rendered resources with dependency metadata
    Variables    map[string]any    // template variables
    CreatedAt    time.Time
    TemplatePath string            // "inline://template.cue" or "registry://<name>"
}

type PlanResource struct {
    Name           string              // template-local name (e.g., "bucket", "sg")
    Kind           string              // resource type (e.g., "S3Bucket")
    Key            string              // canonical Restate object key
    Spec           json.RawMessage     // rendered JSON, may have unresolved expressions
    Dependencies   []string            // template-local names this depends on
    Expressions    map[string]string   // JSON path → expression for dispatch-time hydration
}
```

### Execution: Eager Scheduler

The workflow uses an **eager dispatch** strategy — resources start as soon as their specific dependencies are met, not when an entire tier completes:

```mermaid
flowchart TD
    A["Build dependency graph"] --> B["Initialize: all resources pending"]
    B --> C{"Cancelled?"}
    C -->|Yes| Final
    C -->|No| D{"Pending resources<br/>with all deps met?"}
    D -->|Yes| E["Hydrate spec via expressions<br/>Decode via adapter"]
    E --> F["Dispatch Provision<br/>(returns Restate future)"]
    F --> G["WaitFirst on<br/>in-flight resources"]
    D -->|No| H{"Any in-flight?"}
    H -->|Yes| G
    H -->|No| Final["Finalize: Complete,<br/>Failed, or Cancelled"]
    G --> I{"Result?"}
    I -->|Success| J["Collect outputs<br/>Store in DeploymentState<br/>Record event"]
    I -->|Failure| K["Mark resource as Error<br/>Skip transitive dependents"]
    J --> C
    K --> C
```

### Dispatch-Time Expression Hydration

Templates can express cross-resource dependencies with output expressions:

```cue
logBucket: s3.#S3Bucket & {
    spec: {
        tags: {
            securityGroup: "${resources.sg.outputs.groupId}"
        }
    }
}
```

At template evaluation time, the DAG parser extracts these expressions and records them as dependency edges. The expressions themselves are left as strings in the resource spec.

When the dependency completes and its outputs are available, `HydrateExprs` resolves each expression by walking the dot path (`resources.<name>.outputs.<field>`) through the output map and writes the **typed** result back into the JSON document:

- Strings stay strings
- Integers stay integers
- Booleans stay booleans
- Arrays stay arrays

This is different from template-time variable injection (CUE interpolation), which stringifies results. The hydrator preserves types so drivers receive specs with correct JSON types.

### Parallel Dispatch

The eager scheduler uses Restate's `WaitFirst` to wait on multiple in-flight Provision calls simultaneously. Consider a template with three resources:

```mermaid
graph TD
    bucket["bucket<br/>(no deps)"] ~~~ sg["sg<br/>(no deps)"]
    sg --> logBucket["logBucket<br/>(depends on sg)"]

    style bucket fill:#4CAF50,color:#fff
    style sg fill:#4CAF50,color:#fff
    style logBucket fill:#2196F3,color:#fff
```

Execution:

1. `bucket` and `sg` dispatch in parallel (both have no dependencies)
2. `WaitFirst` returns whichever completes first
3. When `sg` completes → `logBucket` becomes ready, gets hydrated and dispatched
4. `bucket` and `logBucket` run in parallel

The scheduler achieves maximum parallelism limited only by the dependency graph.

---

## Delete Flow

The `DeploymentDeleteWorkflow` handles deployment teardown:

1. Read current DeploymentState
2. **Drain wait**: If an apply workflow is still running, poll every 2 seconds until it reaches a terminal state. After 60 seconds of draining, the delete workflow force-transitions the deployment to `Cancelled` and proceeds — this handles hard-killed apply workflows that left the deployment stuck at `Running` or `Pending`.
3. Reconstruct the dependency graph from stored resource metadata
4. Compute reverse topological order
5. For each resource in reverse order:
   - **Check `lifecycle.preventDestroy`** — if `true`, fail immediately with a terminal error (unless `--force` is used)
   - Skip resources that were never provisioned (`Pending`) or already deleted (`Deleted`)
   - Delete the resource via the driver
6. On failure: mark the resource's dependencies as Skipped (they may still be referenced). When `--force` is set, dependency skipping is bypassed — every resource is attempted for deletion regardless of upstream failures.
7. Independent branches continue in parallel
8. Finalize deployment as Deleted or Failed

Note: Resources in `Skipped` status (bypassed due to a prior dependency failure) are **not** skipped on subsequent delete attempts — they are retried because they were never actually deleted.

Delete is a separate workflow from apply because:

- Both are long-running operations that benefit from Restate's durable execution
- Both need their own workflow key (run-once-per-key semantics)
- They share the same DeploymentState for consistent lifecycle tracking

---

## Deployment State

### Lifecycle

```mermaid
stateDiagram-v2
    [*] --> Pending
    Pending --> Running
    Running --> Complete
    Running --> Failed
    Running --> Cancelled
    Running --> Deleting
    Deleting --> Deleted
    Deleted --> [*]
```

### Resource Lifecycle Within a Deployment

```mermaid
stateDiagram-v2
    [*] --> Pending
    Pending --> Provisioning
    Pending --> Updating
    Provisioning --> Ready
    Provisioning --> Error
    Provisioning --> Skipped
    Updating --> Ready
    Updating --> Error
    Updating --> Skipped
    Ready --> Deleting
    Error --> Deleting
    Deleting --> Deleted
    Deleted --> [*]
```

- **Pending** — queued, dependencies not yet met
- **Provisioning** — driver Provision call dispatched (new resource)
- **Updating** — driver Provision call dispatched (resource existed in prior generation)
- **Ready** — driver returned success and outputs
- **Error** — driver returned an error
- **Skipped** — a dependency failed, this resource was never dispatched
- **Deleting** — driver Delete call dispatched
- **Deleted** — driver confirmed removal

### State Structure

```go
type DeploymentState struct {
    Key          string
    Account      string
    Status       DeploymentStatus
    TemplatePath string
    Resources    map[string]*ResourceState
    Outputs      map[string]map[string]any    // resource name → normalized outputs
    Generation   int64
    Error        string
    CreatedAt    time.Time
    UpdatedAt    time.Time
    Cancelled    bool
}
```

### DeploymentState Handlers

| Handler | Context | Purpose |
|---------|---------|---------|
| `InitDeployment` | Exclusive | Create or re-initialize for a new apply generation |
| `SetStatus` | Exclusive | Update deployment-wide status |
| `UpdateResource` | Exclusive | Update one resource's status and outputs |
| `Finalize` | Exclusive | Set terminal status and error |
| `GetState` | Shared | Return full state (used by CLI `get`) |
| `GetDetail` | Shared | Return deployment detail view |
| `IsCancelled` | Shared | Check cancellation flag |

---

## Deployment Index

Restate Virtual Objects cannot be enumerated by key. To support `praxis list deployments`, a `DeploymentIndex` Virtual Object (keyed by `"global"`) maintains a map of deployment summaries.

Workflows update the index via one-way sends after status changes:

```go
type DeploymentSummary struct {
    Key       string
    Status    DeploymentStatus
    Account   string
    UpdatedAt time.Time
}
```

The `List` handler returns summaries in deterministic key order.

---

## Resource Index

Similar to DeploymentIndex, the `ResourceIndex` Virtual Object (keyed by `"global"`) stores a denormalized map of resource entries across all deployments. This enables efficient cross-deployment queries by resource Kind (e.g., `praxis list S3Bucket`) without scanning every DeploymentStateObj.

Entries are keyed by `deploymentKey~resourceName` and track the resource's Kind, canonical Key, deployment, workspace, and current status.

| Handler | Access | Description |
|---------|--------|-------------|
| `Upsert` | Exclusive | Insert or update a resource entry |
| `Remove` | Exclusive | Remove a single entry by deployment + resource name |
| `RemoveByDeployment` | Exclusive | Remove all entries for a deployment |
| `Query` | Shared | Query entries by Kind and/or Workspace |

The index is updated at these lifecycle points:
- **Deployment submission** (pipeline): all resources seeded as Pending
- **Resource Ready** (apply workflow): status updated to Ready
- **Terminal state** (apply workflow): Error/Skipped resources synced
- **Resource Deleted** (delete/rollback workflows): entry removed
- **Full deployment deletion**: batch removal via RemoveByDeployment
- **State mv** (MoveResource/RemoveResource/AddResource): entries updated

---

## Deployment Events

An append-only event stream per deployment, stored in the `DeploymentEvents` Virtual Object.

Each event carries:

```go
type DeploymentEvent struct {
    DeploymentKey string
    Sequence      int64       // monotonically increasing per deployment
    Status        DeploymentStatus
    ResourceName  string      // empty for deployment-level events
    ResourceKind  string
    Message       string
    CreatedAt     time.Time
}
```

The CLI's `observe` command polls `ListSince(lastSequence)` to stream events in real time.

### Handlers

| Handler | Context | Purpose |
|---------|---------|---------|
| `Append` | Exclusive | Add an event with auto-incremented sequence |
| `ListSince` | Shared | Return events after a given sequence cursor |

---

## DAG Engine

The dependency graph engine (`internal/core/dag/`) is a **pure Go library** with no Restate dependency. It is testable in complete isolation.

### Components

| Component | File | Purpose |
|-----------|------|---------|
| Parser | `parser.go` | Extracts `${resources.<name>.outputs.*}` patterns from JSON specs → dependency edges |
| Graph | `graph.go` | DAG construction, cycle detection (DFS), topological ordering |
| Scheduler | `scheduler.go` | Runtime dispatch queries: `Ready()` and `AffectedByFailure()` |

### Graph Operations

- **Topological sort** — deterministic resource ordering for dispatch
- **Cycle detection** — DFS-based, rejects circular dependencies at template evaluation time
- **Dependency check** — `DependenciesMet(name, completed)` for the scheduler
- **Dependents query** — `Dependents(name)` for failure propagation
- **Reverse topological** — `ReverseTopo()` for delete ordering

### Scheduler

The `Schedule` type wraps a validated graph and answers two questions:

1. **`Ready(completed, dispatched)`** — which resources can be dispatched now? Walks topological order for deterministic results.
2. **`AffectedByFailure(failed)`** — which resources transitively depend on the failed resource? Used to mark them as Skipped.

---

## Re-Apply Semantics

When a user runs `praxis deploy` against an existing deployment:

1. Command Service detects the deployment key already exists
2. Calls `DeploymentState.InitDeployment` which increments the generation counter
3. All resource statuses reset to `Pending`, previous outputs are cleared
4. A new `DeploymentWorkflow` is submitted with key `<deploymentKey>-gen-<N>`
5. The workflow runs the same eager dispatch flow against the updated specs

This means Praxis supports iterative development: update the template, re-apply, and the orchestrator converges resources to the new desired state.

---

## Cancellation

Workflows check for cancellation via `DeploymentState.IsCancelled()` at the start of each dispatch loop iteration. When cancellation is detected:

1. No new resources are dispatched
2. In-flight resources are allowed to complete (no mid-operation abort)
3. The deployment finalizes as `Cancelled`

This graceful approach avoids leaving resources in an indeterminate state.

---

## Lifecycle Rules

Lifecycle rules protect resources from accidental deletion and allow selective drift ignoring. They are declared in templates and enforced by the orchestrator and plan diff engine.

### preventDestroy

When `lifecycle.preventDestroy: true` is set on a resource:

- The **delete workflow** checks the flag before calling `adapter.Delete()`. If set, the resource is marked as failed with a terminal error and the workflow does not retry. When `--force` is used, the protection is overridden and an audit event (`dev.praxis.policy.force_delete_override`) is emitted.
- The **apply workflow** checks the flag before force-replacing a resource (`--replace` or `--allow-replace`). Protected resources cannot be recreated even with auto-replace.
- The error message is explicit:

```text
lifecycle.preventDestroy enabled; refusing to delete resource "prod-db" (RDSInstance)
```

To delete a protected resource, either update the template to set `preventDestroy: false` (or remove it), re-apply, then delete — or use `praxis delete --force` as an escape hatch.

### Auto-Replace (Immutable Field Conflicts)

When `--allow-replace` is set on a deploy command, the workflow automatically handles 409 immutable-field conflicts from drivers:

1. The driver returns a 409 error (immutable field change detected).
2. The workflow checks `lifecycle.preventDestroy` — if set, the resource fails without replacement.
3. An `auto_replace.started` event is emitted for audit visibility.
4. The existing resource is deleted via `adapter.Delete()`.
5. A new resource is provisioned with the updated spec.

This is functionally identical to `--replace <resource>` but triggered automatically by the 409 error code. It is a destructive operation — the resource is fully destroyed and recreated, which may cause downtime and data loss.

### ignoreChanges

When `lifecycle.ignoreChanges: ["path1", "path2"]` is set on a resource:

- The **plan diff engine** filters out field diffs matching the ignored paths before presenting results. If all diffs are filtered, the resource shows as `no-op` instead of `update`.
- Supports exact path matching (`"tags.env"` matches only `tags.env`) and prefix matching (`"tags"` matches `tags.env`, `tags.team`, etc.).
- Non-ignored fields are still diffed and corrected normally.

This allows external systems (billing tools, compliance scanners, other IaC) to manage specific fields without Praxis fighting for control.

---

## Plan-Time Expression Resolution

The `plan` and `deploy --dry-run` commands compute accurate diffs for all resources, including those with `${resources.X.outputs.Y}` expressions. This requires resolving expressions before calling each driver's `Plan` method — but at plan time, no deployment workflow has run, so no dispatch-time hydration occurs.

### Live Output Collection

The plan diff engine solves this by collecting **live outputs** from driver virtual objects as it walks resources in topological order:

```mermaid
flowchart TD
    A["Walk resources in topological order"] --> B{"Has expressions?"}
    B -->|No| C["Call adapter.Plan(key, spec)"]
    B -->|Yes| D["Hydrate spec with accumulated outputs"]
    C --> E{"Referenced by downstream?"}
    D --> F["Call adapter.Plan(key, hydratedSpec)"]
    F --> E
    E -->|Yes| G["Call adapter.GetOutputs(key)\nStore in liveOutputs map"]
    E -->|No| H["Record diff result"]
    G --> H
    H --> A
```

1. **Non-expression resources** are planned first (they have no unresolved dependencies). After planning, if downstream resources reference them, their outputs are read from the driver's virtual-object state via `GetOutputs`.

2. **Expression-bearing resources** are planned after their dependencies. `HydrateExprs` resolves each `${resources.<name>.outputs.<field>}` placeholder using the accumulated `liveOutputs` map, producing a fully hydrated spec for the driver's `Plan` method.

3. **Resolved keys for display.** Expression-bearing resources may have keys containing placeholders (e.g., `${resources.vpc.outputs.vpcId}~web-sg`). The plan engine calls `adapter.BuildKey(hydratedSpec)` to compute the resolved key for display (e.g., `vpc-0abc123~web-sg`), while using the original unresolved key for driver lookups.

### Why the Original Key for Driver Lookups

The deployment workflow provisions driver virtual objects at the **original unresolved key** — the key built from the raw template spec before expression hydration. This is because `buildResourceNodes` runs before any outputs are available, so expression-containing keys are stored literally (e.g., `${resources.vpc.outputs.vpcId}~web-sg`).

The plan engine must use the same key when calling `adapter.Plan` and `GetOutputs`, or the driver won't find its stored state. The resolved key from `BuildKey(hydratedSpec)` is used exclusively for human-readable display.

### Fallback to Prior Deployment Outputs

As a fallback, `fetchPriorOutputs` optionally seeds the `liveOutputs` map with outputs from a prior deployment. This covers edge cases where a driver's virtual-object state is unavailable (e.g., the driver service was restarted and state was lost). The `--key` flag allows specifying which deployment to use; when omitted, the key is auto-derived from the template.

### Field-Level Deletion Display

Within an update diff, fields whose new value is zero/empty (empty string, `0`, `false`, empty list, or empty map) are rendered as deletions with a `-` prefix showing only the old value:

```text
  # Listener "us-east-1~my-listener" will be updated in-place
  ~ resource "Listener" "us-east-1~my-listener" {
      ~ port      = 443 => 8443
      - sslPolicy = "ELBSecurityPolicy-2016-08"
    }
```

This makes it clear that `sslPolicy` is being removed rather than changed to an empty string.
