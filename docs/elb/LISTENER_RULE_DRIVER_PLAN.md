# Listener Rule Driver — Implementation Spec

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
13. [Listener-Rule-Specific Design Decisions](#listener-rule-specific-design-decisions)
14. [Checklist](#checklist)

---

## 1. Overview & Scope

The Listener Rule driver manages the lifecycle of **ELBv2 Listener Rules** only.
It creates, imports, updates, and deletes non-default rules on ALB listeners. Each
rule consists of a priority, a set of conditions (path pattern, host header, HTTP
headers, query strings, source IP, HTTP method), and a set of actions (forward to
target group, redirect, or return fixed response).

Listener rules provide content-based routing on Application Load Balancers —
directing traffic to different target groups based on request attributes. They are
the most complex resource in the ELB family due to the combination of condition
types, action chains, and priority ordering constraints.

**Out of scope**: Load balancers (ALB/NLB drivers), Listeners (separate driver),
Target Groups (separate driver). The default listener action is managed by the
Listener driver, not as a listener rule.

**NLB note**: NLB listeners do not support listener rules — all traffic is forwarded
to the default action's target group. This driver only applies to ALB listeners.

### Driver Contract

| Handler | Context | Purpose |
|---|---|---|
| `Provision` | `ObjectContext` (exclusive) | Create or converge a listener rule |
| `Import` | `ObjectContext` (exclusive) | Adopt an existing listener rule |
| `Delete` | `ObjectContext` (exclusive) | Remove a listener rule |
| `Reconcile` | `ObjectContext` (exclusive) | Detect/correct drift (report-only for Observed mode) |
| `GetStatus` | `ObjectSharedContext` (shared) | Return current status |
| `GetOutputs` | `ObjectSharedContext` (shared) | Return listener rule outputs |

### Mutable vs Immutable Attributes

| Attribute | Mutability | Notes |
|---|---|---|
| `listenerArn` | Immutable | Rule is bound to a specific listener; requires delete + recreate |
| `priority` | Mutable | Updated via `SetRulePriorities`; must be unique per listener (1-50000) |
| `conditions` | Mutable | Updated via `ModifyRule` |
| `actions` | Mutable | Updated via `ModifyRule` |
| `tags` | Mutable | Full replace via `RemoveTags` + `AddTags` |

### Downstream Consumers

```text
${resources.my-rule.outputs.ruleArn}   → Informational references
${resources.my-rule.outputs.priority}  → Informational references
```

---

## 2. Key Strategy

### Key Scope: `KeyScopeRegion`

Listener rules are regional resources tied to a listener. AWS identifies rules by
ARN, not by name. Praxis uses a user-provided logical name as the key.

```text
region~ruleName
```

### BuildKey vs BuildImportKey

- **`BuildKey(resourceDoc)`**: Extracts `metadata.name`, prepends region. Returns
  `region~name`.

- **`BuildImportKey(region, resourceID)`**: Returns `region~resourceID`. The
  `resourceID` is the rule ARN or a user-provided name.

### No Ownership Tags

Listener rules are uniquely identified by ARN within their parent listener. The
driver stores a `praxis:rule-name` tag to associate the AWS rule with the Praxis
logical name.

---

## 3. File Inventory

```text
✦ schemas/aws/elb/listener_rule.cue                        — CUE schema for ListenerRule resource
✦ internal/drivers/listenerrule/types.go                    — Spec, Outputs, ObservedState, State structs
✦ internal/drivers/listenerrule/aws.go                      — ListenerRuleAPI interface + realListenerRuleAPI
✦ internal/drivers/listenerrule/drift.go                    — HasDrift(), ComputeFieldDiffs()
✦ internal/drivers/listenerrule/driver.go                   — ListenerRuleDriver Virtual Object
✦ internal/drivers/listenerrule/driver_test.go              — Unit tests for driver (mocked AWS)
✦ internal/drivers/listenerrule/aws_test.go                 — Unit tests for error classification
✦ internal/drivers/listenerrule/drift_test.go               — Unit tests for drift detection
✦ internal/core/provider/listenerrule_adapter.go            — ListenerRuleAdapter implementing provider.Adapter
✦ internal/core/provider/listenerrule_adapter_test.go       — Unit tests for adapter
✦ tests/integration/listenerrule_driver_test.go             — Integration tests
✎ internal/core/provider/registry.go                        — Add NewListenerRuleAdapter to NewRegistry()
```

---

## Step 1 — CUE Schema

**File**: `schemas/aws/elb/listener_rule.cue`

```cue
package elb

#ListenerRule: {
    apiVersion: "praxis.io/v1"
    kind:       "ListenerRule"

    metadata: {
        name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9_-]{0,62}[a-zA-Z0-9]$"
        labels: [string]: string
    }

    spec: {
        // account is the target AWS account alias (optional).
        account?: string

        // listenerArn is the ARN of the parent listener.
        // Immutable after creation.
        listenerArn: string

        // priority determines the evaluation order (1-50000). Lower numbers
        // are evaluated first. Must be unique within the listener.
        priority: int & >=1 & <=50000

        // conditions define when this rule matches. At least one condition
        // is required. Up to 5 conditions per rule.
        conditions: [...#RuleCondition] & [_, ...] & list.MaxItems(5)

        // actions define what happens when the rule matches. At least one
        // action is required.
        actions: [...#RuleAction] & [_, ...]

        // tags applied to the listener rule.
        tags: [string]: string
    }

    outputs?: {
        ruleArn:  string
        priority: int
    }
}

#RuleCondition: {
    // field is the condition type.
    field: "path-pattern" | "host-header" | "http-header" |
           "query-string" | "source-ip" | "http-request-method"

    // values is the list of match values.
    // For path-pattern: URL path patterns (e.g., "/api/*")
    // For host-header: hostnames (e.g., "api.example.com")
    // For source-ip: CIDR blocks (e.g., "10.0.0.0/8")
    // For http-request-method: HTTP methods (e.g., "GET", "POST")
    values?: [...string]

    // httpHeaderConfig is required for http-header conditions.
    httpHeaderConfig?: {
        httpHeaderName: string
        values: [...string]
    }

    // queryStringConfig is required for query-string conditions.
    queryStringConfig?: {
        values: [...{
            key?:  string
            value: string
        }]
    }
}

#RuleAction: {
    // type is the action type.
    type: "forward" | "redirect" | "fixed-response"

    // order determines the action execution order for multi-action rules.
    order?: int & >=1

    // targetGroupArn is required for "forward" actions.
    targetGroupArn?: string

    // forwardConfig supports weighted target groups.
    forwardConfig?: {
        targetGroups: [...{
            targetGroupArn: string
            weight?:        int & >=0 & <=999 | *1
        }]
        stickiness?: {
            enabled:  bool | *false
            duration: int & >=1 & <=604800
        }
    }

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

- **Up to 5 conditions per rule**: AWS enforces a maximum of 5 conditions per
  listener rule. The CUE schema uses `list.MaxItems(5)` to enforce this at
  validation time.

- **Condition types as discriminated union**: The `field` string determines which
  config sub-field is relevant. `httpHeaderConfig` is only valid when `field` is
  `"http-header"`. `queryStringConfig` is only valid for `"query-string"`. Other
  condition types use `values` directly.

- **`forwardConfig` vs `targetGroupArn`**: Simple forwarding uses `targetGroupArn`.
  Weighted forwarding (traffic splitting) uses `forwardConfig` with multiple target
  groups and optional weights. Only one should be specified per forward action.

- **Priority uniqueness**: Priorities must be unique per listener. The driver does
  NOT manage global priority allocation — users are responsible for choosing
  non-conflicting priorities. The driver returns a terminal error if a priority
  conflict is detected.

---

## Step 2 — Driver Types

**File**: `internal/drivers/listenerrule/types.go`

```go
package listenerrule

import "github.com/shirvan/praxis/pkg/types"

const ServiceName = "ListenerRule"

type ListenerRuleSpec struct {
    Account     string            `json:"account,omitempty"`
    ListenerArn string            `json:"listenerArn"`
    Priority    int               `json:"priority"`
    Conditions  []RuleCondition   `json:"conditions"`
    Actions     []RuleAction      `json:"actions"`
    Tags        map[string]string `json:"tags,omitempty"`
}

type RuleCondition struct {
    Field             string             `json:"field"`
    Values            []string           `json:"values,omitempty"`
    HttpHeaderConfig  *HttpHeaderConfig  `json:"httpHeaderConfig,omitempty"`
    QueryStringConfig *QueryStringConfig `json:"queryStringConfig,omitempty"`
}

type HttpHeaderConfig struct {
    HttpHeaderName string   `json:"httpHeaderName"`
    Values         []string `json:"values"`
}

type QueryStringConfig struct {
    Values []QueryStringKV `json:"values"`
}

type QueryStringKV struct {
    Key   string `json:"key,omitempty"`
    Value string `json:"value"`
}

type RuleAction struct {
    Type                string               `json:"type"`
    Order               int                  `json:"order,omitempty"`
    TargetGroupArn      string               `json:"targetGroupArn,omitempty"`
    ForwardConfig       *ForwardConfig       `json:"forwardConfig,omitempty"`
    RedirectConfig      *RedirectConfig      `json:"redirectConfig,omitempty"`
    FixedResponseConfig *FixedResponseConfig `json:"fixedResponseConfig,omitempty"`
}

type ForwardConfig struct {
    TargetGroups []WeightedTargetGroup `json:"targetGroups"`
    Stickiness   *ForwardStickiness    `json:"stickiness,omitempty"`
}

type WeightedTargetGroup struct {
    TargetGroupArn string `json:"targetGroupArn"`
    Weight         int    `json:"weight,omitempty"`
}

type ForwardStickiness struct {
    Enabled  bool `json:"enabled"`
    Duration int  `json:"duration"`
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

type ListenerRuleOutputs struct {
    RuleArn  string `json:"ruleArn"`
    Priority int    `json:"priority"`
}

type ObservedState struct {
    RuleArn     string            `json:"ruleArn"`
    ListenerArn string            `json:"listenerArn"`
    Priority    int               `json:"priority"`
    Conditions  []RuleCondition   `json:"conditions"`
    Actions     []RuleAction      `json:"actions"`
    IsDefault   bool              `json:"isDefault"`
    Tags        map[string]string `json:"tags"`
}

type ListenerRuleState struct {
    Desired            ListenerRuleSpec     `json:"desired"`
    Observed           ObservedState        `json:"observed"`
    Outputs            ListenerRuleOutputs  `json:"outputs"`
    Status             types.ResourceStatus `json:"status"`
    Mode               types.Mode           `json:"mode"`
    Error              string               `json:"error,omitempty"`
    Generation         int64                `json:"generation"`
    LastReconcile      string               `json:"lastReconcile,omitempty"`
    ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
```

### Key Type Notes

- **`IsDefault` in ObservedState**: AWS has a default rule per listener (priority
  "default") that cannot be deleted. The driver records this flag but never
  creates or deletes default rules — those are managed by the Listener driver's
  `defaultActions` field.

- **`ForwardConfig` with weighted target groups**: Supports traffic splitting
  between multiple target groups. Each target group gets a weight (0-999). This
  enables blue-green and canary deployment patterns.

---

## Step 3 — AWS API Abstraction Layer

**File**: `internal/drivers/listenerrule/aws.go`

### ListenerRuleAPI Interface

```go
type ListenerRuleAPI interface {
    // CreateRule creates a new listener rule.
    CreateRule(ctx context.Context, spec ListenerRuleSpec) (arn string, err error)

    // DescribeRule returns the observed state of a rule by ARN.
    DescribeRule(ctx context.Context, arn string) (ObservedState, error)

    // FindRuleByPriority finds a rule on a listener by priority number.
    FindRuleByPriority(ctx context.Context, listenerArn string, priority int) (ObservedState, error)

    // ListRules returns all rules for a listener.
    ListRules(ctx context.Context, listenerArn string) ([]ObservedState, error)

    // DeleteRule deletes a rule by ARN.
    DeleteRule(ctx context.Context, arn string) error

    // ModifyRule updates the rule's conditions and actions.
    ModifyRule(ctx context.Context, arn string, conditions []RuleCondition, actions []RuleAction) error

    // SetRulePriorities updates the priority of one or more rules.
    SetRulePriorities(ctx context.Context, priorities map[string]int) error

    // UpdateTags replaces all user tags on the rule.
    UpdateTags(ctx context.Context, arn string, desired map[string]string) error
}
```

### realListenerRuleAPI Implementation

```go
type realListenerRuleAPI struct {
    client  *elasticloadbalancingv2.Client
    limiter *ratelimit.Limiter
}

func NewListenerRuleAPI(client *elasticloadbalancingv2.Client) ListenerRuleAPI {
    return &realListenerRuleAPI{
        client:  client,
        limiter: ratelimit.New("listener-rule", 15, 8),
    }
}
```

### Key Implementation Details

#### `CreateRule`

```go
func (r *realListenerRuleAPI) CreateRule(ctx context.Context, spec ListenerRuleSpec) (string, error) {
    input := &elbv2.CreateRuleInput{
        ListenerArn: aws.String(spec.ListenerArn),
        Priority:    aws.Int32(int32(spec.Priority)),
        Conditions:  toAWSConditions(spec.Conditions),
        Actions:     toAWSActions(spec.Actions),
    }
    if len(spec.Tags) > 0 {
        input.Tags = toELBTags(spec.Tags)
    }

    out, err := r.client.CreateRule(ctx, input)
    if err != nil {
        return "", err
    }
    return aws.ToString(out.Rules[0].RuleArn), nil
}
```

#### Condition Serialization (`toAWSConditions`)

Each condition type maps to a different AWS field:

| Condition Field | AWS Field |
|---|---|
| `path-pattern` | `PathPatternConfig.Values` |
| `host-header` | `HostHeaderConfig.Values` |
| `http-header` | `HttpHeaderConfig.HttpHeaderName` + `HttpHeaderConfig.Values` |
| `query-string` | `QueryStringConfig.Values[].Key` + `Value` |
| `source-ip` | `SourceIpConfig.Values` |
| `http-request-method` | `HttpRequestMethodConfig.Values` |

#### `SetRulePriorities`

Priority changes are managed via a dedicated `SetRulePriorities` API call, not via
`ModifyRule`. This API accepts a batch of `(ruleArn, priority)` pairs, allowing
atomic reordering of multiple rules. The driver uses this when the priority field
drifts or changes in the spec.

#### Describe Flow

1. `DescribeRules` — base rule attributes (conditions, actions, priority)
2. `DescribeTags` — resource tags

### Error Classification

| Function | AWS Error Code(s) | Semantics |
|---|---|---|
| `IsNotFound` | `RuleNotFound` | Rule doesn't exist |
| `IsPriorityInUse` | `PriorityInUse` | Priority already taken on this listener |
| `IsTooMany` | `TooManyRules` | Rule quota per listener exceeded (default 100) |
| `IsTooManyConditions` | `TooManyConditionValues` | Too many values in conditions |
| `IsTargetGroupNotFound` | `TargetGroupNotFound` | Referenced TG doesn't exist |
| `IsInvalidConfig` | `InvalidConfigurationRequest` | Bad condition/action config |

---

## Step 4 — Drift Detection

**File**: `internal/drivers/listenerrule/drift.go`

### Drift Comparison Fields

| Field | Comparison Strategy |
|---|---|
| `priority` | Integer equality |
| `conditions` | Deep structural comparison (normalized) |
| `actions` | Deep structural comparison (normalized) |
| `tags` | Map equality |

Immutable fields (`listenerArn`) are not compared for drift.

### Condition Normalization

Conditions are normalized for comparison:

- Sorted by `field` name
- Within each condition, `values` are sorted alphabetically
- `httpHeaderConfig.values` are sorted
- `queryStringConfig.values` are sorted by `(key, value)`

### Action Normalization

Actions are normalized by:

- Sorting by `order` field (or by position if order is not specified)
- Normalizing `forwardConfig.targetGroups` by sorting on `targetGroupArn`
- Default weights (0 or 1) are treated as equivalent

---

## Step 5 — Driver Implementation

**File**: `internal/drivers/listenerrule/driver.go`

### ListenerRuleDriver Struct

```go
type ListenerRuleDriver struct {
    auth       authservice.AuthClient
}

func NewListenerRuleDriver(auth       authservice.AuthClient) *ListenerRuleDriver {
    return &ListenerRuleDriver{accounts: accounts}
}

func (d *ListenerRuleDriver) ServiceName() string { return ServiceName }
```

### Provision Flow

1. Load existing state
2. If rule exists → check for spec changes → converge
3. If rule doesn't exist:
   a. `CreateRule` (wrapped in `restate.Run`)
4. Save state, schedule reconciliation, return outputs

Rules become usable immediately after creation (no provisioning delay).

### Convergence

When the spec changes on an existing rule:

1. **Priority** → `SetRulePriorities` (separate API call)
2. **Conditions + Actions** → `ModifyRule` (single call replaces both)
3. **Tags** → `RemoveTags` + `AddTags`

Priority changes and condition/action changes are independent API calls. Priority
is changed first to avoid transient conflicts (e.g., swapping priorities between
two rules).

### Delete Flow

1. Call `DeleteRule`
2. Clear all state

Deleting a non-default rule has no cascading effects. The driver validates that the
rule is not the default rule (default rules cannot be deleted — they're managed by
the Listener driver).

### Default Rule Protection

The driver refuses to delete a rule where `isDefault: true`. This is a terminal
error. The default rule is a structural part of the listener and is managed by the
Listener driver's `defaultActions` field.

---

## Step 6 — Provider Adapter

**File**: `internal/core/provider/listenerrule_adapter.go`

```go
type ListenerRuleAdapter struct {
    auth       authservice.AuthClient
}

func NewListenerRuleAdapterWithAuth(auth       authservice.AuthClient) *ListenerRuleAdapter {
    return &ListenerRuleAdapter{accounts: accounts}
}

func (a *ListenerRuleAdapter) Kind() string             { return "ListenerRule" }
func (a *ListenerRuleAdapter) ServiceName() string      { return "ListenerRule" }
func (a *ListenerRuleAdapter) Scope() KeyScope          { return KeyScopeRegion }
```

### Plan Method

The Plan method checks:

- `listenerArn` changed → `PlanActionRecreate`
- Other changes → `PlanActionUpdate`

---

## Step 7 — Registry Integration

Add `NewListenerRuleAdapterWithAuth` to `internal/core/provider/registry.go`.

---

## Step 8 — Unit Tests

**File**: `internal/drivers/listenerrule/driver_test.go`

| Test | Description |
|---|---|
| `TestServiceName` | Verify `ServiceName()` returns `"ListenerRule"` |
| `TestSpecFromObserved` | Verify building a spec from observed state |
| `TestDefaultRuleProtection` | Verify delete of default rule → terminal error |
| `TestPriorityConflict` | Verify `PriorityInUse` → terminal error |

**File**: `internal/drivers/listenerrule/drift_test.go`

| Test | Description |
|---|---|
| `TestNoDrift` | Identical desired and observed → no drift |
| `TestPriorityDrift` | Changed priority → drift detected |
| `TestConditionDrift` | Changed conditions → drift detected |
| `TestActionDrift` | Changed actions → drift detected |
| `TestConditionOrderIndependent` | Same conditions in different order → no drift |
| `TestWeightedForwardDrift` | Changed target group weights → drift detected |
| `TestTagDrift` | Changed tags → drift detected |

---

## Step 9 — Integration Tests

**File**: `tests/integration/listenerrule_driver_test.go`

### Prerequisites

- LocalStack with ELBv2 support
- Pre-existing ALB + Listener + Target Group

### Test Scenarios

| Test | Description |
|---|---|
| `TestListenerRuleProvision` | Create rule with path-pattern, verify outputs |
| `TestListenerRuleProvisionHostHeader` | Create rule with host-header condition |
| `TestListenerRuleProvisionIdempotent` | Provision twice → no-op on second call |
| `TestListenerRuleImport` | Import existing rule |
| `TestListenerRuleUpdatePriority` | Change priority → verify updated |
| `TestListenerRuleUpdateConditions` | Change conditions → verify updated |
| `TestListenerRuleUpdateActions` | Change actions → verify updated |
| `TestListenerRuleDelete` | Delete rule, verify Deleted status |
| `TestListenerRuleReconcile` | External modification → reconcile corrects drift |
| `TestListenerRulePriorityConflict` | Provision with taken priority → terminal error |
| `TestListenerRuleWeightedForward` | Create rule with weighted target groups |

---

## Listener-Rule-Specific Design Decisions

### 1. Priority Management

The driver does NOT provide automatic priority allocation. Users must specify an
explicit numeric priority (1-50000) in their template. This is a deliberate design
choice:

- Explicit priorities are deterministic and reproducible
- Automatic allocation would require global coordination across all rules on a
  listener, which conflicts with the driver's single-resource scope
- Priority conflicts surface as clear terminal errors

If a priority conflict occurs, the user must resolve it by changing the priority
in their template.

### 2. Condition Complexity

Listener rule conditions are the most complex type in the ELB driver family. The
six condition types have different schemas:

| Condition | Config Location | Example |
|---|---|---|
| `path-pattern` | `values: ["/api/*"]` | URL path matching |
| `host-header` | `values: ["api.example.com"]` | Hostname matching |
| `http-header` | `httpHeaderConfig: {name, values}` | Custom header matching |
| `query-string` | `queryStringConfig: {values: [{key, value}]}` | Query param matching |
| `source-ip` | `values: ["10.0.0.0/8"]` | Source CIDR matching |
| `http-request-method` | `values: ["GET", "POST"]` | HTTP method matching |

The driver serializes/deserializes these using the discriminated `field` value to
determine which config struct to populate.

### 3. Multi-Action Rules

AWS supports multiple actions per rule with explicit ordering. Common patterns:

- `authenticate-cognito` (order 1) → `forward` (order 2)
- `authenticate-oidc` (order 1) → `forward` (order 2)

The initial implementation supports `forward`, `redirect`, and `fixed-response`
actions. Authentication actions (`authenticate-cognito`, `authenticate-oidc`) are
deferred to a future enhancement.

### 4. Weighted Target Groups (Traffic Splitting)

The `forwardConfig` with multiple weighted target groups enables:

- **Blue-green deployments**: 100% to blue, then 100% to green
- **Canary deployments**: 90% to stable, 10% to canary
- **A/B testing**: Split traffic by weight

Weights are integers (0-999). The total doesn't need to sum to a specific value —
AWS normalizes them proportionally. A weight of 0 means no traffic is sent to that
target group (useful for draining).

### 5. Rule Quota Management

ALB listeners support up to 100 rules (excluding the default rule). The driver
does not track global rule counts — it relies on the `TooManyRules` error from AWS
to surface quota violations as terminal errors.

### 6. Default vs Non-Default Rules

The default rule (the one with no conditions, just a default action) is managed by
the Listener driver. All rules managed by this Listener Rule driver are non-default
rules with explicit conditions and priorities. The driver validates this on import
by checking the `isDefault` flag and refusing to manage default rules.

---

## Checklist

- [x] `schemas/aws/elb/listener_rule.cue` created
- [x] `internal/drivers/listenerrule/types.go` created
- [x] `internal/drivers/listenerrule/aws.go` created
- [x] `internal/drivers/listenerrule/drift.go` created
- [x] `internal/drivers/listenerrule/driver.go` created
- [x] `internal/drivers/listenerrule/driver_test.go` created
- [x] `internal/drivers/listenerrule/aws_test.go` created
- [x] `internal/drivers/listenerrule/drift_test.go` created
- [x] `internal/core/provider/listenerrule_adapter.go` created
- [x] `internal/core/provider/registry.go` updated
- [x] `tests/integration/listenerrule_driver_test.go` created
