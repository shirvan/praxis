# Target Group Driver — Implementation Plan

> A Restate Virtual Object driver that manages ELBv2 Target Groups,
> providing full lifecycle management including creation, import, deletion, drift
> detection, and drift correction for health check configuration, target
> registrations, stickiness settings, and tags.
>
> Key scope: `KeyScopeRegion` — key format is `region~tgName`, permanent and
> immutable for the lifetime of the Virtual Object. The AWS-assigned target group
> ARN lives only in state/outputs.
>
> **Note:** Integration tests are present but skip on LocalStack community edition
> (v4.x) because ELBv2 is a paid-tier service. They should run against a real AWS
> account.

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
11. [Step 8 — Unit Tests](#step-8--unit-tests)
12. [Step 9 — Integration Tests](#step-9--integration-tests)
13. [Target-Group-Specific Design Decisions](#target-group-specific-design-decisions)
14. [Checklist](#checklist)

---

## 1. Overview & Scope

The Target Group driver manages the lifecycle of **ELBv2 Target Groups** only. It
creates, imports, updates, and deletes target groups along with their health check
configuration, target registrations, stickiness settings, deregistration delay, and
tags.

Target groups define the destination for traffic routed by load balancer listeners
and listener rules. They support three target types: `instance` (EC2 instance IDs),
`ip` (IP addresses), and `lambda` (Lambda function ARNs). In compound templates,
target groups are referenced by Listener `defaultActions` and Listener Rule
`actions`.

**Out of scope**: Load balancers (ALB/NLB drivers), Listeners (separate driver),
Listener Rules (separate driver). Each is a distinct resource type.

### Driver Contract

| Handler | Context | Purpose |
|---|---|---|
| `Provision` | `ObjectContext` (exclusive) | Create or converge a target group |
| `Import` | `ObjectContext` (exclusive) | Adopt an existing target group |
| `Delete` | `ObjectContext` (exclusive) | Remove a target group (deregisters targets first) |
| `Reconcile` | `ObjectContext` (exclusive) | Detect/correct drift (report-only for Observed mode) |
| `GetStatus` | `ObjectSharedContext` (shared) | Return current status |
| `GetOutputs` | `ObjectSharedContext` (shared) | Return target group outputs |

### Mutable vs Immutable Attributes

| Attribute | Mutability | Notes |
|---|---|---|
| `name` | Immutable | Part of the Virtual Object key; cannot change after creation |
| `protocol` | Immutable | HTTP, HTTPS, TCP, UDP, TLS, TCP_UDP; requires delete + recreate |
| `port` | Immutable | Default port for targets; requires delete + recreate |
| `vpcId` | Immutable | Target group is bound to a VPC; requires delete + recreate |
| `targetType` | Immutable | `instance`, `ip`, or `lambda`; requires delete + recreate |
| `protocolVersion` | Immutable | `HTTP1`, `HTTP2`, `gRPC`; requires delete + recreate |
| `healthCheck` | Mutable | Updated via `ModifyTargetGroup` |
| `deregistrationDelay` | Mutable | Updated via `ModifyTargetGroupAttributes` |
| `stickiness` | Mutable | Updated via `ModifyTargetGroupAttributes` |
| `targets` | Mutable | Updated via `RegisterTargets` / `DeregisterTargets` |
| `tags` | Mutable | Full replace via `RemoveTags` + `AddTags` |

### Downstream Consumers

```text
${resources.my-tg.outputs.targetGroupArn}  → Listener defaultActions, Listener Rule actions
${resources.my-tg.outputs.targetGroupName} → Informational references
```

---

## 2. Key Strategy

### Key Scope: `KeyScopeRegion`

Target groups are regional resources. Target group names are unique within a region
and account.

```text
region~tgName
```

### BuildKey vs BuildImportKey

- **`BuildKey(resourceDoc)`**: Extracts `metadata.name`, prepends region. Returns
  `region~name`.

- **`BuildImportKey(region, resourceID)`**: Returns `region~resourceID`. The
  `resourceID` is the target group name. Import and template management produce the
  same key.

### No Ownership Tags

Target group names are unique per region per account (AWS-enforced).
`CreateTargetGroup` returns `DuplicateTargetGroupName` if the name already exists.

---

## 3. File Inventory

```text
✦ schemas/aws/elb/target_group.cue                     — CUE schema for TargetGroup resource
✦ internal/drivers/targetgroup/types.go                 — Spec, Outputs, ObservedState, State structs
✦ internal/drivers/targetgroup/aws.go                   — TargetGroupAPI interface + realTargetGroupAPI
✦ internal/drivers/targetgroup/drift.go                 — HasDrift(), ComputeFieldDiffs()
✦ internal/drivers/targetgroup/driver.go                — TargetGroupDriver Virtual Object
✦ internal/drivers/targetgroup/driver_test.go           — Unit tests for driver (mocked AWS)
✦ internal/drivers/targetgroup/aws_test.go              — Unit tests for error classification
✦ internal/drivers/targetgroup/drift_test.go            — Unit tests for drift detection
✦ internal/core/provider/targetgroup_adapter.go         — TargetGroupAdapter implementing provider.Adapter
✦ internal/core/provider/targetgroup_adapter_test.go    — Unit tests for adapter
✦ tests/integration/targetgroup_driver_test.go          — Integration tests
✎ internal/core/provider/registry.go                    — Add NewTargetGroupAdapter to NewRegistry()
```

---

## Step 1 — CUE Schema

**File**: `schemas/aws/elb/target_group.cue`

```cue
package elb

#TargetGroup: {
    apiVersion: "praxis.io/v1"
    kind:       "TargetGroup"

    metadata: {
        name: string & =~"^[a-zA-Z0-9]([a-zA-Z0-9-]{0,30}[a-zA-Z0-9])?$"
        labels: [string]: string
    }

    spec: {
        // name is the target group name in AWS.
        name: string & =~"^[a-zA-Z0-9]([a-zA-Z0-9-]{0,30}[a-zA-Z0-9])?$"

        // account is the target AWS account alias (optional).
        account?: string

        // protocol for routing traffic to targets.
        // ALB targets: HTTP, HTTPS
        // NLB targets: TCP, UDP, TLS, TCP_UDP
        protocol: "HTTP" | "HTTPS" | "TCP" | "UDP" | "TLS" | "TCP_UDP"

        // port is the default port for targets.
        port: int & >=1 & <=65535

        // vpcId is the VPC containing the targets.
        // Required for instance and ip target types.
        vpcId: string

        // targetType determines how targets are specified.
        targetType: "instance" | "ip" | "lambda" | *"instance"

        // protocolVersion for HTTP/HTTPS target groups.
        protocolVersion?: "HTTP1" | "HTTP2" | "gRPC"

        // healthCheck configuration.
        healthCheck: {
            // protocol used for health checks.
            protocol: "HTTP" | "HTTPS" | "TCP" | *"HTTP"

            // path for HTTP/HTTPS health checks.
            path?: string

            // port for health checks. "traffic-port" uses the target's port.
            port: string | *"traffic-port"

            // healthyThreshold is the number of consecutive successes before
            // considering a target healthy.
            healthyThreshold: int & >=2 & <=10 | *5

            // unhealthyThreshold is the number of consecutive failures before
            // considering a target unhealthy.
            unhealthyThreshold: int & >=2 & <=10 | *2

            // interval is the time between health checks in seconds.
            interval: int & >=5 & <=300 | *30

            // timeout is the health check timeout in seconds.
            timeout: int & >=2 & <=120 | *5

            // matcher defines the HTTP response codes for a healthy check.
            matcher?: string
        }

        // deregistrationDelay is the time to wait before deregistering a target
        // (in seconds). Range: 0-3600.
        deregistrationDelay: int & >=0 & <=3600 | *300

        // stickiness configuration for session affinity.
        stickiness?: {
            enabled:  bool | *false
            type:     "lb_cookie" | "app_cookie" | *"lb_cookie"
            duration: int & >=1 & <=604800 | *86400
        }

        // targets is the list of registered targets.
        targets: [...#Target] | *[]

        // tags applied to the target group.
        tags: [string]: string
    }

    outputs?: {
        targetGroupArn:  string
        targetGroupName: string
    }
}

#Target: {
    // id is the target identifier: instance ID, IP address, or Lambda ARN.
    id:    string
    // port overrides the default target group port for this target.
    port?: int & >=1 & <=65535
    // availabilityZone for cross-AZ IP targets ("all" for Lambda, specific AZ for IP).
    availabilityZone?: string
}
```

### Key Design Decisions

- **Target group name regex**: Same constraint as load balancers — 1-32 characters,
  alphanumeric and hyphens, no leading/trailing hyphen.

- **`targetType` defaults to `instance`**: Most common use case. IP and Lambda target
  types change the semantics of the `targets[].id` field.

- **`healthCheck` always required**: A target group without health checks is almost
  always a misconfiguration. Defaults match AWS defaults for HTTP health checks.

- **`targets` as declarative list**: The driver reconciles the target list — it
  computes the diff between desired and registered targets and calls
  `RegisterTargets`/`DeregisterTargets` accordingly. This is an add-before-remove
  operation to minimize traffic disruption.

- **`protocolVersion` optional**: Only meaningful for HTTP/HTTPS target groups. Omitted
  for TCP/UDP/TLS target groups.

- **`matcher` as string**: AWS accepts comma-separated HTTP codes (e.g., "200,302")
  or ranges ("200-299"). String type avoids complex CUE validation for this format.

---

## Step 2 — Driver Types

**File**: `internal/drivers/targetgroup/types.go`

```go
package targetgroup

import "github.com/shirvan/praxis/pkg/types"

const ServiceName = "TargetGroup"

type TargetGroupSpec struct {
    Account             string            `json:"account,omitempty"`
    Name                string            `json:"name"`
    Protocol            string            `json:"protocol"`
    Port                int               `json:"port"`
    VpcId               string            `json:"vpcId"`
    TargetType          string            `json:"targetType"`
    ProtocolVersion     string            `json:"protocolVersion,omitempty"`
    HealthCheck         HealthCheck       `json:"healthCheck"`
    DeregistrationDelay int               `json:"deregistrationDelay"`
    Stickiness          *Stickiness       `json:"stickiness,omitempty"`
    Targets             []Target          `json:"targets,omitempty"`
    Tags                map[string]string `json:"tags,omitempty"`
}

type HealthCheck struct {
    Protocol            string `json:"protocol"`
    Path                string `json:"path,omitempty"`
    Port                string `json:"port"`
    HealthyThreshold    int32  `json:"healthyThreshold"`
    UnhealthyThreshold  int32  `json:"unhealthyThreshold"`
    Interval            int32  `json:"interval"`
    Timeout             int32  `json:"timeout"`
    Matcher             string `json:"matcher,omitempty"`
}

type Stickiness struct {
    Enabled  bool   `json:"enabled"`
    Type     string `json:"type"`
    Duration int    `json:"duration"`
}

type Target struct {
    Id               string `json:"id"`
    Port             int    `json:"port,omitempty"`
    AvailabilityZone string `json:"availabilityZone,omitempty"`
}

type TargetGroupOutputs struct {
    TargetGroupArn  string `json:"targetGroupArn"`
    TargetGroupName string `json:"targetGroupName"`
}

type ObservedState struct {
    TargetGroupArn      string            `json:"targetGroupArn"`
    Name                string            `json:"name"`
    Protocol            string            `json:"protocol"`
    Port                int               `json:"port"`
    VpcId               string            `json:"vpcId"`
    TargetType          string            `json:"targetType"`
    ProtocolVersion     string            `json:"protocolVersion"`
    HealthCheck         HealthCheck       `json:"healthCheck"`
    DeregistrationDelay int               `json:"deregistrationDelay"`
    Stickiness          *Stickiness       `json:"stickiness,omitempty"`
    Targets             []Target          `json:"targets"`
    Tags                map[string]string `json:"tags"`
}

type TargetGroupState struct {
    Desired            TargetGroupSpec      `json:"desired"`
    Observed           ObservedState        `json:"observed"`
    Outputs            TargetGroupOutputs   `json:"outputs"`
    Status             types.ResourceStatus `json:"status"`
    Mode               types.Mode           `json:"mode"`
    Error              string               `json:"error,omitempty"`
    Generation         int64                `json:"generation"`
    LastReconcile      string               `json:"lastReconcile,omitempty"`
    ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
```

---

## Step 3 — AWS API Abstraction Layer

**File**: `internal/drivers/targetgroup/aws.go`

### TargetGroupAPI Interface

```go
type TargetGroupAPI interface {
    // CreateTargetGroup creates a new target group.
    CreateTargetGroup(ctx context.Context, spec TargetGroupSpec) (arn string, err error)

    // DescribeTargetGroup returns the observed state of a target group by ARN.
    DescribeTargetGroup(ctx context.Context, arn string) (ObservedState, error)

    // FindTargetGroup looks up a target group by name.
    FindTargetGroup(ctx context.Context, name string) (ObservedState, error)

    // DeleteTargetGroup deletes a target group by ARN.
    DeleteTargetGroup(ctx context.Context, arn string) error

    // ModifyTargetGroup updates the health check configuration.
    ModifyTargetGroup(ctx context.Context, arn string, healthCheck HealthCheck) error

    // ModifyAttributes updates target group attributes (deregistration delay,
    // stickiness, etc.).
    ModifyAttributes(ctx context.Context, arn string, attrs map[string]string) error

    // RegisterTargets registers targets with the target group.
    RegisterTargets(ctx context.Context, arn string, targets []Target) error

    // DeregisterTargets deregisters targets from the target group.
    DeregisterTargets(ctx context.Context, arn string, targets []Target) error

    // DescribeTargetHealth returns the list of registered targets.
    DescribeTargetHealth(ctx context.Context, arn string) ([]Target, error)

    // UpdateTags replaces all user tags on the target group.
    UpdateTags(ctx context.Context, arn string, desired map[string]string) error
}
```

### realTargetGroupAPI Implementation

```go
type realTargetGroupAPI struct {
    client  *elasticloadbalancingv2.Client
    limiter *ratelimit.Limiter
}

func NewTargetGroupAPI(client *elasticloadbalancingv2.Client) TargetGroupAPI {
    return &realTargetGroupAPI{
        client:  client,
        limiter: ratelimit.New("target-group", 15, 8),
    }
}
```

### Key Implementation Details

#### Composite Describe

The describe operation requires multiple API calls:

1. `DescribeTargetGroups` — base target group attributes
2. `DescribeTargetGroupAttributes` — deregistration delay, stickiness
3. `DescribeTargetHealth` — list of registered targets
4. `DescribeTags` — resource tags

All calls are made within a single `restate.Run` block.

#### Target Registration (Add-Before-Remove)

When reconciling targets, the driver:

1. Computes `toRegister` = desired - observed
2. Computes `toDeregister` = observed - desired
3. Calls `RegisterTargets(toRegister)` **first** (add before remove)
4. Calls `DeregisterTargets(toDeregister)` **second**

This minimizes the window where the target group has fewer healthy targets than
intended.

### Error Classification

| Function | AWS Error Code(s) | Semantics |
|---|---|---|
| `IsNotFound` | `TargetGroupNotFound` | Target group doesn't exist |
| `IsDuplicate` | `DuplicateTargetGroupName` | Name already exists |
| `IsResourceInUse` | `ResourceInUse` | Target group referenced by listener/rule |
| `IsTooMany` | `TooManyTargetGroups` | Account quota exceeded |
| `IsInvalidTarget` | `InvalidTarget` | Target ID doesn't exist or is invalid |

---

## Step 4 — Drift Detection

**File**: `internal/drivers/targetgroup/drift.go`

### Drift Comparison Fields

| Field | Comparison Strategy |
|---|---|
| `healthCheck.protocol` | String equality |
| `healthCheck.path` | String equality |
| `healthCheck.port` | String equality |
| `healthCheck.healthyThreshold` | Integer equality |
| `healthCheck.unhealthyThreshold` | Integer equality |
| `healthCheck.interval` | Integer equality |
| `healthCheck.timeout` | Integer equality |
| `healthCheck.matcher` | String equality |
| `deregistrationDelay` | Integer equality |
| `stickiness.enabled` | Bool equality |
| `stickiness.type` | String equality |
| `stickiness.duration` | Integer equality |
| `targets` | Sorted set comparison (by id + port) |
| `tags` | Map equality |

Immutable fields (`name`, `protocol`, `port`, `vpcId`, `targetType`,
`protocolVersion`) are not compared for drift.

### Target Normalization

Targets are normalized to a canonical form for comparison:

- Sorted by `(id, port)`
- Default port (0 or omitted) is treated as the target group's default port
- `availabilityZone` is normalized (empty string ≡ omitted)

---

## Step 5 — Driver Implementation

**File**: `internal/drivers/targetgroup/driver.go`

### TargetGroupDriver Struct

```go
type TargetGroupDriver struct {
    accounts *auth.Registry
}

func NewTargetGroupDriver(accounts *auth.Registry) *TargetGroupDriver {
    return &TargetGroupDriver{accounts: accounts}
}

func (d *TargetGroupDriver) ServiceName() string { return ServiceName }
```

### Provision Flow

1. Load existing state
2. If target group exists → check for spec changes → converge
3. If target group doesn't exist:
   a. `CreateTargetGroup` (wrapped in `restate.Run`)
   b. Set attributes (deregistration delay, stickiness)
   c. Register targets
4. Save state, schedule reconciliation, return outputs

Target groups become usable immediately after creation (no provisioning delay like
ALBs).

### Convergence

When the spec changes:

1. **Health check** → `ModifyTargetGroup`
2. **Attributes** → `ModifyTargetGroupAttributes` (deregistration delay, stickiness)
3. **Targets** → `RegisterTargets` / `DeregisterTargets` (add-before-remove)
4. **Tags** → `RemoveTags` + `AddTags`

### Delete Flow

1. Deregister all targets
2. Call `DeleteTargetGroup`
3. Clear all state

The driver deregisters targets before deletion to ensure clean teardown. If the
target group is still referenced by a listener or rule, `DeleteTargetGroup` returns
`ResourceInUse`, which becomes a terminal error — the Listener/Listener Rule must be
deleted first.

---

## Step 6 — Provider Adapter

**File**: `internal/core/provider/targetgroup_adapter.go`

```go
type TargetGroupAdapter struct {
    accounts *auth.Registry
}

func NewTargetGroupAdapterWithRegistry(accounts *auth.Registry) *TargetGroupAdapter {
    return &TargetGroupAdapter{accounts: accounts}
}

func (a *TargetGroupAdapter) Kind() string             { return "TargetGroup" }
func (a *TargetGroupAdapter) ServiceName() string      { return "TargetGroup" }
func (a *TargetGroupAdapter) Scope() KeyScope          { return KeyScopeRegion }
```

### Plan Method

The Plan method checks for immutable field changes that require recreate:

- `protocol` changed → `PlanActionRecreate`
- `port` changed → `PlanActionRecreate`
- `vpcId` changed → `PlanActionRecreate`
- `targetType` changed → `PlanActionRecreate`
- `protocolVersion` changed → `PlanActionRecreate`

---

## Step 7 — Registry Integration

Add `NewTargetGroupAdapterWithRegistry` to `internal/core/provider/registry.go`.

---

## Step 8 — Unit Tests

**File**: `internal/drivers/targetgroup/driver_test.go`

| Test | Description |
|---|---|
| `TestServiceName` | Verify `ServiceName()` returns `"TargetGroup"` |
| `TestSpecFromObserved` | Verify building a spec from observed state |
| `TestDuplicateNameHandling` | Verify `DuplicateTargetGroupName` → terminal 409 |

**File**: `internal/drivers/targetgroup/drift_test.go`

| Test | Description |
|---|---|
| `TestNoDrift` | Identical desired and observed → no drift |
| `TestHealthCheckDrift` | Changed health check config → drift detected |
| `TestTargetDrift` | Different target sets → drift detected |
| `TestStickinesssDrift` | Changed stickiness config → drift detected |
| `TestDeregistrationDelayDrift` | Changed delay → drift detected |
| `TestTagDrift` | Changed tags → drift detected |

---

## Step 9 — Integration Tests

**File**: `tests/integration/targetgroup_driver_test.go`

### Prerequisites

- LocalStack with ELBv2 support
- Pre-existing VPC

### Test Scenarios

| Test | Description |
|---|---|
| `TestTargetGroupProvision` | Create TG, verify outputs, verify Ready status |
| `TestTargetGroupProvisionIdempotent` | Provision twice → no-op on second call |
| `TestTargetGroupImport` | Import existing TG |
| `TestTargetGroupUpdateHealthCheck` | Change health check → verify convergence |
| `TestTargetGroupRegisterTargets` | Add targets → verify registered |
| `TestTargetGroupDeregisterTargets` | Remove targets → verify deregistered |
| `TestTargetGroupDelete` | Delete TG, verify Deleted status |
| `TestTargetGroupDeleteInUse` | Delete TG referenced by listener → terminal error |
| `TestTargetGroupReconcile` | External modification → reconcile corrects drift |
| `TestTargetGroupDuplicateName` | Provision with existing name → terminal 409 |

---

## Target-Group-Specific Design Decisions

### 1. Target Registration as Part of Spec

Targets are declared as part of the target group spec rather than managed separately.
This means the driver owns the full target set — external targets not in the spec
will be deregistered during reconciliation (in Managed mode). This is consistent
with how Security Group rules and Route Table routes are managed.

In Observed mode, target registrations are recorded but not mutated.

### 2. Lambda Target Type

When `targetType: "lambda"`, the `vpcId` field is not required and the `targets[].id`
field contains the Lambda function ARN. The driver detects this and skips VPC-specific
validation. Lambda targets also require the `lambda:InvokeFunction` permission on the
ELB service principal, which is outside the scope of this driver.

### 3. Health Check Defaults

AWS health check defaults differ between ALB and NLB target groups. The CUE schema
uses ALB-oriented defaults (HTTP, `/`, 30s interval). For NLB target groups with TCP
protocol, users should explicitly set health check protocol to "TCP" and omit the
path.

### 4. Immutable Field Recreation

Many target group fields are immutable after creation (protocol, port, vpcId,
targetType). If a user changes these in their template, the Plan handler returns
`PlanActionRecreate`. The deployment orchestrator handles the delete-then-create
sequence. The driver itself never performs implicit recreation — it returns a terminal
error if a Provision call receives a spec with different immutable fields than the
existing resource.

---

## Checklist

- [x] `schemas/aws/elb/target_group.cue` created
- [x] `internal/drivers/targetgroup/types.go` created
- [x] `internal/drivers/targetgroup/aws.go` created
- [x] `internal/drivers/targetgroup/drift.go` created
- [x] `internal/drivers/targetgroup/driver.go` created
- [x] `internal/drivers/targetgroup/driver_test.go` created
- [x] `internal/drivers/targetgroup/aws_test.go` created
- [x] `internal/drivers/targetgroup/drift_test.go` created
- [x] `internal/core/provider/targetgroup_adapter.go` created
- [x] `internal/core/provider/registry.go` updated
- [x] `tests/integration/targetgroup_driver_test.go` created
