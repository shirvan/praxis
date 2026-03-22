# Listener Driver — Implementation Plan

> NYI
> Target: A Restate Virtual Object driver that manages ELBv2 Listeners, providing
> full lifecycle management including creation, import, deletion, drift detection,
> and drift correction for port, protocol, SSL configuration, default actions, and
> tags.
>
> Key scope: `KeyScopeRegion` — key format is `region~listenerName`, permanent and
> immutable for the lifetime of the Virtual Object. The AWS-assigned listener ARN
> lives only in state/outputs.

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
13. [Listener-Specific Design Decisions](#listener-specific-design-decisions)
14. [Checklist](#checklist)

---

## 1. Overview & Scope

The Listener driver manages the lifecycle of **ELBv2 Listeners** only. It creates,
imports, updates, and deletes listeners on Application Load Balancers and Network
Load Balancers. A listener defines the port and protocol on which the load balancer
accepts traffic, along with a default routing action.

Listeners are the bridge between a load balancer and target groups. Every load
balancer needs at least one listener. HTTPS/TLS listeners additionally manage SSL
certificate associations and SSL policy selection.

**Out of scope**: ALBs and NLBs (separate drivers), Listener Rules (separate driver),
Target Groups (separate driver), ACM certificates (future driver).

### Driver Contract

| Handler | Context | Purpose |
|---|---|---|
| `Provision` | `ObjectContext` (exclusive) | Create or converge a listener |
| `Import` | `ObjectContext` (exclusive) | Adopt an existing listener |
| `Delete` | `ObjectContext` (exclusive) | Remove a listener |
| `Reconcile` | `ObjectContext` (exclusive) | Detect/correct drift (report-only for Observed mode) |
| `GetStatus` | `ObjectSharedContext` (shared) | Return current status |
| `GetOutputs` | `ObjectSharedContext` (shared) | Return listener outputs |

### Mutable vs Immutable Attributes

| Attribute | Mutability | Notes |
|---|---|---|
| `loadBalancerArn` | Immutable | Listener is bound to a specific LB; requires delete + recreate to move |
| `port` | Mutable | Updated via `ModifyListener`; must be unique per LB |
| `protocol` | Mutable | Updated via `ModifyListener`; protocol change may require certificate updates |
| `sslPolicy` | Mutable | Updated via `ModifyListener`; only relevant for HTTPS/TLS |
| `certificateArn` | Mutable | Updated via `ModifyListener`; only relevant for HTTPS/TLS |
| `defaultActions` | Mutable | Updated via `ModifyListener` |
| `alpnPolicy` | Mutable | Updated via `ModifyListener`; only relevant for TLS (NLB) |
| `tags` | Mutable | Full replace via `RemoveTags` + `AddTags` |

### Downstream Consumers

```
${resources.my-listener.outputs.listenerArn}  → Listener Rule's listenerArn
${resources.my-listener.outputs.port}          → Informational references
```

---

## 2. Key Strategy

### Key Scope: `KeyScopeRegion`

Listeners are regional resources tied to a load balancer. AWS identifies listeners
by ARN, not by name. Praxis uses a user-provided logical name as the key.

```
region~listenerName
```

### BuildKey vs BuildImportKey

- **`BuildKey(resourceDoc)`**: Extracts `metadata.name`, prepends region. Returns
  `region~name`.

- **`BuildImportKey(region, resourceID)`**: Returns `region~resourceID`. The
  `resourceID` is either the listener ARN or a user-provided name. Since AWS
  listeners don't have user-defined names, the import key may differ from the
  template key for the same underlying listener.

### No Ownership Tags

Listeners are identified by ARN and are uniquely scoped to their parent load
balancer. There is no name-collision risk across load balancers. A listener on
port 443 on ALB-A is a different ARN from a listener on port 443 on ALB-B.

However, within a single load balancer, port numbers must be unique. The driver
uses the `DuplicateListener` error code to detect conflicts.

### Naming Convention

AWS listeners don't have a `Name` field — they're identified by ARN, which includes
the LB ARN and a random suffix. Praxis adds a logical name via `metadata.name` for
Virtual Object keying. This name is tracked in tags (`praxis:listener-name`) for
import reconciliation.

---

## 3. File Inventory

```text
✦ schemas/aws/elb/listener.cue                        — CUE schema for Listener resource
✦ internal/drivers/listener/types.go                   — Spec, Outputs, ObservedState, State structs
✦ internal/drivers/listener/aws.go                     — ListenerAPI interface + realListenerAPI
✦ internal/drivers/listener/drift.go                   — HasDrift(), ComputeFieldDiffs()
✦ internal/drivers/listener/driver.go                  — ListenerDriver Virtual Object
✦ internal/drivers/listener/driver_test.go             — Unit tests for driver (mocked AWS)
✦ internal/drivers/listener/aws_test.go                — Unit tests for error classification
✦ internal/drivers/listener/drift_test.go              — Unit tests for drift detection
✦ internal/core/provider/listener_adapter.go           — ListenerAdapter implementing provider.Adapter
✦ internal/core/provider/listener_adapter_test.go      — Unit tests for adapter
✦ tests/integration/listener_driver_test.go            — Integration tests
✎ internal/core/provider/registry.go                   — Add NewListenerAdapter to NewRegistry()
```

---

## Step 1 — CUE Schema

**File**: `schemas/aws/elb/listener.cue`

```cue
package elb

#Listener: {
    apiVersion: "praxis.io/v1"
    kind:       "Listener"

    metadata: {
        name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9_-]{0,62}[a-zA-Z0-9]$"
        labels: [string]: string
    }

    spec: {
        // account is the target AWS account alias (optional).
        account?: string

        // loadBalancerArn is the ARN of the parent ALB or NLB.
        // Immutable after creation.
        loadBalancerArn: string

        // port is the listener port (1-65535).
        port: int & >=1 & <=65535

        // protocol defines the protocol for connections.
        // ALB: HTTP, HTTPS
        // NLB: TCP, UDP, TLS, TCP_UDP
        protocol: "HTTP" | "HTTPS" | "TCP" | "UDP" | "TLS" | "TCP_UDP"

        // sslPolicy is the SSL negotiation policy. Required for HTTPS/TLS.
        sslPolicy?: string

        // certificateArn is the default SSL certificate ARN. Required for HTTPS/TLS.
        certificateArn?: string

        // alpnPolicy is the ALPN policy for TLS listeners on NLBs.
        alpnPolicy?: string

        // defaultActions define what happens to traffic that doesn't match
        // any listener rules.
        defaultActions: [...#ListenerAction] & [_, ...]

        // tags applied to the listener.
        tags: [string]: string
    }

    outputs?: {
        listenerArn: string
        port:        int
        protocol:    string
    }
}

#ListenerAction: {
    // type is the action type.
    type: "forward" | "redirect" | "fixed-response"

    // targetGroupArn is required for "forward" actions.
    targetGroupArn?: string

    // redirectConfig is required for "redirect" actions.
    redirectConfig?: {
        protocol:   string | *"#{protocol}"
        host:       string | *"#{host}"
        port:       string | *"#{port}"
        path:       string | *"/#{path}"
        query:      string | *"#{query}"
        statusCode: "HTTP_301" | "HTTP_302"
    }

    // fixedResponseConfig is required for "fixed-response" actions.
    fixedResponseConfig?: {
        statusCode:  string
        contentType: string | *"text/plain"
        messageBody: string | *""
    }
}
```

### Key Design Decisions

- **`defaultActions` minimum 1**: Every listener requires at least one default action.
  The CUE constraint `[_, ...]` enforces this.

- **`sslPolicy` and `certificateArn` optional**: Only required for HTTPS/TLS
  listeners. The driver validates at runtime that these are present when the protocol
  requires them, surfacing a terminal error if missing.

- **Action types limited to forward/redirect/fixed-response**: The
  `authenticate-oidc` and `authenticate-cognito` action types are complex and
  deferred to a future enhancement. They require additional configuration fields
  and external service integration.

- **Redirect config defaults**: Redirect config uses AWS's `#{variable}` syntax for
  dynamic substitution. The defaults (`#{protocol}`, `#{host}`, etc.) preserve the
  original request values, which is the most common redirect pattern.

---

## Step 2 — Driver Types

**File**: `internal/drivers/listener/types.go`

```go
package listener

import "github.com/praxiscloud/praxis/pkg/types"

const ServiceName = "Listener"

type ListenerSpec struct {
    Account         string            `json:"account,omitempty"`
    LoadBalancerArn string            `json:"loadBalancerArn"`
    Port            int               `json:"port"`
    Protocol        string            `json:"protocol"`
    SslPolicy       string            `json:"sslPolicy,omitempty"`
    CertificateArn  string            `json:"certificateArn,omitempty"`
    AlpnPolicy      string            `json:"alpnPolicy,omitempty"`
    DefaultActions  []ListenerAction  `json:"defaultActions"`
    Tags            map[string]string `json:"tags,omitempty"`
}

type ListenerAction struct {
    Type                string               `json:"type"`
    TargetGroupArn      string               `json:"targetGroupArn,omitempty"`
    RedirectConfig      *RedirectConfig      `json:"redirectConfig,omitempty"`
    FixedResponseConfig *FixedResponseConfig `json:"fixedResponseConfig,omitempty"`
}

type RedirectConfig struct {
    Protocol   string `json:"protocol"`
    Host       string `json:"host"`
    Port       string `json:"port"`
    Path       string `json:"path"`
    Query      string `json:"query"`
    StatusCode string `json:"statusCode"`
}

type FixedResponseConfig struct {
    StatusCode  string `json:"statusCode"`
    ContentType string `json:"contentType"`
    MessageBody string `json:"messageBody"`
}

type ListenerOutputs struct {
    ListenerArn string `json:"listenerArn"`
    Port        int    `json:"port"`
    Protocol    string `json:"protocol"`
}

type ObservedState struct {
    ListenerArn     string            `json:"listenerArn"`
    LoadBalancerArn string            `json:"loadBalancerArn"`
    Port            int               `json:"port"`
    Protocol        string            `json:"protocol"`
    SslPolicy       string            `json:"sslPolicy,omitempty"`
    CertificateArn  string            `json:"certificateArn,omitempty"`
    AlpnPolicy      string            `json:"alpnPolicy,omitempty"`
    DefaultActions  []ListenerAction  `json:"defaultActions"`
    Tags            map[string]string `json:"tags"`
}

type ListenerState struct {
    Desired            ListenerSpec         `json:"desired"`
    Observed           ObservedState        `json:"observed"`
    Outputs            ListenerOutputs      `json:"outputs"`
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

**File**: `internal/drivers/listener/aws.go`

### ListenerAPI Interface

```go
type ListenerAPI interface {
    // CreateListener creates a new listener on a load balancer.
    CreateListener(ctx context.Context, spec ListenerSpec) (arn string, err error)

    // DescribeListener returns the observed state of a listener by ARN.
    DescribeListener(ctx context.Context, arn string) (ObservedState, error)

    // FindListenerByPort finds a listener on a load balancer by port number.
    FindListenerByPort(ctx context.Context, lbArn string, port int) (ObservedState, error)

    // DeleteListener deletes a listener by ARN.
    DeleteListener(ctx context.Context, arn string) error

    // ModifyListener updates the listener's port, protocol, SSL config, and
    // default actions.
    ModifyListener(ctx context.Context, arn string, spec ListenerSpec) error

    // UpdateTags replaces all user tags on the listener.
    UpdateTags(ctx context.Context, arn string, desired map[string]string) error
}
```

### realListenerAPI Implementation

```go
type realListenerAPI struct {
    client  *elasticloadbalancingv2.Client
    limiter *ratelimit.Limiter
}

func NewListenerAPI(client *elasticloadbalancingv2.Client) ListenerAPI {
    return &realListenerAPI{
        client:  client,
        limiter: ratelimit.New("listener", 15, 8),
    }
}
```

### Key Implementation Details

#### `CreateListener`

```go
func (r *realListenerAPI) CreateListener(ctx context.Context, spec ListenerSpec) (string, error) {
    input := &elbv2.CreateListenerInput{
        LoadBalancerArn: aws.String(spec.LoadBalancerArn),
        Port:            aws.Int32(int32(spec.Port)),
        Protocol:        elbv2types.ProtocolEnum(spec.Protocol),
        DefaultActions:  toAWSActions(spec.DefaultActions),
    }

    if spec.SslPolicy != "" {
        input.SslPolicy = aws.String(spec.SslPolicy)
    }
    if spec.CertificateArn != "" {
        input.Certificates = []elbv2types.Certificate{
            {CertificateArn: aws.String(spec.CertificateArn)},
        }
    }
    if spec.AlpnPolicy != "" {
        input.AlpnPolicy = []string{spec.AlpnPolicy}
    }
    if len(spec.Tags) > 0 {
        input.Tags = toELBTags(spec.Tags)
    }

    out, err := r.client.CreateListener(ctx, input)
    if err != nil {
        return "", err
    }
    return aws.ToString(out.Listeners[0].ListenerArn), nil
}
```

#### `ModifyListener`

`ModifyListener` is an atomic operation that updates port, protocol, SSL config,
and default actions in a single API call. The driver always sends the full desired
state rather than computing a delta, since `ModifyListener` is idempotent.

#### Describe Flow

Composite describe:
1. `DescribeListeners` — base listener attributes (port, protocol, actions, SSL)
2. `DescribeTags` — resource tags

Certificate details are embedded in the listener description response, so no
additional API call is needed.

### Error Classification

| Function | AWS Error Code(s) | Semantics |
|---|---|---|
| `IsNotFound` | `ListenerNotFound` | Listener doesn't exist |
| `IsDuplicate` | `DuplicateListener` | Port already in use on this LB |
| `IsTooMany` | `TooManyListeners` | Listener quota per LB exceeded |
| `IsTargetGroupNotFound` | `TargetGroupNotFound` | Referenced TG doesn't exist |
| `IsInvalidConfig` | `InvalidConfigurationRequest` | Bad protocol/SSL/action config |
| `IsCertificateNotFound` | `CertificateNotFound` | Referenced ACM cert doesn't exist |

---

## Step 4 — Drift Detection

**File**: `internal/drivers/listener/drift.go`

### Drift Comparison Fields

| Field | Comparison Strategy |
|---|---|
| `port` | Integer equality |
| `protocol` | String equality (case-insensitive) |
| `sslPolicy` | String equality (if HTTPS/TLS) |
| `certificateArn` | String equality (if HTTPS/TLS) |
| `alpnPolicy` | String equality (if TLS) |
| `defaultActions` | Deep comparison of action type + config |
| `tags` | Map equality |

Immutable fields (`loadBalancerArn`) are not compared for drift.

### Action Comparison

Default actions are compared structurally:
- Same action count
- Same action types in order
- For `forward`: target group ARN equality
- For `redirect`: all redirect config fields
- For `fixed-response`: status code, content type, message body

---

## Step 5 — Driver Implementation

**File**: `internal/drivers/listener/driver.go`

### ListenerDriver Struct

```go
type ListenerDriver struct {
    accounts *auth.Registry
}

func NewListenerDriver(accounts *auth.Registry) *ListenerDriver {
    return &ListenerDriver{accounts: accounts}
}

func (d *ListenerDriver) ServiceName() string { return ServiceName }
```

### Provision Flow

1. Load existing state
2. If listener exists → check for spec changes → converge via `ModifyListener`
3. If listener doesn't exist:
   a. Validate SSL config if protocol is HTTPS/TLS
   b. `CreateListener` (wrapped in `restate.Run`)
4. Save state, schedule reconciliation, return outputs

Listeners become usable immediately after creation (no provisioning delay).

### HTTPS/TLS Validation

Before creating or modifying an HTTPS/TLS listener, the driver validates:
- `certificateArn` is non-empty
- `sslPolicy` is non-empty (or defaults to AWS recommended policy)

These are terminal errors if missing, not retryable.

### Convergence

All mutable fields are updated in a single `ModifyListener` call. Tags are updated
separately via `RemoveTags` + `AddTags`.

### Delete Flow

1. Call `DeleteListener`
2. Clear all state

AWS automatically deletes all listener rules when a listener is deleted.

---

## Step 6 — Provider Adapter

**File**: `internal/core/provider/listener_adapter.go`

```go
type ListenerAdapter struct {
    accounts *auth.Registry
}

func NewListenerAdapterWithRegistry(accounts *auth.Registry) *ListenerAdapter {
    return &ListenerAdapter{accounts: accounts}
}

func (a *ListenerAdapter) Kind() string             { return "Listener" }
func (a *ListenerAdapter) ServiceName() string      { return "Listener" }
func (a *ListenerAdapter) KeyScope() types.KeyScope { return types.KeyScopeRegion }
```

### Plan Method

The Plan method checks:
- `loadBalancerArn` changed → `PlanActionRecreate`
- Other changes → `PlanActionUpdate`

---

## Step 7 — Registry Integration

Add `NewListenerAdapterWithRegistry` to `internal/core/provider/registry.go`.

---

## Step 8 — Unit Tests

**File**: `internal/drivers/listener/driver_test.go`

| Test | Description |
|---|---|
| `TestServiceName` | Verify `ServiceName()` returns `"Listener"` |
| `TestSpecFromObserved` | Verify building a spec from observed state |
| `TestSSLValidation` | Verify HTTPS without cert → terminal error |

**File**: `internal/drivers/listener/drift_test.go`

| Test | Description |
|---|---|
| `TestNoDrift` | Identical desired and observed → no drift |
| `TestPortDrift` | Changed port → drift detected |
| `TestProtocolDrift` | Changed protocol → drift detected |
| `TestSSLPolicyDrift` | Changed SSL policy → drift detected |
| `TestDefaultActionDrift` | Changed default action → drift detected |
| `TestTagDrift` | Changed tags → drift detected |

---

## Step 9 — Integration Tests

**File**: `tests/integration/listener_driver_test.go`

### Prerequisites

- LocalStack with ELBv2 support
- Pre-existing ALB + Target Group

### Test Scenarios

| Test | Description |
|---|---|
| `TestListenerProvision` | Create HTTP listener, verify outputs |
| `TestListenerProvisionHTTPS` | Create HTTPS listener with certificate |
| `TestListenerProvisionIdempotent` | Provision twice → no-op on second call |
| `TestListenerImport` | Import existing listener |
| `TestListenerUpdate` | Change default action → verify convergence |
| `TestListenerDelete` | Delete listener, verify Deleted status |
| `TestListenerReconcile` | External modification → reconcile corrects drift |
| `TestListenerDuplicatePort` | Provision with existing port → terminal 409 |

---

## Listener-Specific Design Decisions

### 1. Logical Name vs ARN

AWS listeners don't have user-defined names. The Praxis `metadata.name` provides a
stable logical name for the Virtual Object key. The driver stores a
`praxis:listener-name` tag on the AWS listener to facilitate import-to-template
reconciliation.

### 2. Protocol Changes

Changing a listener's protocol (e.g., HTTP → HTTPS) is a modify operation in AWS,
but it requires adding a certificate and SSL policy. The driver handles this as a
single `ModifyListener` call with the full new configuration. If the certificate or
SSL policy is missing, the driver returns a terminal error.

### 3. Default Action as Full Replacement

When updating default actions, the driver sends the complete desired action list
rather than computing a delta. `ModifyListener` replaces the entire default action
configuration atomically. This is simpler and avoids ordering issues with multi-action
configurations.

### 4. SSL Policy Recommendation

The driver does not enforce a specific SSL policy. Users are responsible for choosing
an appropriate policy (e.g., `ELBSecurityPolicy-TLS13-1-2-2021-06` for modern TLS).
A future enhancement could add a CUE constraint or warning for deprecated policies.

### 5. Additional Certificates

AWS supports multiple certificates per listener via SNI (Server Name Indication). The
initial driver implementation supports only the primary certificate via
`certificateArn`. Additional certificates can be managed via
`AddListenerCertificates` / `RemoveListenerCertificates` in a future enhancement.

---

## Checklist

- [ ] `schemas/aws/elb/listener.cue` created
- [ ] `internal/drivers/listener/types.go` created
- [ ] `internal/drivers/listener/aws.go` created
- [ ] `internal/drivers/listener/drift.go` created
- [ ] `internal/drivers/listener/driver.go` created
- [ ] `internal/drivers/listener/driver_test.go` created
- [ ] `internal/drivers/listener/aws_test.go` created
- [ ] `internal/drivers/listener/drift_test.go` created
- [ ] `internal/core/provider/listener_adapter.go` created
- [ ] `internal/core/provider/registry.go` updated
- [ ] `tests/integration/listener_driver_test.go` created
