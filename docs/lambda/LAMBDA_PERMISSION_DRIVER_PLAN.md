# Lambda Permission Driver — Implementation Spec

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
16. [Lambda-Permission-Specific Design Decisions](#lambda-permission-specific-design-decisions)
17. [Checklist](#checklist)

---

## 1. Overview & Scope

The Lambda Permission driver manages individual **resource-based policy statements**
on Lambda functions. Each permission is a statement in the function's resource policy
that grants a specific AWS service or account permission to invoke (or otherwise
interact with) the function.

### Permission Model

Lambda functions have a resource-based policy (separate from the execution role).
Each statement in this policy is:

- **Identified by a `StatementId`** — a user-chosen unique identifier within the
  function's policy.
- **Immutable after creation** — once added, a statement cannot be modified. To
  change the principal, action, or condition, the old statement must be removed
  and a new one added.
- **Atomic** — each `AddPermission` call adds one statement; each `RemovePermission`
  call removes one.

### Driver Contract

| Handler | Context | Purpose |
|---|---|---|
| `Provision` | `ObjectContext` (exclusive) | Add permission (or replace if changed) |
| `Import` | `ObjectContext` (exclusive) | Adopt an existing permission statement |
| `Delete` | `ObjectContext` (exclusive) | Remove the permission statement |
| `Reconcile` | `ObjectContext` (exclusive) | Detect/correct drift |
| `GetStatus` | `ObjectSharedContext` (shared) | Return current status |
| `GetOutputs` | `ObjectSharedContext` (shared) | Return permission outputs |

### Mutable vs Immutable Attributes

| Attribute | Mutability | Notes |
|---|---|---|
| Statement ID | Immutable | User-defined, unique per function policy |
| Function name/ARN | Immutable | Target function for the permission |
| Action | Immutable | e.g., `lambda:InvokeFunction` |
| Principal | Immutable | e.g., `apigateway.amazonaws.com`, `s3.amazonaws.com`, account ID |
| Source ARN | Immutable | Optional condition restricting the invoker |
| Source account | Immutable | Optional condition restricting the account |

**All fields are immutable.** Changing any field requires removing the old statement
and adding a new one. The driver handles this as a remove-then-add operation within
Provision.

### What Is NOT In Scope

- **Function policy management**: The driver manages individual statements, not the
  entire policy document. Aggregating all statements into a single policy view is
  a Core/CLI concern.
- **Qualifier-scoped permissions**: Permissions can be scoped to a specific function
  version or alias. This driver targets `$LATEST` by default; qualifier support is
  a future extension.
- **Function URL auth**: Function URL authorization type is a separate API. Not
  managed by this driver.

### Downstream Consumers

```text
${resources.my-perm.outputs.statementId}   → Informational / dependency tracking
${resources.my-perm.outputs.functionName}  → Cross-references
```

Permissions are typically leaf nodes in the DAG — other resources depend on the
function, and the permission depends on the function and the invoker. Permissions
themselves rarely produce outputs that other resources consume.

---

## 2. Key Strategy

### Key Format: `region~functionName~statementId`

A permission is scoped to a specific function and identified by its statement ID.
The composite key ensures uniqueness: one Virtual Object per permission statement
per function per region.

1. **BuildKey** (adapter, plan-time): returns `region~functionName~statementId`,
   extracted from `spec.region`, `spec.functionName`, and `spec.statementId`.
2. **Provision / Delete**: dispatched to the same VO key.
3. **Plan**: reads VO state via `GetOutputs`. If outputs exist, compares spec fields.
   Any field change → `OpUpdate` (which triggers remove + add).
4. **Import**: `BuildImportKey(region, resourceID)` where `resourceID` is
   `functionName~statementId`. Returns `region~functionName~statementId`.

### No Ownership Tags

Lambda permissions are policy statements, not taggable AWS resources. Statement IDs
are unique within a function's policy. AWS rejects duplicate `AddPermission` calls
with the same `StatementId`, providing natural conflict detection.

---

## 3. File Inventory

```text
✦ internal/drivers/lambdaperm/types.go                — Spec, Outputs, ObservedState, State
✦ internal/drivers/lambdaperm/aws.go                  — PermissionAPI interface + impl
✦ internal/drivers/lambdaperm/drift.go                — HasDrift(), ComputeFieldDiffs()
✦ internal/drivers/lambdaperm/driver.go               — LambdaPermissionDriver Virtual Object
✦ internal/drivers/lambdaperm/driver_test.go          — Unit tests for driver
✦ internal/drivers/lambdaperm/aws_test.go             — Unit tests for error classification
✦ internal/drivers/lambdaperm/drift_test.go           — Unit tests for drift detection
✦ internal/core/provider/lambdaperm_adapter.go        — Adapter
✦ internal/core/provider/lambdaperm_adapter_test.go   — Adapter tests
✦ schemas/aws/lambda/permission.cue                   — CUE schema
✦ tests/integration/lambda_permission_driver_test.go  — Integration tests
✎ cmd/praxis-compute/main.go                          — Bind LambdaPermission driver
✎ internal/core/provider/registry.go                  — Add adapter to NewRegistry()
```

---

## Step 1 — CUE Schema

**File**: `schemas/aws/lambda/permission.cue`

```cue
package lambda

#LambdaPermission: {
    apiVersion: "praxis.io/v1"
    kind:       "LambdaPermission"

    metadata: {
        // name is a logical name for this permission within the Praxis template.
        // Maps to statementId in the spec.
        name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9_-]{0,99}$"
        labels: [string]: string
    }

    spec: {
        // region is the AWS region of the target function.
        region: string

        // functionName is the name or ARN of the target Lambda function.
        functionName: string

        // statementId is the unique identifier for this permission statement.
        // Must be unique within the function's resource policy.
        statementId: string & =~"^[a-zA-Z0-9_-]{1,100}$"

        // action is the Lambda API action to grant (e.g., "lambda:InvokeFunction").
        action: string | *"lambda:InvokeFunction"

        // principal is the AWS service or account granted permission.
        // Examples: "apigateway.amazonaws.com", "s3.amazonaws.com", "events.amazonaws.com",
        //           "sns.amazonaws.com", "123456789012"
        principal: string

        // sourceArn restricts the permission to a specific resource ARN.
        // Optional — omit to allow any resource from the principal.
        sourceArn?: string

        // sourceAccount restricts the permission to a specific AWS account.
        // Optional — used with S3 notifications where sourceArn is a bucket ARN.
        sourceAccount?: string

        // eventSourceToken is used for Alexa Smart Home event sources.
        eventSourceToken?: string

        // qualifier scopes the permission to a specific function version or alias.
        // Optional — defaults to $LATEST (unqualified).
        qualifier?: string
    }

    outputs?: {
        statementId:  string
        functionName: string
        statement:    string  // JSON string of the policy statement
    }
}
```

### Schema Design Notes

- **`statementId` separate from `metadata.name`**: The statement ID is an AWS-level
  identifier within the function's policy. `metadata.name` is the Praxis template
  resource name. They may differ — e.g., `metadata.name: "allow-apigw"` with
  `statementId: "AllowAPIGatewayInvoke"`.
- **`action` defaults to `lambda:InvokeFunction`**: This is the most common permission
  action. Other valid actions include `lambda:InvokeFunctionUrl` and
  `lambda:GetFunction`.
- **`principal` is a string**: Can be a service principal (`s3.amazonaws.com`), an
  account ID (`123456789012`), or a wildcard (`*`). AWS validates at API call time.

---

## Step 2 — AWS Client Factory

**File**: `internal/infra/awsclient/client.go` — **NO CHANGES NEEDED**

Permission operations use the same Lambda API client. Reuses `NewLambdaClient()`.

---

## Step 3 — Driver Types

**File**: `internal/drivers/lambdaperm/types.go`

```go
package lambdaperm

import "github.com/shirvan/praxis/pkg/types"

const ServiceName = "LambdaPermission"

type LambdaPermissionSpec struct {
    Account          string `json:"account,omitempty"`
    Region           string `json:"region"`
    FunctionName     string `json:"functionName"`
    StatementId      string `json:"statementId"`
    Action           string `json:"action"`
    Principal        string `json:"principal"`
    SourceArn        string `json:"sourceArn,omitempty"`
    SourceAccount    string `json:"sourceAccount,omitempty"`
    EventSourceToken string `json:"eventSourceToken,omitempty"`
    Qualifier        string `json:"qualifier,omitempty"`
    ManagedKey       string `json:"managedKey,omitempty"`
}

type LambdaPermissionOutputs struct {
    StatementId  string `json:"statementId"`
    FunctionName string `json:"functionName"`
    Statement    string `json:"statement"` // JSON policy statement
}

type ObservedState struct {
    StatementId      string `json:"statementId"`
    FunctionName     string `json:"functionName"`
    Action           string `json:"action"`
    Principal        string `json:"principal"`
    SourceArn        string `json:"sourceArn,omitempty"`
    SourceAccount    string `json:"sourceAccount,omitempty"`
    EventSourceToken string `json:"eventSourceToken,omitempty"`
    Condition        string `json:"condition,omitempty"` // JSON string
}

type LambdaPermissionState struct {
    Desired            LambdaPermissionSpec    `json:"desired"`
    Observed           ObservedState           `json:"observed"`
    Outputs            LambdaPermissionOutputs `json:"outputs"`
    Status             types.ResourceStatus    `json:"status"`
    Mode               types.Mode              `json:"mode"`
    Error              string                  `json:"error,omitempty"`
    Generation         int64                   `json:"generation"`
    LastReconcile      string                  `json:"lastReconcile,omitempty"`
    ReconcileScheduled bool                    `json:"reconcileScheduled"`
}
```

### Types Design Notes

- **`ObservedState` is parsed from the policy JSON**: `GetPolicy` returns the entire
  function policy as a JSON string. The driver parses it to extract the specific
  statement matching the `StatementId`.
- **`Outputs.Statement` is the raw JSON**: Stored for diagnostic purposes. The full
  JSON statement is useful for debugging policy issues.

---

## Step 4 — AWS API Abstraction Layer

**File**: `internal/drivers/lambdaperm/aws.go`

### PermissionAPI Interface

```go
type PermissionAPI interface {
    // AddPermission adds a resource-based policy statement to a function.
    AddPermission(ctx context.Context, spec LambdaPermissionSpec) (string, error)

    // RemovePermission removes a policy statement by statement ID.
    RemovePermission(ctx context.Context, functionName, statementId string) error

    // GetPolicy returns the function's resource policy as a JSON string.
    GetPolicy(ctx context.Context, functionName string) (string, error)

    // GetPermission parses the policy and extracts a specific statement.
    GetPermission(ctx context.Context, functionName, statementId string) (ObservedState, error)
}
```

### Implementation: realPermissionAPI

```go
type realPermissionAPI struct {
    client  *lambda.Client
    limiter ratelimit.Limiter
}

func NewPermissionAPI(client *lambdasdk.Client) PermissionAPI {
    return &realPermissionAPI{client: client, limiter: ratelimit.New("lambda-permission", 20, 10)}
}
```

### Error Classification

```go
func isNotFound(err error) bool {
    var rnf *types.ResourceNotFoundException
    return errors.As(err, &rnf)
}

func isConflict(err error) bool {
    var rce *types.ResourceConflictException
    return errors.As(err, &rce)
}

func isPreconditionFailed(err error) bool {
    var pfe *types.PreconditionFailedException
    return errors.As(err, &pfe)
}

func isThrottled(err error) bool {
    var tmr *types.TooManyRequestsException
    return errors.As(err, &tmr)
}
```

### Key Implementation: GetPermission

```go
func (r *realPermissionAPI) GetPermission(ctx context.Context, functionName, statementId string) (ObservedState, error) {
    r.limiter.Wait(ctx)

    policyJson, err := r.GetPolicy(ctx, functionName)
    if err != nil {
        return ObservedState{}, err
    }

    // Parse policy JSON to extract the matching statement
    var policy struct {
        Statement []struct {
            Sid       string      `json:"Sid"`
            Effect    string      `json:"Effect"`
            Principal interface{} `json:"Principal"`
            Action    string      `json:"Action"`
            Resource  string      `json:"Resource"`
            Condition interface{} `json:"Condition,omitempty"`
        } `json:"Statement"`
    }

    if err := json.Unmarshal([]byte(policyJson), &policy); err != nil {
        return ObservedState{}, fmt.Errorf("failed to parse policy: %w", err)
    }

    for _, stmt := range policy.Statement {
        if stmt.Sid == statementId {
            return observedFromStatement(stmt, functionName), nil
        }
    }

    return ObservedState{}, &types.ResourceNotFoundException{
        Message: aws.String("statement " + statementId + " not found in policy"),
    }
}
```

### Key Implementation: AddPermission

```go
func (r *realPermissionAPI) AddPermission(ctx context.Context, spec LambdaPermissionSpec) (string, error) {
    r.limiter.Wait(ctx)

    input := &lambda.AddPermissionInput{
        FunctionName: &spec.FunctionName,
        StatementId:  &spec.StatementId,
        Action:       &spec.Action,
        Principal:    &spec.Principal,
    }

    if spec.SourceArn != "" {
        input.SourceArn = &spec.SourceArn
    }
    if spec.SourceAccount != "" {
        input.SourceAccount = &spec.SourceAccount
    }
    if spec.EventSourceToken != "" {
        input.EventSourceToken = &spec.EventSourceToken
    }
    if spec.Qualifier != "" {
        input.Qualifier = &spec.Qualifier
    }

    out, err := r.client.AddPermission(ctx, input)
    if err != nil {
        return "", err
    }

    return aws.ToString(out.Statement), nil
}
```

---

## Step 5 — Drift Detection

**File**: `internal/drivers/lambdaperm/drift.go`

### Drift-Detectable Fields

| Field | Drift Source | Notes |
|---|---|---|
| Statement existence | External removal | Statement removed via CLI/console |
| Action | External change | Statement removed and re-added with different action |
| Principal | External change | Statement removed and re-added with different principal |
| Source ARN | External change | Condition changed |
| Source Account | External change | Condition changed |

### HasDrift

```go
func HasDrift(desired LambdaPermissionSpec, observed ObservedState) bool {
    if desired.Action != observed.Action { return true }
    if desired.Principal != observed.Principal { return true }
    if desired.SourceArn != observed.SourceArn { return true }
    if desired.SourceAccount != observed.SourceAccount { return true }
    return false
}
```

### ComputeFieldDiffs

```go
func ComputeFieldDiffs(desired LambdaPermissionSpec, observed ObservedState) []types.FieldDiff {
    var diffs []types.FieldDiff
    if desired.Action != observed.Action {
        diffs = append(diffs, types.FieldDiff{Field: "action", Desired: desired.Action, Observed: observed.Action})
    }
    if desired.Principal != observed.Principal {
        diffs = append(diffs, types.FieldDiff{Field: "principal", Desired: desired.Principal, Observed: observed.Principal})
    }
    if desired.SourceArn != observed.SourceArn {
        diffs = append(diffs, types.FieldDiff{Field: "sourceArn", Desired: desired.SourceArn, Observed: observed.SourceArn})
    }
    if desired.SourceAccount != observed.SourceAccount {
        diffs = append(diffs, types.FieldDiff{Field: "sourceAccount", Desired: desired.SourceAccount, Observed: observed.SourceAccount})
    }
    return diffs
}
```

---

## Step 6 — Driver Implementation

**File**: `internal/drivers/lambdaperm/driver.go`

### Constructor

```go
type LambdaPermissionDriver struct {
    auth       authservice.AuthClient
    apiFactory func(aws.Config) PermissionAPI
}

func NewLambdaPermissionDriver(auth authservice.AuthClient) *LambdaPermissionDriver {
    return NewLambdaPermissionDriverWithFactory(auth, func(cfg aws.Config) PermissionAPI {
        return NewPermissionAPI(awsclient.NewLambdaClient(cfg))
    })
}

func (d *LambdaPermissionDriver) ServiceName() string { return ServiceName }
```

### Provision

Provision handles three cases:

1. **New permission**: Add the statement.
2. **Unchanged permission**: Return existing outputs (idempotent).
3. **Changed permission**: Remove old statement, add new one (replace).

```go
func (d *LambdaPermissionDriver) Provision(ctx restate.ObjectContext, spec LambdaPermissionSpec) (LambdaPermissionOutputs, error) {
    state, _ := restate.Get[*LambdaPermissionState](ctx, drivers.StateKey)
    api := d.buildAPI(spec.Account, spec.Region)

    // If existing state and spec hasn't changed, return early
    if state != nil && state.Outputs.StatementId != "" && !specChanged(spec, state.Desired) {
        return state.Outputs, nil
    }

    // If existing state with different spec, remove old statement first
    if state != nil && state.Outputs.StatementId != "" {
        if _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
            return restate.Void{}, api.RemovePermission(rc, state.Desired.FunctionName, state.Desired.StatementId)
        }); err != nil {
            if !isNotFound(err) {
                return LambdaPermissionOutputs{}, err
            }
        }
    }

    // Write pending state
    newState := &LambdaPermissionState{
        Desired:    spec,
        Status:     types.StatusProvisioning,
        Mode:       drivers.DefaultMode(""),
        Generation: stateGeneration(state) + 1,
    }
    restate.Set(ctx, drivers.StateKey, newState)

    // Add the permission
    statement, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
        return api.AddPermission(rc, spec)
    })
    if err != nil {
        if isConflict(err) {
            return LambdaPermissionOutputs{}, restate.TerminalError(
                fmt.Errorf("statement %q already exists on function %q", spec.StatementId, spec.FunctionName), 409)
        }
        if isNotFound(err) {
            return LambdaPermissionOutputs{}, restate.TerminalError(
                fmt.Errorf("function %q not found in %s", spec.FunctionName, spec.Region), 404)
        }
        return LambdaPermissionOutputs{}, err
    }

    // Get observed state
    observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
        return api.GetPermission(rc, spec.FunctionName, spec.StatementId)
    })
    if err != nil {
        // Non-fatal — permission was added, just can't verify
        observed = ObservedState{
            StatementId:  spec.StatementId,
            FunctionName: spec.FunctionName,
            Action:       spec.Action,
            Principal:    spec.Principal,
            SourceArn:    spec.SourceArn,
            SourceAccount: spec.SourceAccount,
        }
    }

    outputs := LambdaPermissionOutputs{
        StatementId:  spec.StatementId,
        FunctionName: spec.FunctionName,
        Statement:    statement,
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
func (d *LambdaPermissionDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (LambdaPermissionOutputs, error) {
    api := d.buildAPI(ref.Account, ref.Region)

    // ResourceID is "functionName~statementId"
    parts := strings.SplitN(ref.ResourceID, "~", 2)
    if len(parts) != 2 {
        return LambdaPermissionOutputs{}, restate.TerminalError(
            fmt.Errorf("resource ID must be functionName~statementId, got %q", ref.ResourceID), 400)
    }
    functionName, statementId := parts[0], parts[1]

    observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
        return api.GetPermission(rc, functionName, statementId)
    })
    if err != nil {
        if isNotFound(err) {
            return LambdaPermissionOutputs{}, restate.TerminalError(
                fmt.Errorf("permission %q not found on function %q", statementId, functionName), 404)
        }
        return LambdaPermissionOutputs{}, err
    }

    spec := specFromObserved(observed, ref)
    outputs := LambdaPermissionOutputs{
        StatementId:  statementId,
        FunctionName: functionName,
    }

    mode := types.ModeObserved
    if ref.Mode != "" {
        mode = ref.Mode
    }

    restate.Set(ctx, drivers.StateKey, &LambdaPermissionState{
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
func (d *LambdaPermissionDriver) Delete(ctx restate.ObjectContext) error {
    state, err := restate.Get[*LambdaPermissionState](ctx, drivers.StateKey)
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
        return restate.Void{}, api.RemovePermission(rc, state.Desired.FunctionName, state.Desired.StatementId)
    }); err != nil {
        if !isNotFound(err) {
            return err
        }
    }

    state.Status = types.StatusDeleted
    restate.Set(ctx, drivers.StateKey, state)
    restate.Clear(ctx, drivers.StateKey)

    return nil
}
```

### Reconcile

```go
func (d *LambdaPermissionDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
    state, err := restate.Get[*LambdaPermissionState](ctx, drivers.StateKey)
    if err != nil {
        return types.ReconcileResult{}, err
    }
    if state == nil {
        return types.ReconcileResult{Status: "no-state"}, nil
    }

    state.ReconcileScheduled = false
    api := d.buildAPI(state.Desired.Account, state.Desired.Region)

    observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
        return api.GetPermission(rc, state.Desired.FunctionName, state.Desired.StatementId)
    })
    if err != nil {
        if isNotFound(err) {
            state.Status = types.StatusError
            state.Error = "permission statement not found — may have been removed externally"
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
        // Correct drift: remove old, add new
        if _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
            return restate.Void{}, api.RemovePermission(rc, state.Desired.FunctionName, state.Desired.StatementId)
        }); err != nil {
            if !isNotFound(err) {
                state.Error = fmt.Sprintf("drift correction (remove) failed: %v", err)
                state.Status = types.StatusError
                restate.Set(ctx, drivers.StateKey, state)
                d.scheduleReconcile(ctx)
                return types.ReconcileResult{Status: "error", Error: state.Error}, nil
            }
        }

        if _, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
            return api.AddPermission(rc, state.Desired)
        }); err != nil {
            state.Error = fmt.Sprintf("drift correction (add) failed: %v", err)
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

### GetStatus / GetOutputs

Follow the standard pattern (identical to Lambda Function driver).

---

## Step 7 — Provider Adapter

**File**: `internal/core/provider/lambdaperm_adapter.go`

```go
type LambdaPermissionAdapter struct {
    accounts *auth.Registry
}

func NewLambdaPermissionAdapterWithRegistry(accounts *auth.Registry) *LambdaPermissionAdapter {
    return &LambdaPermissionAdapter{accounts: accounts}
}

func (a *LambdaPermissionAdapter) Kind() string { return lambdaperm.ServiceName }

func (a *LambdaPermissionAdapter) Scope() KeyScope { return KeyScopeRegion }

func (a *LambdaPermissionAdapter) BuildKey(doc json.RawMessage) (string, error) {
    region, _ := jsonpath.String(doc.Spec, "region")
    functionName, _ := jsonpath.String(doc.Spec, "functionName")
    statementId, _ := jsonpath.String(doc.Spec, "statementId")
    if region == "" || functionName == "" || statementId == "" {
        return "", fmt.Errorf("LambdaPermission requires spec.region, spec.functionName, and spec.statementId")
    }
    return region + "~" + functionName + "~" + statementId, nil
}

func (a *LambdaPermissionAdapter) BuildImportKey(region, resourceID string) (string, error) {
    // resourceID is "functionName~statementId"
    return region + "~" + resourceID, nil
}

func (a *LambdaPermissionAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
    spec, err := decodeSpec(doc)
    if err != nil {
        return types.PlanResult{}, err
    }

    if len(currentOutputs) == 0 {
        return types.PlanResult{Op: types.OpCreate, Spec: spec}, nil
    }

    // Check if any spec fields have changed
    cfg, err := a.accounts.GetConfig(spec.Account, spec.Region)
    if err != nil {
        return types.PlanResult{}, err
    }
    client := awsclient.NewLambdaClient(cfg)
    api := newRealPermissionAPI(client, ratelimit.New("lambda-permission", 15, 10))

    observed, err := api.GetPermission(ctx, spec.FunctionName, spec.StatementId)
    if err != nil {
        if isNotFound(err) {
            return types.PlanResult{Op: types.OpCreate, Spec: spec}, nil
        }
        return types.PlanResult{}, err
    }

    if HasDrift(spec, observed) {
        diffs := ComputeFieldDiffs(spec, observed)
        return types.PlanResult{Op: types.OpUpdate, Spec: spec, Diffs: diffs}, nil
    }

    return types.PlanResult{Op: types.OpNoop, Spec: spec}, nil
}
```

---

## Step 8 — Registry Integration

**File**: `internal/core/provider/registry.go` — **MODIFY**

Add `NewLambdaPermissionAdapterWithRegistry(accounts)` to `NewRegistry()`.

---

## Step 9 — Compute Driver Pack Entry Point

**File**: `cmd/praxis-compute/main.go` — **MODIFY**

```go
import "github.com/shirvan/praxis/internal/drivers/lambdaperm"

Bind(restate.Reflect(lambdaperm.NewLambdaPermissionDriver(cfg.Auth())))
```

---

## Step 10 — Docker Compose & Justfile

### Docker Compose

No changes needed.

### Justfile Additions

```just
test-lambda-permission:
    go test ./internal/drivers/lambdaperm/... -v -count=1 -race

test-lambda-permission-integration:
    go test ./tests/integration/... -run TestLambdaPermission -v -timeout=3m
```

---

## Step 11 — Unit Tests

**File**: `internal/drivers/lambdaperm/driver_test.go`

| Test | Description |
|---|---|
| `TestProvision_NewPermission` | Adds permission; verifies outputs and state |
| `TestProvision_NoChange` | Same spec; verifies idempotent return |
| `TestProvision_ReplacePermission` | Changed principal; verifies remove + add |
| `TestProvision_FunctionNotFound` | Target function doesn't exist; verifies 404 |
| `TestProvision_StatementConflict` | Statement already exists on first create; verifies 409 |
| `TestImport_Success` | Imports existing permission; verifies state |
| `TestImport_NotFound` | Statement not in policy; verifies 404 |
| `TestImport_BadResourceId` | Invalid resource ID format; verifies 400 |
| `TestDelete_Managed` | Removes permission; verifies cleanup |
| `TestDelete_Observed` | Cannot delete observed; verifies 403 |
| `TestDelete_AlreadyGone` | Statement already removed; verifies idempotent |
| `TestReconcile_NoDrift` | Permission matches; verifies ok |
| `TestReconcile_PrincipalChanged` | Principal drifted; verifies correction |
| `TestReconcile_StatementRemoved` | Statement deleted externally; verifies error |

**File**: `internal/drivers/lambdaperm/drift_test.go`

| Test | Description |
|---|---|
| `TestHasDrift_NoDrift` | All fields match; no drift |
| `TestHasDrift_ActionChanged` | Action differs; drift detected |
| `TestHasDrift_PrincipalChanged` | Principal differs; drift detected |
| `TestHasDrift_SourceArnChanged` | Source ARN differs; drift detected |

---

## Step 12 — Integration Tests

**File**: `tests/integration/lambda_permission_driver_test.go`

| Test | Description |
|---|---|
| `TestLambdaPermission_AddAndVerify` | Add permission, verify in policy |
| `TestLambdaPermission_Replace` | Add, change spec, verify old removed + new added |
| `TestLambdaPermission_Import` | Add permission via AWS API, import, verify state |
| `TestLambdaPermission_Delete` | Add then remove, verify not in policy |
| `TestLambdaPermission_Reconcile` | Add, externally remove, reconcile in managed mode |

### LocalStack Considerations

- LocalStack supports `AddPermission`, `RemovePermission`, and `GetPolicy`.
- Tests must create a Lambda function first (prerequisite) before adding permissions.
- Use a minimal Lambda function with the Python runtime and a no-op handler.

---

## Lambda-Permission-Specific Design Decisions

### 1. Replace-on-Change Strategy

**Decision**: When any field changes, the driver removes the old statement and adds
a new one.

**Rationale**: Lambda permission statements are immutable. AWS provides no
`UpdatePermission` API. The only way to change a permission is remove + add.
This is atomic within the Restate journal — if the handler crashes between remove
and add, replay will re-execute both.

### 2. Composite Key with Three Components

**Decision**: The VO key is `region~functionName~statementId` (three-part composite).

**Rationale**: A statement is uniquely identified by its ID within a specific
function's policy. Two functions can have statements with the same ID. The function
name must be part of the key to ensure uniqueness.

### 3. Policy Parsing

**Decision**: `GetPermission` calls `GetPolicy` and parses the JSON to extract the
specific statement.

**Rationale**: AWS does not provide a `GetPermission` API for a single statement.
The only way to read a permission is to get the full policy and parse it. The driver
parses the standard IAM policy JSON format (`Version`, `Statement[]`, `Sid`,
`Effect`, `Principal`, `Action`, `Condition`).

### 4. Import Default Mode

**Decision**: Import defaults to `ModeObserved`.

**Rationale**: Consistent with all other Lambda drivers. Permissions are typically
part of a broader security configuration — importing as observed prevents accidental
modification or removal.

### 5. Drift Correction in Managed Mode

**Decision**: In managed mode, drift correction removes the drifted statement and
re-adds the desired one.

**Rationale**: Since statements are immutable, the only correction path is remove +
add. This is safe because the statement ID doesn't change — functions referencing
the permission by its existence (not by content) are unaffected during the brief
gap between remove and add.

### 6. No Tags

**Decision**: Lambda permissions do not support tags.

**Rationale**: Permissions are policy statements, not first-class AWS resources.
They have no ARN, no tags, and no independent lifecycle. The statement ID within the
function's policy is the sole identity.

---

## Checklist

### Implementation

- [ ] `schemas/aws/lambda/permission.cue`
- [ ] `internal/drivers/lambdaperm/types.go`
- [ ] `internal/drivers/lambdaperm/aws.go`
- [ ] `internal/drivers/lambdaperm/drift.go`
- [ ] `internal/drivers/lambdaperm/driver.go`
- [ ] `internal/core/provider/lambdaperm_adapter.go`

### Tests

- [ ] `internal/drivers/lambdaperm/driver_test.go`
- [ ] `internal/drivers/lambdaperm/aws_test.go`
- [ ] `internal/drivers/lambdaperm/drift_test.go`
- [ ] `internal/core/provider/lambdaperm_adapter_test.go`
- [ ] `tests/integration/lambda_permission_driver_test.go`

### Integration

- [ ] `cmd/praxis-compute/main.go` — Bind driver
- [ ] `internal/core/provider/registry.go` — Register adapter
- [ ] `justfile` — Add test targets
