# CloudWatch Dashboard Driver — Implementation Plan

> Target: A Restate Virtual Object driver that manages CloudWatch Dashboards, following
> the exact patterns established by the S3, Security Group, EC2, VPC, EBS, Elastic IP,
> Key Pair, AMI, Lambda, Log Group, and Metric Alarm drivers.
>
> Key scope: `KeyScopeRegion` — key format is `region~dashboardName`, permanent and
> immutable for the lifetime of the Virtual Object. The AWS-assigned dashboard ARN
> lives only in state/outputs.

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
12. [Step 9 — Monitoring Driver Pack Entry Point](#step-9--monitoring-driver-pack-entry-point)
13. [Step 10 — Docker Compose & Justfile](#step-10--docker-compose--justfile)
14. [Step 11 — Unit Tests](#step-11--unit-tests)
15. [Step 12 — Integration Tests](#step-12--integration-tests)
16. [Dashboard-Specific Design Decisions](#dashboard-specific-design-decisions)
17. [Checklist](#checklist)

---

## 1. Overview & Scope

The Dashboard driver manages the lifecycle of **CloudWatch dashboards** only.
Dashboard sharing, automatic dashboards, annotations, and cross-account dashboards
are separate CloudWatch features and are not managed by this driver. This document
focuses exclusively on dashboard creation, body updates, import, deletion, and drift
reconciliation.

The driver follows the established Virtual Object contract:

| Handler | Context | Purpose |
|---|---|---|
| `Provision` | `ObjectContext` (exclusive) | Create or converge a dashboard |
| `Import` | `ObjectContext` (exclusive) | Adopt an existing dashboard |
| `Delete` | `ObjectContext` (exclusive) | Delete a dashboard |
| `Reconcile` | `ObjectContext` (exclusive) | Detect/correct drift (report-only for Observed mode) |
| `GetStatus` | `ObjectSharedContext` (shared) | Return current status |
| `GetOutputs` | `ObjectSharedContext` (shared) | Return dashboard outputs |

### Mutable vs Immutable Attributes

| Attribute | Mutability | API | Notes |
|---|---|---|---|
| Dashboard name | Immutable | — | Set at creation; cannot be renamed |
| Dashboard ARN | Immutable | — | AWS-assigned; derived from account, region, and name |
| Dashboard body | Mutable | `PutDashboard` | JSON string defining widgets; the entire body is replaced on update |

### PutDashboard Is an Upsert

Like `PutMetricAlarm`, `PutDashboard` is an **upsert** operation: if the dashboard
does not exist, it creates it; if it already exists, it replaces the entire body.
This simplifies the create vs update decision — both paths use the same API call.
The driver still tracks state for drift detection but the AWS interaction is a
single converge call.

### Dashboard Body Is Opaque JSON

The dashboard body is a JSON string conforming to the [CloudWatch Dashboard Body
Structure](https://docs.aws.amazon.com/AmazonCloudWatch/latest/APIReference/CloudWatch-Dashboard-Body-Structure.html).
The driver treats it as an opaque blob — it does not parse, validate, or diff
individual widgets. AWS validates the body on `PutDashboard` and returns structured
`DashboardValidationMessage` errors if the body is malformed.

### What Is NOT In Scope

- **Dashboard Sharing**: Sharing dashboards with external accounts. Separate API
  (`EnableSharingForDashboard`). Future extension.
- **Automatic Dashboards**: AWS auto-generated dashboards for certain services.
  Not user-managed resources.
- **Annotations**: Dashboard annotations are part of the widget configuration
  within the dashboard body, not separate resources.
- **Cross-Account Dashboards**: Dashboards that display metrics from multiple accounts.
  Configured via dashboard body source account references, not separate resources.
- **Widget-Level Management**: The driver manages the entire dashboard body as a
  single unit. There is no per-widget CRUD — updating a single widget requires
  replacing the entire body.
- **Contributor Insights Rules**: Separate CloudWatch feature, not part of dashboards.

### Downstream Consumers

```
${resources.my-dashboard.outputs.dashboardArn}     → IAM policies, sharing configuration
${resources.my-dashboard.outputs.dashboardName}     → CLI references, console URLs
```

---

## 2. Key Strategy

### Key Format: `region~dashboardName`

CloudWatch dashboard names are unique within a region+account. The CUE schema maps
`metadata.name` to the dashboard name. The adapter produces `region~metadata.name`
as the Virtual Object key.

1. **BuildKey** (adapter, plan-time): returns `region~metadata.name`.
2. **Provision / Delete**: dispatched to the same VO key.
3. **Plan**: reads VO state via `GetOutputs`. If outputs contain a dashboard ARN,
   describes the dashboard by name. Otherwise, `OpCreate`.
4. **Import**: `BuildImportKey(region, resourceID)` returns `region~resourceID`
   where `resourceID` is the dashboard name. Same key as BuildKey.

### Ownership Tags

Dashboard names are unique within a region+account. `PutDashboard` is an upsert,
so calling it on an existing dashboard does not error — it replaces the body. Unlike
alarms and log groups, dashboards do not support the standard CloudWatch `TagResource`
API in the same way. The driver tracks ownership via the Virtual Object key mapping
in Restate state rather than via AWS tags.

**FindByManagedKey** is NOT needed because dashboard names are AWS-enforced unique
per region per account.

### Import Semantics

Import and template-based management produce the **same Virtual Object key**:

- `praxis import --kind Dashboard --region us-east-1 --resource-id my-dashboard`:
  Creates VO key `us-east-1~my-dashboard`.
- Template with `metadata.name: "my-dashboard"` in `us-east-1`:
  Creates VO key `us-east-1~my-dashboard`.

---

## 3. File Inventory

Create or modify these files (✦ = new file, ✎ = modify existing):

```text
✦ internal/drivers/dashboard/types.go                — Spec, Outputs, ObservedState, State structs
✦ internal/drivers/dashboard/aws.go                  — DashboardAPI interface + realDashboardAPI impl
✦ internal/drivers/dashboard/drift.go                — HasDrift(), ComputeFieldDiffs()
✦ internal/drivers/dashboard/driver.go               — DashboardDriver Virtual Object
✦ internal/drivers/dashboard/driver_test.go          — Unit tests for driver (mocked AWS)
✦ internal/drivers/dashboard/aws_test.go             — Unit tests for error classification
✦ internal/drivers/dashboard/drift_test.go           — Unit tests for drift detection
✦ internal/core/provider/dashboard_adapter.go        — DashboardAdapter implementing provider.Adapter
✦ internal/core/provider/dashboard_adapter_test.go   — Unit tests for adapter
✦ schemas/aws/cloudwatch/dashboard.cue               — CUE schema for Dashboard resource
✦ tests/integration/dashboard_driver_test.go         — Integration tests (Testcontainers + LocalStack)
✎ internal/infra/awsclient/client.go                 — Uses existing NewCloudWatchClient()
✎ cmd/praxis-monitoring/main.go                      — Bind Dashboard driver
✎ internal/core/provider/registry.go                 — Add adapter to NewRegistry()
✎ justfile                                           — Add dashboard test targets
```

---

## Step 1 — CUE Schema

**File**: `schemas/aws/cloudwatch/dashboard.cue`

```cue
package cloudwatch

#Dashboard: {
    apiVersion: "praxis.io/v1"
    kind:       "Dashboard"

    metadata: {
        // name is the CloudWatch dashboard name in AWS.
        // Must contain only alphanumeric characters, hyphens, and underscores.
        // Max length 255 characters.
        name: string & =~"^[a-zA-Z0-9_\\-]{1,255}$"
        labels: [string]: string
    }

    spec: {
        // region is the AWS region to create the dashboard in.
        region: string

        // account is the optional AWS account alias for credential resolution.
        account?: string

        // dashboardBody is the JSON string defining the dashboard widgets and layout.
        // Must conform to the CloudWatch Dashboard Body Structure specification.
        // See: https://docs.aws.amazon.com/AmazonCloudWatch/latest/APIReference/CloudWatch-Dashboard-Body-Structure.html
        dashboardBody: string
    }

    outputs?: {
        dashboardArn:  string
        dashboardName: string
    }
}
```

### Schema Design Notes

- **`dashboardBody` is a raw string**: The body is a JSON document, but CUE treats
  it as a plain string. Structural validation of widget definitions is left to AWS —
  `PutDashboard` returns `DashboardValidationMessage` errors for malformed bodies.
  Attempting to model the full dashboard body structure in CUE would be extremely
  complex and brittle, providing little value over the AWS-side validation.
- **No tags field**: CloudWatch dashboards do not support the standard tag API in the
  same way as other CloudWatch resources. While dashboards have an ARN and can be
  referenced in IAM policies, tag management is not a first-class operation.
- **`name` regex**: Dashboard names are more restrictive than log group names or alarm
  names — no dots, slashes, or spaces. Only alphanumeric characters, hyphens, and
  underscores.
- **Multi-line CUE strings**: Users will typically use CUE's multi-line string syntax
  (`"""..."""`) for the dashboard body to maintain readability in templates.

---

## Step 2 — AWS Client Factory

**File**: `internal/infra/awsclient/client.go` — **Uses existing NewCloudWatchClient()**

The Dashboard driver shares the `cloudwatch.Client` with the Metric Alarm driver.
Both use the `github.com/aws/aws-sdk-go-v2/service/cloudwatch` SDK package.
No new factory function is needed — `NewCloudWatchClient()` was added as part of the
Metric Alarm driver implementation.

---

## Step 3 — Driver Types

**File**: `internal/drivers/dashboard/types.go`

```go
package dashboard

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate Virtual Object name for CloudWatch Dashboards.
const ServiceName = "Dashboard"

// DashboardSpec is the desired state for a CloudWatch dashboard.
type DashboardSpec struct {
    Account       string `json:"account,omitempty"`
    Region        string `json:"region"`
    DashboardName string `json:"dashboardName"`
    DashboardBody string `json:"dashboardBody"`
    ManagedKey    string `json:"managedKey,omitempty"`
}

// DashboardOutputs is produced after provisioning and stored in Restate K/V.
type DashboardOutputs struct {
    DashboardArn  string `json:"dashboardArn"`
    DashboardName string `json:"dashboardName"`
}

// ObservedState captures the actual configuration of a dashboard from AWS.
type ObservedState struct {
    DashboardArn  string `json:"dashboardArn"`
    DashboardName string `json:"dashboardName"`
    DashboardBody string `json:"dashboardBody"`
}

// DashboardState is the single atomic state object stored under drivers.StateKey.
type DashboardState struct {
    Desired            DashboardSpec        `json:"desired"`
    Observed           ObservedState        `json:"observed"`
    Outputs            DashboardOutputs     `json:"outputs"`
    Status             types.ResourceStatus `json:"status"`
    Mode               types.Mode           `json:"mode"`
    Error              string               `json:"error,omitempty"`
    Generation         int64                `json:"generation"`
    LastReconcile      string               `json:"lastReconcile,omitempty"`
    ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
```

### Types Design Notes

- **Minimal outputs**: Dashboards have a very small output surface — just the ARN and
  name. There is no "state" like alarms or "stored bytes" like log groups.
- **`ObservedState.DashboardBody`**: The full JSON body as returned by `GetDashboard`.
  Used for drift comparison against the desired body.
- **No tags in state**: Tags are not a managed attribute for dashboards, so they do
  not appear in spec, observed state, or outputs.

---

## Step 4 — AWS API Abstraction Layer

**File**: `internal/drivers/dashboard/aws.go`

### DashboardAPI Interface

```go
type DashboardAPI interface {
    // PutDashboard creates or updates a dashboard (upsert).
    // Returns validation messages if the body has warnings (non-fatal).
    PutDashboard(ctx context.Context, spec DashboardSpec) ([]ValidationMessage, error)

    // GetDashboard returns the current state of a dashboard.
    // Returns (nil, nil) if the dashboard does not exist.
    GetDashboard(ctx context.Context, dashboardName string) (*ObservedState, error)

    // DeleteDashboard deletes one or more dashboards by name.
    DeleteDashboard(ctx context.Context, dashboardName string) error

    // ListDashboards lists dashboards with an optional name prefix.
    // Used for import discovery. Not used in normal driver operations.
    ListDashboards(ctx context.Context, namePrefix string) ([]DashboardEntry, error)
}

// ValidationMessage represents a warning or error from PutDashboard.
type ValidationMessage struct {
    DataPath string `json:"dataPath"`
    Message  string `json:"message"`
}

// DashboardEntry is a summary returned by ListDashboards.
type DashboardEntry struct {
    DashboardName string `json:"dashboardName"`
    DashboardArn  string `json:"dashboardArn"`
    LastModified  string `json:"lastModified"`
    Size          int64  `json:"size"`
}
```

### Implementation: realDashboardAPI

```go
type realDashboardAPI struct {
    client  *cloudwatch.Client
    limiter ratelimit.Limiter
}

func newRealDashboardAPI(client *cloudwatch.Client, limiter ratelimit.Limiter) DashboardAPI {
    return &realDashboardAPI{client: client, limiter: limiter}
}
```

### Error Classification

```go
func isNotFound(err error) bool {
    var rnf *cwtypes.ResourceNotFoundException
    if errors.As(err, &rnf) {
        return true
    }
    // GetDashboard returns ResourceNotFound; also check for
    // DashboardNotFoundError which may be returned by some operations.
    var dnf *cwtypes.DashboardNotFoundError
    return errors.As(err, &dnf)
}

func isDashboardInvalidInput(err error) bool {
    var die *cwtypes.DashboardInvalidInputError
    return errors.As(err, &die)
}

func isInvalidParam(err error) bool {
    var ipv *cwtypes.InvalidParameterValueException
    return errors.As(err, &ipv)
}

func isThrottled(err error) bool {
    var te *cwtypes.ThrottlingException
    return errors.As(err, &te)
}
```

### Key Implementation Details

#### PutDashboard

```go
func (r *realDashboardAPI) PutDashboard(ctx context.Context, spec DashboardSpec) ([]ValidationMessage, error) {
    r.limiter.Wait(ctx)

    input := &cloudwatch.PutDashboardInput{
        DashboardName: &spec.DashboardName,
        DashboardBody: &spec.DashboardBody,
    }

    out, err := r.client.PutDashboard(ctx, input)
    if err != nil {
        return nil, err
    }

    // PutDashboard may return validation messages even on success.
    // These are warnings about deprecated widget properties, etc.
    var msgs []ValidationMessage
    if out != nil && len(out.DashboardValidationMessages) > 0 {
        for _, m := range out.DashboardValidationMessages {
            msgs = append(msgs, ValidationMessage{
                DataPath: deref(m.DataPath),
                Message:  deref(m.Message),
            })
        }
    }

    return msgs, nil
}
```

#### GetDashboard

```go
func (r *realDashboardAPI) GetDashboard(ctx context.Context, dashboardName string) (*ObservedState, error) {
    r.limiter.Wait(ctx)

    out, err := r.client.GetDashboard(ctx, &cloudwatch.GetDashboardInput{
        DashboardName: &dashboardName,
    })
    if err != nil {
        if isNotFound(err) {
            return nil, nil
        }
        return nil, err
    }

    observed := &ObservedState{
        DashboardArn:  deref(out.DashboardArn),
        DashboardName: deref(out.DashboardName),
        DashboardBody: deref(out.DashboardBody),
    }

    return observed, nil
}
```

**Note**: `GetDashboard` returns the full dashboard body. Unlike `DescribeAlarms`
or `DescribeLogGroups`, this is a single call that returns all the information
the driver needs — no separate tag fetch required.

#### DeleteDashboard

```go
func (r *realDashboardAPI) DeleteDashboard(ctx context.Context, dashboardName string) error {
    r.limiter.Wait(ctx)

    _, err := r.client.DeleteDashboards(ctx, &cloudwatch.DeleteDashboardsInput{
        DashboardNames: []string{dashboardName},
    })
    return err
}
```

**Note**: The AWS API is `DeleteDashboards` (plural) — it accepts a list of dashboard
names. The driver always passes a single name. The API returns `DashboardNotFoundError`
if any of the specified dashboards do not exist.

---

## Step 5 — Drift Detection

**File**: `internal/drivers/dashboard/drift.go`

### Drift-Detectable Fields

| Field | Drift Source | Notes |
|---|---|---|
| DashboardBody | Console, CLI, IaC | Dashboard body JSON replaced or modified externally |

### Fields NOT Drift-Detected

- **Dashboard ARN**: Immutable. Derived from account, region, and name.
- **Dashboard name**: Immutable. Part of the Virtual Object key.
- **Last modified time**: Read-only metadata, not a managed attribute.
- **Size**: Read-only metadata, derived from the body.

### HasDrift

```go
func HasDrift(desired DashboardSpec, observed ObservedState) bool {
    return !bodiesEqual(desired.DashboardBody, observed.DashboardBody)
}
```

### ComputeFieldDiffs

```go
func ComputeFieldDiffs(desired DashboardSpec, observed ObservedState) []types.FieldDiff {
    var diffs []types.FieldDiff

    if !bodiesEqual(desired.DashboardBody, observed.DashboardBody) {
        diffs = append(diffs, types.FieldDiff{
            Field:    "dashboardBody",
            Desired:  truncateBody(desired.DashboardBody, 200),
            Observed: truncateBody(observed.DashboardBody, 200),
        })
    }

    return diffs
}
```

### JSON-Semantic Body Comparison

```go
// bodiesEqual compares two dashboard body JSON strings for semantic equality.
// AWS may reformat the body (e.g., reorder keys, normalize whitespace) when
// storing it. A simple string comparison would produce false-positive drift.
// This function unmarshals both bodies and compares the resulting structures.
func bodiesEqual(desired, observed string) bool {
    var d, o any
    if err := json.Unmarshal([]byte(desired), &d); err != nil {
        // If desired is not valid JSON, fall back to string comparison.
        // AWS will reject this on PutDashboard anyway.
        return desired == observed
    }
    if err := json.Unmarshal([]byte(observed), &o); err != nil {
        return false
    }
    return reflect.DeepEqual(d, o)
}

// truncateBody returns the first n characters of a body string for diff display.
func truncateBody(body string, n int) string {
    if len(body) <= n {
        return body
    }
    return body[:n] + "..."
}
```

### Why JSON-Semantic Comparison

AWS CloudWatch may normalize the dashboard body when storing it. For example:
- Key ordering within JSON objects may change.
- Whitespace may be normalized (indentation, trailing newlines).
- Numeric values may be reformatted (e.g., `5.0` → `5`).

A naive string comparison between the user's CUE template body and the AWS-returned
body would produce false-positive drift on every reconcile cycle. The driver
unmarshals both bodies to Go `any` values and compares the resulting structures,
ignoring formatting differences.

---

## Step 6 — Driver Implementation

**File**: `internal/drivers/dashboard/driver.go`

### Constructor

```go
type DashboardDriver struct {
    accounts   *auth.Registry
    apiFactory func(aws.Config) DashboardAPI
}

func NewDashboardDriver(accounts *auth.Registry) *DashboardDriver {
    return NewDashboardDriverWithFactory(accounts, func(cfg aws.Config) DashboardAPI {
        return NewDashboardAPI(awsclient.NewCloudWatchClient(cfg))
    })
}

func NewDashboardDriverWithFactory(accounts *auth.Registry, factory func(aws.Config) DashboardAPI) *DashboardDriver {
    if accounts == nil {
        accounts = auth.LoadFromEnv()
    }
    if factory == nil {
        factory = func(cfg aws.Config) DashboardAPI {
            return NewDashboardAPI(awsclient.NewCloudWatchClient(cfg))
        }
    }
    return &DashboardDriver{accounts: accounts, apiFactory: factory}
}

func (DashboardDriver) ServiceName() string { return ServiceName }
```

### Provision

Because `PutDashboard` is an upsert, the create and update paths converge on the
same API call. This makes the Dashboard driver the simplest of the three CloudWatch
drivers.

```go
func (d *DashboardDriver) Provision(ctx restate.ObjectContext, spec DashboardSpec) (DashboardOutputs, error) {
    // Load existing state
    state, _ := restate.Get[*DashboardState](ctx, drivers.StateKey)

    api := d.buildAPI(spec.Account, spec.Region)

    gen := int64(1)
    if state != nil {
        gen = state.Generation + 1
    }

    // Write pending state
    restate.Set(ctx, drivers.StateKey, &DashboardState{
        Desired:    spec,
        Status:     types.StatusProvisioning,
        Mode:       drivers.DefaultMode(state),
        Generation: gen,
    })

    // Put dashboard (upsert — works for both create and update)
    validationMsgs, err := restate.Run(ctx, func(rc restate.RunContext) ([]ValidationMessage, error) {
        return api.PutDashboard(rc, spec)
    })
    if err != nil {
        if isDashboardInvalidInput(err) {
            return DashboardOutputs{}, restate.TerminalError(
                fmt.Errorf("invalid dashboard body: %w", err), 400)
        }
        if isInvalidParam(err) {
            return DashboardOutputs{}, restate.TerminalError(
                fmt.Errorf("invalid dashboard parameter: %w", err), 400)
        }
        return DashboardOutputs{}, err
    }

    // Log validation warnings (non-fatal)
    if len(validationMsgs) > 0 {
        for _, msg := range validationMsgs {
            slog.Warn("dashboard validation warning",
                "dashboardName", spec.DashboardName,
                "dataPath", msg.DataPath,
                "message", msg.Message)
        }
    }

    // Get dashboard to populate observed state and ARN
    observed, err := restate.Run(ctx, func(rc restate.RunContext) (*ObservedState, error) {
        return api.GetDashboard(rc, spec.DashboardName)
    })
    if err != nil || observed == nil {
        return DashboardOutputs{}, fmt.Errorf("failed to get dashboard after put: %w", err)
    }

    outputs := DashboardOutputs{
        DashboardArn:  observed.DashboardArn,
        DashboardName: observed.DashboardName,
    }

    restate.Set(ctx, drivers.StateKey, &DashboardState{
        Desired:    spec,
        Observed:   *observed,
        Outputs:    outputs,
        Status:     types.StatusReady,
        Mode:       types.ModeManaged,
        Generation: gen,
    })

    d.scheduleReconcile(ctx)
    return outputs, nil
}
```

### Import

```go
func (d *DashboardDriver) Import(ctx restate.ObjectContext, req drivers.ImportRequest) (DashboardOutputs, error) {
    api := d.buildAPI(req.Account, req.Region)

    observed, err := restate.Run(ctx, func(rc restate.RunContext) (*ObservedState, error) {
        return api.GetDashboard(rc, req.ResourceID)
    })
    if err != nil {
        return DashboardOutputs{}, err
    }
    if observed == nil {
        return DashboardOutputs{}, restate.TerminalError(
            fmt.Errorf("dashboard %q not found in %s", req.ResourceID, req.Region), 404)
    }

    outputs := DashboardOutputs{
        DashboardArn:  observed.DashboardArn,
        DashboardName: observed.DashboardName,
    }

    mode := drivers.ImportMode(req.Observe)

    restate.Set(ctx, drivers.StateKey, &DashboardState{
        Desired:  specFromObserved(observed, req),
        Observed: *observed,
        Outputs:  outputs,
        Status:   types.StatusReady,
        Mode:     mode,
    })

    d.scheduleReconcile(ctx)
    return outputs, nil
}
```

### Delete

```go
func (d *DashboardDriver) Delete(ctx restate.ObjectContext) error {
    state, _ := restate.Get[*DashboardState](ctx, drivers.StateKey)
    if state == nil {
        return nil
    }

    api := d.buildAPI(state.Desired.Account, state.Desired.Region)

    if _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
        return restate.Void{}, api.DeleteDashboard(rc, state.Desired.DashboardName)
    }); err != nil {
        if isNotFound(err) {
            restate.ClearAll(ctx)
            return nil
        }
        return err
    }

    restate.ClearAll(ctx)
    return nil
}
```

### Reconcile

```go
func (d *DashboardDriver) Reconcile(ctx restate.ObjectContext) error {
    state, _ := restate.Get[*DashboardState](ctx, drivers.StateKey)
    if state == nil {
        return nil
    }

    api := d.buildAPI(state.Desired.Account, state.Desired.Region)

    observed, err := restate.Run(ctx, func(rc restate.RunContext) (*ObservedState, error) {
        return api.GetDashboard(rc, state.Desired.DashboardName)
    })
    if err != nil {
        return err
    }

    if observed == nil {
        state.Status = types.StatusDrifted
        state.Error = "dashboard deleted externally"
        state.Observed = ObservedState{}
        restate.Set(ctx, drivers.StateKey, state)
        d.scheduleReconcile(ctx)
        return nil
    }

    state.Observed = *observed

    if HasDrift(state.Desired, *observed) {
        state.Status = types.StatusDrifted

        if state.Mode == types.ModeManaged {
            // Auto-correct via PutDashboard (upsert idempotently restores body)
            if _, err := restate.Run(ctx, func(rc restate.RunContext) ([]ValidationMessage, error) {
                return api.PutDashboard(rc, state.Desired)
            }); err != nil {
                state.Error = err.Error()
                restate.Set(ctx, drivers.StateKey, state)
                d.scheduleReconcile(ctx)
                return nil
            }

            // Re-get after correction
            observed, err = restate.Run(ctx, func(rc restate.RunContext) (*ObservedState, error) {
                return api.GetDashboard(rc, state.Desired.DashboardName)
            })
            if err != nil || observed == nil {
                d.scheduleReconcile(ctx)
                return nil
            }
            state.Observed = *observed
            state.Status = types.StatusReady
            state.Error = ""
        }
    } else {
        state.Status = types.StatusReady
        state.Error = ""
    }

    state.Outputs = DashboardOutputs{
        DashboardArn:  observed.DashboardArn,
        DashboardName: observed.DashboardName,
    }
    restate.Set(ctx, drivers.StateKey, state)
    d.scheduleReconcile(ctx)
    return nil
}
```

### GetStatus / GetOutputs

```go
func (d *DashboardDriver) GetStatus(ctx restate.ObjectSharedContext) (types.ResourceStatus, error) {
    state, _ := restate.Get[*DashboardState](ctx, drivers.StateKey)
    if state == nil {
        return types.StatusNotFound, nil
    }
    return state.Status, nil
}

func (d *DashboardDriver) GetOutputs(ctx restate.ObjectSharedContext) (DashboardOutputs, error) {
    state, _ := restate.Get[*DashboardState](ctx, drivers.StateKey)
    if state == nil {
        return DashboardOutputs{}, nil
    }
    return state.Outputs, nil
}
```

---

## Step 7 — Provider Adapter

**File**: `internal/core/provider/dashboard_adapter.go`

```go
type DashboardAdapter struct {
    accounts   *auth.Registry
    apiFactory func(aws.Config) dashboard.DashboardAPI
}

func NewDashboardAdapterWithRegistry(accounts *auth.Registry) *DashboardAdapter {
    if accounts == nil {
        accounts = auth.LoadFromEnv()
    }
    return &DashboardAdapter{
        accounts: accounts,
        apiFactory: func(cfg aws.Config) dashboard.DashboardAPI {
            return dashboard.NewDashboardAPI(awsclient.NewCloudWatchClient(cfg))
        },
    }
}

func (a *DashboardAdapter) Kind() string        { return "Dashboard" }
func (a *DashboardAdapter) ServiceName() string { return dashboard.ServiceName }
func (a *DashboardAdapter) Scope() KeyScope     { return KeyScopeRegion }

func (a *DashboardAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
    var doc struct {
        Metadata struct{ Name string `json:"name"` } `json:"metadata"`
        Spec     struct{ Region string `json:"region"` } `json:"spec"`
    }
    if err := json.Unmarshal(resourceDoc, &doc); err != nil {
        return "", fmt.Errorf("DashboardAdapter.BuildKey: %w", err)
    }
    return JoinKey(doc.Spec.Region, doc.Metadata.Name), nil
}

func (a *DashboardAdapter) BuildImportKey(region, resourceID string) (string, error) {
    return JoinKey(region, resourceID), nil
}

func (a *DashboardAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
    return decodeSpec[dashboard.DashboardSpec](resourceDoc)
}
```

---

## Step 8 — Registry Integration

**File**: `internal/core/provider/registry.go` — add to `NewRegistry()`:

```go
// Add to the NewRegistryWithAdapters(...) call:
NewDashboardAdapterWithRegistry(accounts),
```

---

## Step 9 — Monitoring Driver Pack Entry Point

**File**: `cmd/praxis-monitoring/main.go`

```go
srv := server.NewRestate().
    Bind(restate.Reflect(loggroup.NewLogGroupDriver(cfg.Auth()))).
    Bind(restate.Reflect(metricalarm.NewMetricAlarmDriver(cfg.Auth()))).
    Bind(restate.Reflect(dashboard.NewDashboardDriver(cfg.Auth())))
```

---

## Step 10 — Docker Compose & Justfile

### Docker Compose

CloudWatch drivers are hosted in the `praxis-monitoring` service on port 9086.
See the [pack overview](CLOUDWATCH_DRIVER_PACK_OVERVIEW.md) for the docker-compose
service definition.

### Justfile

```just
test-dashboard:    go test ./internal/drivers/dashboard/...    -v -count=1 -race
```

---

## Step 11 — Unit Tests

**File**: `internal/drivers/dashboard/driver_test.go`

### Test Cases

| Test | Description |
|---|---|
| `TestProvision_CreateNew` | Creates a new dashboard with widget body |
| `TestProvision_UpdateBody` | Updates body on existing dashboard |
| `TestProvision_InvalidBody` | Terminal error on malformed dashboard body |
| `TestProvision_ValidationWarnings` | Succeeds with validation warnings logged |
| `TestProvision_InvalidParam` | Terminal error on invalid parameter |
| `TestImport_Existing` | Imports existing dashboard in managed mode |
| `TestImport_Observed` | Imports existing dashboard in observed mode |
| `TestImport_NotFound` | Returns 404 terminal error |
| `TestDelete_Success` | Deletes dashboard and clears state |
| `TestDelete_AlreadyGone` | No-op when already deleted |
| `TestDelete_NoState` | No-op when no state exists |
| `TestReconcile_NoDrift` | Status remains `Ready` |
| `TestReconcile_BodyDrift` | Detects and corrects body change |
| `TestReconcile_ExternalDeletion` | Detects external deletion, sets `Drifted` |
| `TestReconcile_ObservedMode` | Detects drift but does not correct |
| `TestGetStatus_NotFound` | Returns `StatusNotFound` for missing state |
| `TestGetOutputs_Exists` | Returns correct outputs |

**File**: `internal/drivers/dashboard/drift_test.go`

| Test | Description |
|---|---|
| `TestHasDrift_NoDrift` | Identical body → no drift |
| `TestHasDrift_BodyChanged` | Widget added → drift |
| `TestHasDrift_WhitespaceOnly` | Whitespace reformatting → no drift (JSON-semantic) |
| `TestHasDrift_KeyReordered` | JSON key reordering → no drift (JSON-semantic) |
| `TestHasDrift_NumericNormalization` | `5.0` vs `5` → no drift (JSON-semantic) |
| `TestHasDrift_WidgetRemoved` | Widget removed from body → drift |
| `TestHasDrift_PropertyChanged` | Widget property value changed → drift |
| `TestHasDrift_InvalidDesiredJSON` | Desired is not valid JSON → falls back to string comparison |

**File**: `internal/drivers/dashboard/aws_test.go`

| Test | Description |
|---|---|
| `TestIsNotFound_ResourceNotFound` | Classifies `ResourceNotFoundException` |
| `TestIsNotFound_DashboardNotFound` | Classifies `DashboardNotFoundError` |
| `TestIsDashboardInvalidInput` | Classifies `DashboardInvalidInputError` |
| `TestIsInvalidParam` | Classifies `InvalidParameterValueException` |
| `TestIsThrottled` | Classifies `ThrottlingException` |

---

## Step 12 — Integration Tests

**File**: `tests/integration/dashboard_driver_test.go`

### Prerequisites

- LocalStack with CloudWatch support
- Restate dev server
- praxis-monitoring pack running

### Test Scenarios

| Test | Description |
|---|---|
| `TestDashboard_CreateAndGet` | Create → get → verify body and ARN |
| `TestDashboard_UpdateBody` | Create → change body → verify updated widgets |
| `TestDashboard_ReplaceEntireBody` | Create → replace with completely different body → verify |
| `TestDashboard_MultipleWidgetTypes` | Create with metric, alarm, log, and text widgets → verify |
| `TestDashboard_Import` | Create externally → import → verify outputs |
| `TestDashboard_Delete` | Create → delete → verify deletion |
| `TestDashboard_DriftCorrection` | Create → externally replace body → reconcile → verify corrected |
| `TestDashboard_InvalidBody` | Attempt to create with malformed JSON → verify terminal error |
| `TestDashboard_EmptyWidgets` | Create with empty widgets array → verify (should succeed) |

---

## Dashboard-Specific Design Decisions

### 1. PutDashboard Is an Upsert

Like `PutMetricAlarm`, `PutDashboard` handles both creation and update. This makes
the Dashboard driver the simplest of the three CloudWatch drivers. The Provision
handler has a single code path regardless of whether the dashboard exists.

### 2. Body Is an Opaque JSON Blob

The driver does not parse, validate, or diff the internal structure of the dashboard
body. AWS handles validation via `DashboardValidationMessage` responses. This has
several advantages:
- **Forward-compatible**: New widget types added by AWS work immediately without
  driver changes.
- **Simple drift detection**: Compare the whole body (JSON-semantically) rather than
  diffing individual widget properties.
- **No schema coupling**: The driver does not need to track the CloudWatch dashboard
  body specification, which is complex and evolving.

The trade-off is that drift reporting is coarse-grained — the driver reports "body
changed" rather than "widget X property Y changed from A to B". This is acceptable
because dashboard changes are typically reviewed visually in the CloudWatch console.

### 3. JSON-Semantic Drift Comparison

AWS may normalize the dashboard body when storing it (reorder keys, normalize
whitespace, reformat numbers). A naive string comparison would produce false-positive
drift. The driver unmarshals both bodies and compares the resulting Go structures
using `reflect.DeepEqual`. If the desired body is not valid JSON (e.g., the user
made a typo), the driver falls back to string comparison — AWS will reject the body
on the next `PutDashboard` call anyway.

### 4. No Tag Support

CloudWatch dashboards do not support the standard `TagResource` / `UntagResource`
pattern used by log groups and alarms. While dashboards have ARNs and can be
referenced in IAM policies, tag-based management (ownership tags, cost allocation)
is not available. The driver tracks ownership through the Virtual Object key
mapping in Restate state.

### 5. Validation Messages Are Warnings

`PutDashboard` may return `DashboardValidationMessage` entries even when the
operation succeeds. These are warnings about deprecated properties, unresolved
metric references, or sub-optimal configurations. The driver logs these warnings
but does not fail the Provision operation. Only `DashboardInvalidInputError`
(returned as an error, not a validation message) triggers a terminal error.

### 6. DeleteDashboards Is Plural

The AWS API for dashboard deletion is `DeleteDashboards` (plural) — it accepts a
list of dashboard names to delete in a single call. The driver always passes a
single name. The API returns `DashboardNotFoundError` if any specified dashboard
does not exist. The driver classifies this as "already gone" and clears state.

### 7. No Secondary API Calls

Unlike the Log Group driver (which needs separate calls for retention, KMS, and
tags) and the Metric Alarm driver (which needs a separate call for tags), the
Dashboard driver's lifecycle is entirely served by three API calls:
- `PutDashboard` (create/update)
- `GetDashboard` (describe)
- `DeleteDashboards` (delete)

There are no secondary convergence calls, no tag management, and no split-API
patterns. This makes Dashboard the simplest driver in the CloudWatch pack.

---

## Checklist

### Files
- [ ] `schemas/aws/cloudwatch/dashboard.cue`
- [ ] `internal/drivers/dashboard/types.go`
- [ ] `internal/drivers/dashboard/aws.go`
- [ ] `internal/drivers/dashboard/drift.go`
- [ ] `internal/drivers/dashboard/driver.go`
- [ ] `internal/drivers/dashboard/driver_test.go`
- [ ] `internal/drivers/dashboard/aws_test.go`
- [ ] `internal/drivers/dashboard/drift_test.go`
- [ ] `internal/core/provider/dashboard_adapter.go`
- [ ] `internal/core/provider/dashboard_adapter_test.go`
- [ ] `tests/integration/dashboard_driver_test.go`

### Modifications
- [ ] `cmd/praxis-monitoring/main.go` — Bind DashboardDriver
- [ ] `internal/core/provider/registry.go` — Register adapter
- [ ] `justfile` — Add `test-dashboard` target
