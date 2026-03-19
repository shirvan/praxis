# Security Group Driver — Implementation Plan

> Target: A Restate Virtual Object driver that manages EC2 Security Groups, providing
> full lifecycle management including creation, import, deletion, drift detection, and
> drift correction with add-before-remove rule application.
>
> Key scope: `KeyScopeCustom` — key format is `vpcId~groupName`, permanent and
> immutable for the lifetime of the Virtual Object. The AWS-assigned group ID lives
> only in state/outputs.

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
11. [Step 8 — Binary Entry Point & Dockerfile](#step-8--binary-entry-point--dockerfile)
12. [Step 9 — Docker Compose & Justfile](#step-9--docker-compose--justfile)
13. [Step 10 — Unit Tests](#step-10--unit-tests)
14. [Step 11 — Integration Tests](#step-11--integration-tests)
15. [SG-Specific Design Decisions](#sg-specific-design-decisions)
16. [Checklist](#checklist)

---

## 1. Overview & Scope

The SG driver manages the lifecycle of EC2 **Security Groups** only. It creates,
imports, updates, and deletes security groups with their ingress and egress rules.
Network ACLs, VPCs, subnets, and other networking primitives are handled by separate
drivers.

The driver follows the established Virtual Object contract:

| Handler | Context | Purpose |
|---|---|---|
| `Provision` | `ObjectContext` (exclusive) | Create or converge a security group |
| `Import` | `ObjectContext` (exclusive) | Adopt an existing security group |
| `Delete` | `ObjectContext` (exclusive) | Remove a security group |
| `Reconcile` | `ObjectContext` (exclusive) | Detect/correct drift (report-only for Observed mode) |
| `GetStatus` | `ObjectSharedContext` (shared) | Return current status |
| `GetOutputs` | `ObjectSharedContext` (shared) | Return security group outputs |

### Mutable vs Immutable Attributes

| Attribute | Mutability | Notes |
|---|---|---|
| `groupName` | Immutable | Part of the Virtual Object key; cannot change after creation |
| `description` | Immutable | AWS does not allow updating SG description after creation |
| `vpcId` | Immutable | Part of the Virtual Object key; changing VPC requires delete + recreate |
| `ingressRules` | Mutable | Updated via AuthorizeIngress/RevokeIngress with add-before-remove |
| `egressRules` | Mutable | Updated via AuthorizeEgress/RevokeEgress with add-before-remove |
| `tags` | Mutable | Full replace via DeleteTags + CreateTags |

---

## 2. Key Strategy

### Key Scope: `KeyScopeCustom`

Unlike S3 (`KeyScopeGlobal` — bucket names are globally unique) and EC2 instances
(`KeyScopeRegion` — `region~metadata.name`), security groups use `KeyScopeCustom`
with the format:

```
vpcId~groupName
```

**Rationale**: Security group names are unique only within a VPC. Two different VPCs
can have security groups with the same name. The key must capture both dimensions
to avoid conflicts.

### BuildKey vs BuildImportKey

- **`BuildKey(resourceDoc)`**: Extracts `spec.vpcId` and `spec.groupName` from the
  resource document. Returns `vpcId~groupName`. This is the key used for all
  template-driven operations (Provision, Delete, Reconcile, Plan).

- **`BuildImportKey(region, resourceID)`**: Returns `resourceID` as-is. The
  `resourceID` is the AWS security group ID (e.g., `sg-0abc123`). This means an
  imported security group targets a **different Virtual Object** than a
  template-provisioned one for the same underlying AWS resource. This is intentional
  and consistent with how all drivers handle imports.

### Key Validation

Both `vpcId` and `groupName` are validated via `ValidateKeyPart()` at BuildKey time
to ensure they contain no delimiters (`~`) and are non-empty. This prevents
malformed keys from entering the system.

---

## 3. File Inventory

All files below exist in the repository (✓ = implemented):

```
✓ schemas/aws/ec2/sg.cue                   — CUE schema for SecurityGroup resource
✓ internal/drivers/sg/types.go              — Spec, Outputs, ObservedState, State structs
✓ internal/drivers/sg/aws.go               — SGAPI interface + realSGAPI implementation
✓ internal/drivers/sg/drift.go             — NormalizedRule, HasDrift, ComputeDiff, ComputeFieldDiffs
✓ internal/drivers/sg/driver.go            — SecurityGroupDriver Virtual Object
✓ internal/drivers/sg/driver_test.go       — Unit tests: specFromObserved, extractCidr, ServiceName
✓ internal/drivers/sg/aws_test.go          — Unit tests: error classification (IsDependencyViolation)
✓ internal/drivers/sg/drift_test.go        — Unit tests: normalization, drift, diff, split
✓ internal/core/provider/sg_adapter.go     — SecurityGroupAdapter implementing provider.Adapter
✓ internal/core/provider/registry.go       — (modified) registers SecurityGroupAdapter
✓ cmd/praxis-network/main.go               — Network driver pack entry point (SG bound here)
✓ cmd/praxis-network/Dockerfile            — Multi-stage Docker build
✓ docker-compose.yaml                      — (modified) praxis-network service on port 9082
✓ justfile                                 — (modified) network build/test/register targets
✓ tests/integration/sg_driver_test.go      — Integration tests (Testcontainers + LocalStack)
```

> **Note**: The CUE schema lives at `schemas/aws/ec2/sg.cue` (under the `ec2`
> package directory), not a separate `schemas/aws/sg/` directory. Security groups are
> an EC2 resource type, so they share the `ec2` CUE package with the EC2 instance
> schema and can cross-reference each other.

---

## Step 1 — CUE Schema

**File**: `schemas/aws/ec2/sg.cue`

Defines the shape of a `SecurityGroup` resource document. The template engine
validates user templates against this schema before dispatch.

```cue
package ec2

#SecurityGroup: {
    apiVersion: "praxis.io/v1"
    kind:       "SecurityGroup"

    metadata: {
        name: string & =~"^[a-zA-Z0-9 _\\-]{1,255}$"
        labels: [string]: string
    }

    spec: {
        groupName:   string
        description: string
        vpcId:       string
        ingressRules: [...#Rule] | *[]
        egressRules:  [...#Rule] | *[{protocol: "-1", fromPort: 0, toPort: 0, cidrBlock: "0.0.0.0/0"}]
        tags: [string]: string
    }

    outputs?: {
        groupId:  string
        groupArn: string
        vpcId:    string
    }
}

#Rule: {
    protocol:  "tcp" | "udp" | "icmp" | "-1"
    fromPort:  int & >=0 & <=65535
    toPort:    int & >=fromPort & <=65535
    cidrBlock: string & =~"^([0-9]{1,3}\\.){3}[0-9]{1,3}/[0-9]{1,2}$"
}
```

### Key Design Decisions

- **Default egress rule**: `egressRules` defaults to allow-all egress
  (`protocol: "-1", cidrBlock: "0.0.0.0/0"`), matching AWS default behavior when
  creating a security group. Users can override this with explicit egress rules.

- **Default ingress**: `ingressRules` defaults to an empty list (no ingress), which
  matches the AWS default of denying all inbound traffic.

- **Protocol values**: `-1` means "all protocols" in the AWS API. The CUE schema
  accepts the raw AWS value; the driver normalizes to `"all"` internally for
  canonical comparison.

- **CIDR validation**: The regex `^([0-9]{1,3}\.){3}[0-9]{1,3}/[0-9]{1,2}$`
  validates CIDR block format at schema level. Semantic validation (e.g., valid
  subnet mask) is left to the AWS API.

- **Shared CUE package**: The `#Rule` type is defined in `sg.cue` alongside
  `#SecurityGroup`. If a future VPC or NACL driver needs rule types, they would be
  defined separately or extracted to a shared types file within the `ec2` package.

---

## Step 2 — Driver Types

**File**: `internal/drivers/sg/types.go`

```go
type SecurityGroupSpec struct {
    Account      string            `json:"account,omitempty"`
    GroupName    string            `json:"groupName"`
    Description  string            `json:"description"`
    VpcId        string            `json:"vpcId"`
    IngressRules []IngressRule     `json:"ingressRules,omitempty"`
    EgressRules  []EgressRule      `json:"egressRules,omitempty"`
    Tags         map[string]string `json:"tags,omitempty"`
}

type IngressRule struct {
    Protocol  string `json:"protocol"`
    FromPort  int32  `json:"fromPort"`
    ToPort    int32  `json:"toPort"`
    CidrBlock string `json:"cidrBlock,omitempty"`
}

type EgressRule struct {
    Protocol  string `json:"protocol"`
    FromPort  int32  `json:"fromPort"`
    ToPort    int32  `json:"toPort"`
    CidrBlock string `json:"cidrBlock,omitempty"`
}
```

### Separate IngressRule and EgressRule types

Ingress and egress rules have the same shape today, but are kept as separate Go
types for two reasons:

1. **Future extensibility**: AWS egress rules may gain fields that ingress rules
   don't have (or vice versa) if security group references or prefix list targets
   are added later.

2. **Type safety**: The compiler prevents accidentally passing an ingress rule slice
   where an egress slice is expected, catching direction-swap bugs at compile time.

### Outputs and State

```go
type SecurityGroupOutputs struct {
    GroupId  string `json:"groupId"`
    GroupArn string `json:"groupArn"`
    VpcId    string `json:"vpcId"`
}

type ObservedState struct {
    GroupId      string            `json:"groupId"`
    GroupName    string            `json:"groupName"`
    Description  string            `json:"description"`
    VpcId        string            `json:"vpcId"`
    IngressRules []NormalizedRule  `json:"ingressRules"`
    EgressRules  []NormalizedRule  `json:"egressRules"`
    Tags         map[string]string `json:"tags"`
}

type SecurityGroupState struct {
    Desired            SecurityGroupSpec    `json:"desired"`
    Observed           ObservedState        `json:"observed"`
    Outputs            SecurityGroupOutputs `json:"outputs"`
    Status             types.ResourceStatus `json:"status"`
    Mode               types.Mode           `json:"mode"`
    Error              string               `json:"error,omitempty"`
    Generation         int64                `json:"generation"`
    LastReconcile      string               `json:"lastReconcile,omitempty"`
    ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
```

### ObservedState uses NormalizedRule, not spec types

The `ObservedState` stores rules as `NormalizedRule` (from the drift package) rather
than `IngressRule`/`EgressRule`. This is intentional: observed state is always in
canonical form (lowercased protocols, `-1` normalized to `"all"`, `cidr:` prefix on
targets). This eliminates normalization during every drift check — the observed side
is always ready for set comparison.

### Single atomic state key

All state is stored in one Restate K/V entry (`drivers.StateKey = "state"`). This
prevents torn state: if a handler crashes between two separate `Set` calls, replay
would see an inconsistent snapshot. A single key guarantees all-or-nothing state
transitions via one `restate.Set()` call.

---

## Step 3 — AWS API Abstraction Layer

**File**: `internal/drivers/sg/aws.go`

### SGAPI Interface

```go
type SGAPI interface {
    DescribeSecurityGroup(ctx context.Context, groupId string) (ObservedState, error)
    FindSecurityGroup(ctx context.Context, groupName, vpcId string) (ObservedState, error)
    CreateSecurityGroup(ctx context.Context, spec SecurityGroupSpec) (string, error)
    DeleteSecurityGroup(ctx context.Context, groupId string) error
    AuthorizeIngress(ctx context.Context, groupId string, rules []NormalizedRule) error
    AuthorizeEgress(ctx context.Context, groupId string, rules []NormalizedRule) error
    RevokeIngress(ctx context.Context, groupId string, rules []NormalizedRule) error
    RevokeEgress(ctx context.Context, groupId string, rules []NormalizedRule) error
    UpdateTags(ctx context.Context, groupId string, tags map[string]string) error
}
```

### Key Architectural Decisions

**`context.Context`, not `restate.RunContext`**: All SGAPI methods accept a plain
`context.Context`. The driver wraps every SGAPI call inside `restate.Run()`, which
provides the `RunContext`. This separation keeps the AWS layer independent of the
Restate SDK, enabling direct testing without a Restate test environment.

**Two describe paths**: `DescribeSecurityGroup` looks up by group ID (for the
driver, which stores the ID after creation). `FindSecurityGroup` looks up by
group name + VPC ID (for the adapter's `Plan()` method, which needs to find an
existing group from the declarative spec).

**Rate limiting via `ratelimit.Limiter`**: All AWS calls go through a rate limiter
(`ec2` service, 20 RPS, burst of 10) to prevent API throttling. The limiter is
shared across all calls from one driver instance.

### Rule Encoding

Rules are passed to the AWS SDK as `IpPermission` structs. The `rulesToIpPermissions`
helper groups rules by `(protocol, fromPort, toPort)` to batch CIDRs under a single
permission entry, matching the AWS SDK's expected structure.

### Protocol Normalization

AWS returns `-1` for "all protocols" but the driver normalizes to `"all"` for
human readability and canonical comparison:

- `normalizeProtocol("-1")` → `"all"` (AWS → internal)
- `denormalizeProtocol("all")` → `"-1"` (internal → AWS)

All normalization happens at the boundary: inbound in `DescribeSecurityGroup` and
outbound in `rulesToIpPermissions`.

### Idempotent Rule Authorization

`AuthorizeIngress` and `AuthorizeEgress` silently absorb `InvalidPermission.Duplicate`
errors. This handles the case where AWS auto-creates a default egress rule during
`CreateSecurityGroup` and we subsequently try to add the same rule. Without this,
the first provision would fail with a spurious duplicate error.

### Tag Update Strategy: Delete-Then-Create

`UpdateTags` follows a delete-then-create strategy:

1. Describe current tags on the security group.
2. Delete all existing tags (full `DeleteTags` call).
3. Create new tags from the desired spec.

This ensures removed tags are cleaned up. It's not a no-op when tags haven't changed
(the driver checks `tagsMatch()` before calling `UpdateTags` during reconciliation).

### Error Classification

Five error classifiers are provided, all following the same pattern: check
`smithy.APIError.ErrorCode()` first, then fall back to string matching for errors
that have been wrapped by Restate's panic/recovery mechanism.

| Function | AWS Error Code(s) | Semantics |
|---|---|---|
| `IsNotFound` | `InvalidGroup.NotFound`, `InvalidGroupId.Malformed` | Group doesn't exist |
| `IsDuplicate` | `InvalidGroup.Duplicate` | Group name already taken in this VPC |
| `isDuplicatePermission` | `InvalidPermission.Duplicate` | Rule already exists (idempotent absorb) |
| `IsInvalidParam` | `InvalidParameterValue`, `InvalidPermission.Malformed` | Bad input (terminal) |
| `IsDependencyViolation` | `DependencyViolation` | Group is referenced by other resources |

**Critical: `IsDependencyViolation` has string fallback matching**. Restate's
`restate.Run()` panics on non-terminal errors and wraps the error text in a recovery
message. The structured `smithy.APIError` type is lost after the panic boundary. To
handle this, `IsDependencyViolation` falls back to substring matching on
`"DependencyViolation"`, `"resource is still in use"`, and
`"still referenced by other resources"`.

> **Restate footgun**: `restate.Run()` panics on non-terminal errors, which means
> error classification **must** happen inside the `restate.Run()` callback, not
> after it returns. If you classify after the callback, the original error type
> has been destroyed by the panic/recovery. The SG driver classifies inside the
> callback for `Delete` (converting `DependencyViolation` to `TerminalError`), and
> `IsDependencyViolation` includes string fallback as a defense-in-depth measure
> for any case where classification leaks outside the callback.

---

## Step 4 — Drift Detection

**File**: `internal/drivers/sg/drift.go`

### NormalizedRule — The Canonical Rule Representation

```go
type NormalizedRule struct {
    Direction string `json:"direction"` // "ingress" or "egress"
    Protocol  string `json:"protocol"`  // "tcp", "udp", "icmp", "all"
    FromPort  int32  `json:"fromPort"`
    ToPort    int32  `json:"toPort"`
    Target    string `json:"target"`    // "cidr:10.0.0.0/8"
}
```

AWS returns rules in arbitrary order. Direct slice comparison would produce false
drift on every reconciliation. `NormalizedRule` solves this by:

1. Collapsing all rule representations into a single canonical struct.
2. Generating deterministic string keys via `ruleKey()`:
   `direction|protocol|fromPort|toPort|target`
3. Sorting by key before comparison.

**Target prefix `cidr:`**: The `Target` field uses a `cidr:` prefix to distinguish
CIDR-based rules from future target types (security group references, prefix lists).
This extensibility point is baked in from day one without adding complexity.

### Core Functions

**`Normalize(spec SecurityGroupSpec) []NormalizedRule`**
Converts a spec into a sorted `NormalizedRule` slice. Expands each spec rule into
one `NormalizedRule` per CIDR, lowercases protocols, and normalizes `-1` → `"all"`.

**`HasDrift(desired SecurityGroupSpec, observed ObservedState) bool`**
Returns true if rules or tags differ between desired and observed. Normalizes both
sides and compares as sorted sets. Compares tags with `tagsMatch()`.

**`ComputeDiff(desired, observed []NormalizedRule) (toAdd, toRemove []NormalizedRule)`**
Pure set difference on normalized rule tuples:
- `toAdd` = desired − observed (rules to authorize)
- `toRemove` = observed − desired (rules to revoke)

Both outputs are sorted for deterministic application order.

**`ComputeFieldDiffs(desired SecurityGroupSpec, observed ObservedState) []FieldDiffEntry`**
Produces human-readable diffs for the plan renderer. Checks immutable fields
(groupName, description, vpcId), tags (added/changed/removed), and rules (using
`ComputeDiff`). Each diff entry has a `Path` (e.g., `spec.ingressRules[ingress|tcp|80|80|cidr:0.0.0.0/0]`)
for precise identification.

### Helper Functions

- `mergeObservedRules(obs)`: Combines ingress + egress from `ObservedState` into one
  sorted slice for diffing.
- `rulesEqual(a, b)`: Compares two sorted `NormalizedRule` slices by key.
- `sortRules(rules)`: In-place sort by `ruleKey()`.
- `tagsMatch(a, b)`: Semantic equality for tag maps (handles nil vs empty).
- `SplitByDirection(rules)`: Splits a mixed rule slice into ingress and egress
  slices for applying to the correct AWS API call.
- `ruleDiffPath(rule)`: Generates the path string for `FieldDiffEntry`.

---

## Step 5 — Driver Implementation

**File**: `internal/drivers/sg/driver.go`

### Service Registration

```go
const ServiceName = "SecurityGroup"
```

The driver is registered as a Restate Virtual Object named `"SecurityGroup"`. Each
instance is keyed by `vpcId~groupName`.

### Constructor Pattern

```go
func NewSecurityGroupDriver(accounts *auth.Registry) *SecurityGroupDriver
func NewSecurityGroupDriverWithFactory(accounts *auth.Registry, factory func(aws.Config) SGAPI) *SecurityGroupDriver
```

- `NewSecurityGroupDriver`: Production constructor. Creates an `SGAPI` from
  `awsclient.NewEC2Client()` for each resolved AWS config.
- `NewSecurityGroupDriverWithFactory`: Test constructor. Accepts a custom factory
  for injecting mock SGAPI implementations.

Both constructors fall back to `auth.LoadFromEnv()` if the provided `auth.Registry`
is nil, and to the default factory if factory is nil.

### Provision Handler

The Provision handler implements idempotent "ensure desired state" semantics:

1. **Input validation**: `groupName`, `description`, and `vpcId` must be non-empty.
   Returns `TerminalError(400)` on failure.

2. **Load current state**: Reads `SecurityGroupState` from Restate K/V. Sets status
   to `Provisioning`, increments generation.

3. **Re-provision check**: If `state.Outputs.GroupId` is non-empty, describes the
   group. If it's been deleted externally (404), clears `groupId` and falls through
   to creation.

4. **Create security group**: Calls `api.CreateSecurityGroup`. Classifies errors
   inside `restate.Run()`:
   - `IsDuplicate` → `TerminalError(409)` — group name already taken in this VPC
   - `IsInvalidParam` → `TerminalError(400)` — bad input

5. **Apply tags**: Calls `api.UpdateTags` if tags are specified.

6. **Apply rules with add-before-remove**: Calls `applyRuleDiff()` which computes
   the diff between desired and observed (empty for fresh creation) and applies
   additions before removals.

7. **Build outputs**: Constructs `SecurityGroupOutputs` with `GroupId`, `GroupArn`
   (synthesized), and `VpcId`.

8. **Commit state**: Sets status to `Ready`, saves state atomically, schedules
   reconciliation.

### Import Handler

The Import handler adopts an existing AWS security group:

1. Describes the group by `ref.ResourceID` (the AWS group ID).
2. Synthesizes a `SecurityGroupSpec` from the observed state via `specFromObserved()`.
   This ensures the first reconciliation sees no drift.
3. Commits state with the observed state as both desired baseline and observed
   snapshot.
4. Schedules reconciliation.

**`specFromObserved(obs ObservedState) SecurityGroupSpec`**: Converts observed rules
back to spec types, denormalizing `"all"` → `"-1"` for protocol and stripping the
`"cidr:"` prefix from targets. This round-trip ensures the synthesized spec produces
the same `NormalizedRule` set when `Normalize()` is applied.

### Delete Handler

1. Sets status to `Deleting`.
2. Calls `api.DeleteSecurityGroup` inside `restate.Run()`.
3. **Error classification inside the callback** (critical Restate pattern):
   - `IsDependencyViolation` → `TerminalError(409)` with clear message
   - `IsNotFound` → silent success (already gone)
   - Other errors → returned for Restate retry
4. On success, replaces state with a minimal `SecurityGroupState{Status: StatusDeleted}`.
5. On dependency violation, persists error state before returning terminal error.

> **Why dependency violation is terminal**: A security group referenced by ENIs, other
> security group rules, or EC2 instances cannot be deleted until those references are
> removed. Retrying is pointless — the user must fix the dependency first. The error
> message directs operators to check resource references.

### Reconcile Handler

Reconcile runs on a 5-minute timer (`drivers.ReconcileInterval`) and follows this
flow:

1. Clears `ReconcileScheduled` flag (prevents double-scheduling).
2. Skips if status is not `Ready` or `Error` (nothing to reconcile during
   `Provisioning` or `Deleting`).
3. Describes current AWS state.
4. If the group is gone (404), sets error status and re-schedules.
5. **Error status path**: Read-only describe, no correction. Reports drift status
   but does not attempt to fix. Re-schedules.
6. **Ready + Managed + drift**: Corrects rules via `applyRuleDiff()` and tags via
   `UpdateTags`. Reports `{Drift: true, Correcting: true}`. Re-schedules.
7. **Ready + Observed + drift**: Reports only. `{Drift: true, Correcting: false}`.
   Re-schedules.
8. **No drift**: Reports clean. Re-schedules.

Every path re-schedules via `scheduleReconcile()` to keep the reconciliation loop
running indefinitely.

### GetStatus / GetOutputs (Shared Handlers)

Both are `ObjectSharedContext` handlers that read state and return projections:

- `GetStatus` → `types.StatusResponse` (Status, Mode, Generation, Error)
- `GetOutputs` → `SecurityGroupOutputs` (GroupId, GroupArn, VpcId)

These run concurrently with exclusive handlers and never block Provision or Reconcile.

### scheduleReconcile

```go
func (d *SecurityGroupDriver) scheduleReconcile(ctx restate.ObjectContext, state *SecurityGroupState) {
    if state.ReconcileScheduled {
        return
    }
    state.ReconcileScheduled = true
    restate.Set(ctx, drivers.StateKey, *state)
    restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").
        Send(restate.Void{}, restate.WithDelay(drivers.ReconcileInterval))
}
```

Uses a `ReconcileScheduled` flag to prevent duplicate timers. The flag is cleared at
the start of Reconcile and set when scheduling. The delayed message is a Restate
durable timer that survives restarts.

### applyRuleDiff — Add-Before-Remove

```go
func (d *SecurityGroupDriver) applyRuleDiff(ctx, api, groupId, desired, observed) error
```

This is the core convergence function:

1. Normalize desired and observed rule sets.
2. Compute set diff (toAdd, toRemove).
3. Split each by direction (ingress/egress).
4. **Add new rules first** (authorize ingress, then authorize egress).
5. **Remove stale rules second** (revoke ingress, then revoke egress).

**Why add-before-remove**: If rules are removed first, there's a window where
legitimate traffic is blocked. By adding the new rules before removing the old ones,
the security group remains permissive during the transition. The worst case is a
brief period where both old and new rules coexist (more permissive than intended),
which is safer than a gap where rules are missing.

---

## Step 6 — Provider Adapter

**File**: `internal/core/provider/sg_adapter.go`

### SecurityGroupAdapter

The adapter bridges the generic resource document world (JSON, CUE, templates) to
the strongly-typed SG driver.

```go
type SecurityGroupAdapter struct {
    auth              *auth.Registry
    staticPlanningAPI sg.SGAPI        // injected in tests
    apiFactory        func(aws.Config) sg.SGAPI
}
```

### Constructors

- `NewSecurityGroupAdapter()`: Production, loads auth from env.
- `NewSecurityGroupAdapterWithRegistry(accounts)`: Production, explicit auth.
- `NewSecurityGroupAdapterWithAPI(api)`: Test-only, injects a fixed planning API.

### Methods

**`Scope() KeyScope`** → `KeyScopeCustom`

**`Kind() string`** → `"SecurityGroup"`

**`ServiceName() string`** → `"SecurityGroup"`

**`BuildKey(resourceDoc json.RawMessage) (string, error)`**:
Decodes the resource document, extracts `spec.vpcId` and `spec.groupName`, validates
both via `ValidateKeyPart()`, and returns `JoinKey(vpcId, groupName)` (i.e.,
`vpcId~groupName`).

**`BuildImportKey(region, resourceID string) (string, error)`**:
Returns `resourceID` directly (the AWS group ID). This is intentionally different
from `BuildKey` — imports target a separate Virtual Object keyed by the AWS group ID.

**`DecodeSpec(resourceDoc json.RawMessage) (any, error)`**:
Decodes and returns a `sg.SecurityGroupSpec`. Validates that `groupName` is non-empty.
Clears the `Account` field (it's injected at dispatch time, not from the template).

**`Provision(ctx, key, account, spec) (ProvisionInvocation, error)`**:
Dispatches to the SG Virtual Object's Provision handler. Returns a
`provisionHandle` that wraps the response future and provides `NormalizeOutputs`.

**`Delete(ctx, key) (DeleteInvocation, error)`**:
Dispatches to the SG Virtual Object's Delete handler.

**`NormalizeOutputs(raw any) (map[string]any, error)`**:
Converts `sg.SecurityGroupOutputs` to a generic map with keys: `groupId`,
`groupArn`, `vpcId`.

**`Import(ctx, key, account, ref) (types.ResourceStatus, map[string]any, error)`**:
Dispatches to the SG Virtual Object's Import handler. Returns the status and
normalized outputs.

**`Plan(ctx, key, account, desiredSpec) (types.DiffOperation, []types.FieldDiff, error)`**:
Queries AWS to determine the current state:

1. Calls `FindSecurityGroup(groupName, vpcId)` via `restate.Run()`.
2. If not found → returns `OpCreate` with field diffs synthesized from the spec.
3. If found → calls `sg.ComputeFieldDiffs()` to compare desired vs observed.
4. If no diffs → returns `OpNoOp`.
5. If diffs → returns `OpUpdate` with the field diffs.

The `FindSecurityGroup` call wraps the not-found case as a successful journal entry
(`describePlanResult{Found: false}`) rather than an error. This prevents
Restate from retrying a not-found response, which is a normal planning outcome.

---

## Step 7 — Registry Integration

**File**: `internal/core/provider/registry.go` (modified)

The `NewRegistry()` function registers the SG adapter:

```go
NewSecurityGroupAdapterWithRegistry(accounts),
```

No other changes were needed — the registry's adapter interface is generic enough to
handle all driver types.

---

## Step 8 — Network Driver Pack Entry Point & Dockerfile

### Entry Point

**File**: `cmd/praxis-network/main.go`

The SecurityGroup driver is added to the **network** driver pack. The Restate SDK supports binding multiple Virtual Objects to one server via chained `.Bind()` calls, so the network pack hosts all networking-related drivers (SG, and in the future VPC, ELB, Route 53, CloudFront, API Gateway).

```go
func main() {
    cfg := config.Load()

    srv := server.NewRestate().
        Bind(restate.Reflect(sg.NewSecurityGroupDriver(cfg.Auth())))

    slog.Info("starting network driver pack", "addr", cfg.ListenAddr)
    if err := srv.Start(context.Background(), cfg.ListenAddr); err != nil {
        slog.Error("network driver pack exited", "err", err.Error())
        os.Exit(1)
    }
}
```

Standard pattern: load config, create driver, bind to Restate, start server.

### Dockerfile

**File**: `cmd/praxis-network/Dockerfile`

```dockerfile
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /praxis-network ./cmd/praxis-network

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /praxis-network /praxis-network
ENTRYPOINT ["/praxis-network"]
```

Multi-stage build: Go 1.25 Alpine for compilation, distroless for runtime.
`CGO_ENABLED=0` for a fully static binary. `nonroot` base image for security.

---

## Step 9 — Docker Compose & Justfile

### Docker Compose

**File**: `docker-compose.yaml` (modified)

```yaml
praxis-network:
    build:
      context: .
      dockerfile: cmd/praxis-network/Dockerfile
    container_name: praxis-network
    env_file:
      - .env
    depends_on:
      restate:
        condition: service_healthy
      localstack:
        condition: service_healthy
    ports:
      - "9082:9080"
    environment:
      - PRAXIS_LISTEN_ADDR=0.0.0.0:9080
```

Listens on container port 9080, mapped to host port 9082 (Storage is 9081, Core is 9083, Compute is 9084).
Depends on both Restate and LocalStack being healthy.

### Justfile Targets

| Target | Command |
|---|---|
| `logs-network` | `docker compose logs -f praxis-network` |
| `test-sg` | `go test ./internal/drivers/sg/... -v -count=1 -race` |
| `test-sg-integration` | `go test ./tests/integration/ -run TestSG -v -count=1 -tags=integration -timeout=5m` |
| `build` (shared) | `go build -o bin/praxis-network ./cmd/praxis-network` |
| `register` (shared) | Registers network pack with Restate at `http://praxis-network:9080` |
| `up` (shared) | `docker compose up -d --build praxis-core praxis-storage praxis-network praxis-compute` |

---

## Step 10 — Unit Tests

### `internal/drivers/sg/drift_test.go`

Tests the normalization and drift detection logic:

| Test | Purpose |
|---|---|
| `TestNormalize_Empty` | Empty spec produces empty rules |
| `TestNormalize_IngressAndEgress` | Mixed rules expand correctly; protocols lowercased; `-1` → `"all"` |
| `TestNormalize_ProtocolNormalization` | `-1` → `"all"` specifically |
| `TestHasDrift_NoDrift` | Matching rules and tags → no drift |
| `TestHasDrift_RuleDrift` | Extra desired rule → drift |
| `TestHasDrift_TagDrift` | Tag value change → drift |
| `TestHasDrift_EmptyTagsNoDrift` | `{}` vs `nil` tags → no drift |
| `TestHasDrift_OrderIndependent` | Rules in different order → no drift |
| `TestHasDrift_ExtraObservedRule` | Rule added externally → drift |
| `TestComputeDiff_NoChanges` | Same sets → empty diff |
| `TestComputeDiff_AddOnly` | New rule → toAdd only |
| `TestComputeDiff_RemoveOnly` | Removed rule → toRemove only |
| `TestComputeDiff_AddAndRemove` | Different rules → both toAdd and toRemove |
| `TestComputeDiff_EmptyDesired` | All rules removed |
| `TestComputeDiff_EmptyObserved` | All rules added |
| `TestComputeDiff_MixedDirections` | Ingress/egress in same diff |
| `TestSplitByDirection` | Correctly splits ingress vs egress |

### `internal/drivers/sg/driver_test.go`

Tests driver-level functions:

| Test | Purpose |
|---|---|
| `TestSpecFromObserved_FullyPopulated` | Round-trip: observed → spec preserves all fields; `"all"` denormalized to `"-1"` |
| `TestSpecFromObserved_Empty` | Empty observed → empty rules |
| `TestSpecFromObserved_NilTags` | Nil tags preserved (not converted to empty map) |
| `TestServiceName` | `NewSecurityGroupDriver(nil).ServiceName()` returns `"SecurityGroup"` |
| `TestExtractCidr` | `"cidr:10.0.0.0/8"` → `"10.0.0.0/8"`; passthrough for non-prefixed |

### `internal/drivers/sg/aws_test.go`

Tests error classification:

| Test | Purpose |
|---|---|
| `TestIsDependencyViolation_MatchesWrappedErrorText` | String fallback matches `api error DependencyViolation:` pattern |
| `TestIsDependencyViolation_MatchesRestateWrappedPanicText` | String fallback matches Restate's double-wrapped panic error format |

---

## Step 11 — Integration Tests

**File**: `tests/integration/sg_driver_test.go`

Integration tests run against Testcontainers (Restate) + LocalStack (AWS). They
use `restatetest.Start()` to spin up a real Restate environment with the SG driver
registered. Each test gets a unique security group name via `uniqueSGName(t)` and
uses the default VPC from LocalStack.

### Helper Functions

- `uniqueSGName(t)`: Generates a test-unique SG name (sanitized test name + timestamp).
- `setupSGDriver(t)`: Configures LocalStack account, creates Restate test env, returns
  ingress client and EC2 SDK client.
- `getDefaultVpcId(t, ec2Client)`: Queries LocalStack for the default VPC ID.

### Test Cases

| Test | Description |
|---|---|
| `TestSGProvision_CreatesSecurityGroup` | Creates an SG with ingress/egress rules and tags. Verifies the group exists in LocalStack with the correct name. |
| `TestSGProvision_Idempotent` | Provisions the same spec twice on the same key. Verifies same GroupId is returned (no duplicate created). |
| `TestSGImport_ExistingGroup` | Creates an SG directly via EC2 API, then imports it via the driver. Verifies the driver returns the correct GroupId. |
| `TestSGDelete_RemovesGroup` | Provisions then deletes. Verifies the group is gone from LocalStack. |
| `TestSGReconcile_DetectsDrift` | Provisions, then adds an extra rule directly via EC2 API. Triggers reconcile and verifies `Drift: true, Correcting: true`. Verifies the extra rule was removed from LocalStack. |
| `TestSGGetStatus_ReturnsReady` | Provisions and checks `GetStatus` returns `Ready`, `Managed`, generation > 0. |

---

## SG-Specific Design Decisions

### 1. Custom Key Scope — Why not Region or Global?

S3 buckets are globally unique → `KeyScopeGlobal` (key = bucket name).
EC2 instances need region context → `KeyScopeRegion` (key = `region~name`).
Security groups are unique within a VPC → `KeyScopeCustom` (key = `vpcId~groupName`).

A region-scoped key would fail for users with multiple VPCs in the same region who
reuse group names (common: "web-sg" in prod VPC and staging VPC).

### 2. Add-Before-Remove Rule Application

When converging rules during Provision or Reconcile, the driver always:
1. Authorizes new ingress rules
2. Authorizes new egress rules
3. Revokes stale ingress rules
4. Revokes stale egress rules

This ordering ensures there is never a window where legitimate traffic is blocked.
The brief period of having both old and new rules (more permissive) is considered
safer than a gap where rules are missing (less permissive, breaking traffic).

### 3. Duplicate Permission Absorption

AWS creates a default egress rule (`-1`/`0.0.0.0/0`) when a security group is
created. If the user's spec also includes this default rule, `AuthorizeEgress` will
return `InvalidPermission.Duplicate`. The driver silently absorbs this error in the
`SGAPI` layer, keeping Provision idempotent without requiring the driver to
special-case default rules.

### 4. Import Key ≠ BuildKey

For most template operations, the key is `vpcId~groupName` (derived from the spec).
For imports, the key is the raw AWS group ID (`sg-0abc123`). This means importing
and then provisioning the same group via a template creates two separate Virtual
Objects. This is consistent with the S3 driver (where import key = bucket name =
BuildKey) and intentional for the SG case: the import captures the current state
under the AWS identifier, while the template manages state under the stable
declarative identity.

### 5. IsDependencyViolation String Matching

`restate.Run()` panics on non-terminal errors, destroying the structured error type.
The `IsDependencyViolation` classifier uses three string patterns as fallback:
- `"DependencyViolation"` (AWS error code in the message)
- `"resource is still in use"` (common AWS error description)
- `"still referenced by other resources"` (Praxis error message from state)

This defense-in-depth approach handles both direct AWS errors and Restate-wrapped
errors.

### 6. No Ownership Tag (Unlike EC2)

The EC2 driver uses a `praxis:managed-key` tag for conflict detection because EC2
instance names (Name tags) are mutable and non-unique. Security groups don't need
this because `groupName + vpcId` is enforced unique by AWS itself — you cannot create
two security groups with the same name in the same VPC. The `IsDuplicate` error
classifier handles conflicts instead.

### 7. ARN Synthesis

The GroupArn is synthesized locally as
`arn:aws:ec2:{region}:000000000000:security-group/{groupId}` rather than fetched from
AWS. AWS does not return the ARN directly from `CreateSecurityGroup` or
`DescribeSecurityGroups`. The account number is hardcoded to `000000000000` for
LocalStack compatibility; production would use the resolved account ID.

### 8. Plan Uses FindSecurityGroup, Not GetOutputs

Unlike the EC2 adapter's Plan (which uses `GetOutputs` to find the instance ID from
VO state), the SG adapter's Plan calls `FindSecurityGroup(groupName, vpcId)` directly
against AWS. This is valid because SG names are unique within a VPC and immutable,
making them a reliable lookup key — unlike EC2 Name tags which are mutable.

### 9. Error State Reconciliation

When reconciliation encounters an error (transient AWS failure, group deleted
externally), the driver transitions to Error status but continues scheduling
reconciliation cycles. The Error-status reconciliation path is read-only: it
describes the current state but does not attempt corrections. This allows the driver
to self-heal when the transient error resolves (e.g., the AWS API recovers).

---

## Checklist

- [x] CUE schema at `schemas/aws/ec2/sg.cue`
  - [x] `#SecurityGroup` definition with metadata, spec, optional outputs
  - [x] `#Rule` type with protocol, port range, CIDR validation
  - [x] Default egress rule (allow-all)
  - [x] Default empty ingress rules
- [x] Driver types at `internal/drivers/sg/types.go`
  - [x] `SecurityGroupSpec` with all fields
  - [x] Separate `IngressRule` and `EgressRule` types
  - [x] `SecurityGroupOutputs` (GroupId, GroupArn, VpcId)
  - [x] `ObservedState` with `NormalizedRule` slices
  - [x] `SecurityGroupState` with all lifecycle fields
- [x] AWS API layer at `internal/drivers/sg/aws.go`
  - [x] `SGAPI` interface with 9 methods
  - [x] `realSGAPI` implementation with rate limiting
  - [x] `DescribeSecurityGroup` with rule normalization
  - [x] `FindSecurityGroup` by name + VPC ID
  - [x] `CreateSecurityGroup`
  - [x] `DeleteSecurityGroup`
  - [x] `AuthorizeIngress`/`AuthorizeEgress` with duplicate absorption
  - [x] `RevokeIngress`/`RevokeEgress`
  - [x] `UpdateTags` with delete-then-create
  - [x] `rulesToIpPermissions` with CIDR batching
  - [x] Protocol normalization (`normalizeProtocol`, `denormalizeProtocol`)
  - [x] Error classifiers: `IsNotFound`, `IsDuplicate`, `isDuplicatePermission`, `IsInvalidParam`, `IsDependencyViolation`
  - [x] String fallback in `IsDependencyViolation` for Restate-wrapped errors
- [x] Drift detection at `internal/drivers/sg/drift.go`
  - [x] `NormalizedRule` with `ruleKey()` for set comparison
  - [x] `Normalize()` — spec → sorted NormalizedRule slice
  - [x] `HasDrift()` — rules + tags comparison
  - [x] `ComputeDiff()` — set difference
  - [x] `ComputeFieldDiffs()` — human-readable diffs for plan renderer
  - [x] `SplitByDirection()` helper
  - [x] Helper functions: `mergeObservedRules`, `rulesEqual`, `sortRules`, `tagsMatch`
- [x] Driver at `internal/drivers/sg/driver.go`
  - [x] `Provision` — create or converge, add-before-remove, idempotent
  - [x] `Import` — describe, synthesize spec, no-drift baseline
  - [x] `Delete` — dependency violation handling, error classification inside callback
  - [x] `Reconcile` — drift detection, Managed correction, Observed report-only
  - [x] `GetStatus` (shared handler)
  - [x] `GetOutputs` (shared handler)
  - [x] `scheduleReconcile` with dedup flag
  - [x] `applyRuleDiff` — add-before-remove convergence
  - [x] `apiForAccount` — per-request AWS config resolution
  - [x] `specFromObserved` — import round-trip
- [x] Provider adapter at `internal/core/provider/sg_adapter.go`
  - [x] `Scope()` → `KeyScopeCustom`
  - [x] `BuildKey()` → `vpcId~groupName`
  - [x] `BuildImportKey()` → raw group ID
  - [x] `Plan()` with `FindSecurityGroup` + `ComputeFieldDiffs`
  - [x] `Provision()`, `Delete()`, `Import()`
  - [x] `NormalizeOutputs()`
  - [x] `decodeSpec()` with groupName validation
  - [x] `planningAPI()` with static override for tests
- [x] Registry integration — `NewSecurityGroupAdapterWithRegistry` in `NewRegistry()`
- [x] Binary entry point in `cmd/praxis-network/main.go`
- [x] Dockerfile at `cmd/praxis-network/Dockerfile`
- [x] Docker Compose service (port 9082)
- [x] Justfile targets: `logs-network`, `test-sg`, `test-sg-integration`, build, register
- [x] Unit tests
  - [x] `drift_test.go` — 17 tests covering normalization, drift, diff, split
  - [x] `driver_test.go` — 5 tests covering specFromObserved, ServiceName, extractCidr
  - [x] `aws_test.go` — 2 tests covering IsDependencyViolation string matching
- [x] Integration tests at `tests/integration/sg_driver_test.go`
  - [x] `TestSGProvision_CreatesSecurityGroup`
  - [x] `TestSGProvision_Idempotent`
  - [x] `TestSGImport_ExistingGroup`
  - [x] `TestSGDelete_RemovesGroup`
  - [x] `TestSGReconcile_DetectsDrift` (end-to-end drift correction)
  - [x] `TestSGGetStatus_ReturnsReady`
