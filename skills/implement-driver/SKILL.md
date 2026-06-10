# Implement Driver

**Description**: Step-by-step procedure to implement a new AWS resource driver in Praxis.

**When to Use**: Adding a new AWS resource type (e.g., DynamoDB Table, ECS Cluster, KMS Key).

**Prerequisites**:
- Read [docs/DRIVERS.md](../../docs/DRIVERS.md) for the driver model
- Identify the AWS SDK v2 package for the resource
- Know the resource's key scope (Global, Region, or Custom)

---

## Steps

### 1. Create CUE Schema

File: `schemas/aws/{resource}/{resource}.cue`

```cue
package {resource}

#{Resource}: {
    apiVersion: "praxis.io/v1"
    kind:       "{Resource}"

    metadata: {
        name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9._-]{0,254}$"
        labels?: [string]: string
    }

    spec: {
        region: string
        // Add resource-specific fields
        // Use *default for optional fields: versioning: bool | *false
        // Use regex for validated fields: name: string & =~"^[a-z]+"
        tags?: [string]: string
    }

    outputs?: {
        arn:        string
        resourceId: string
        // Add resource-specific outputs
    }
}
```

**Conventions**:
- Immutable fields first, then mutable
- Regex validation on names
- `*default` for optional fields with defaults
- Outputs section is fully optional

### 2. Create Types

File: `internal/drivers/{resource}/types.go`

```go
package {resource}

import "github.com/shirvan/praxis/pkg/types"

const ServiceName = "{Resource}"

type {Resource}Spec struct {
    Account string            `json:"account,omitempty"`
    Region  string            `json:"region"`
    // Resource-specific fields matching CUE schema spec
    Tags    map[string]string `json:"tags,omitempty"`
}

type {Resource}Outputs struct {
    ARN        string `json:"arn"`
    ResourceId string `json:"resourceId"`
    // Resource-specific outputs
}

type ObservedState struct {
    // Fields that can be observed from AWS Describe API
    Tags map[string]string `json:"tags,omitempty"`
}

type {Resource}State struct {
    Desired            {Resource}Spec       `json:"desired"`
    Observed           ObservedState        `json:"observed"`
    Outputs            {Resource}Outputs    `json:"outputs"`
    Status             types.ResourceStatus `json:"status"`
    Mode               types.Mode           `json:"mode"`
    Error              string               `json:"error,omitempty"`
    Generation         int64                `json:"generation"`
    LastReconcile      string               `json:"lastReconcile,omitempty"`
    ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
```

### 3. Create AWS API Wrapper

File: `internal/drivers/{resource}/aws.go`

```go
package {resource}

import (
    "context"
    "strings"
    awssdk "github.com/aws/aws-sdk-go-v2/service/{awsservice}"
    "github.com/shirvan/praxis/internal/infra/ratelimit"
)

type {Resource}API interface {
    Create{Resource}(ctx context.Context, spec {Resource}Spec) (string, error)
    Describe{Resource}(ctx context.Context, id string) (ObservedState, error)
    Update{Resource}(ctx context.Context, id string, spec {Resource}Spec) error
    Delete{Resource}(ctx context.Context, id string) error
    // FindByManagedKey if resource doesn't support direct name lookup
}

type real{Resource}API struct {
    client  *awssdk.Client
    limiter *ratelimit.Limiter
}

func New{Resource}API(client *awssdk.Client) {Resource}API {
    return &real{Resource}API{
        client:  client,
        limiter: ratelimit.New("{resource}", 20, 5), // adjust limits
    }
}

// Error classifiers — check typed AWS errors AND string fallback
func IsNotFound(err error) bool {
    if err == nil { return false }
    // Check typed: var nfe *awstypes.{NotFoundException}
    // String fallback: strings.Contains(err.Error(), "NotFound")
    return false
}

func IsConflict(err error) bool { /* similar */ return false }
func IsInvalidParam(err error) bool { /* similar */ return false }
```

**Critical**: Error classifiers MUST check both typed AWS errors and string-based fallback (Restate wraps errors, losing type info).

### 4. Create Drift Detection

File: `internal/drivers/{resource}/drift.go`

```go
package {resource}

type FieldDiffEntry struct {
    Path     string
    OldValue any
    NewValue any
}

func HasDrift(desired {Resource}Spec, observed ObservedState) bool {
    // Compare MUTABLE fields only (not immutable like region)
    // Filter praxis:* tags before comparing
    return false
}

func ComputeFieldDiffs(desired {Resource}Spec, observed ObservedState) []FieldDiffEntry {
    var diffs []FieldDiffEntry
    // For each mutable field, if desired != observed, add diff
    // For immutable fields that differ, mark as "(immutable, requires replacement)"
    return diffs
}
```

### 5. Create Driver (Virtual Object)

File: `internal/drivers/{resource}/driver.go`

```go
package {resource}

import (
    restate "github.com/restatedev/sdk-go"
    "github.com/shirvan/praxis/internal/drivers"
    "github.com/shirvan/praxis/pkg/types"
)

type {Resource}Driver struct {
    apiFactory func(account string) ({Resource}API, error)
}

func New{Resource}Driver(auth /* auth registry */) *{Resource}Driver {
    return &{Resource}Driver{
        apiFactory: func(account string) ({Resource}API, error) {
            // Resolve AWS config from auth registry
            // Return New{Resource}API(client)
        },
    }
}

func (d *{Resource}Driver) ServiceName() string { return ServiceName }

// Provision — Exclusive handler: create or converge
func (d *{Resource}Driver) Provision(ctx restate.ObjectContext, spec {Resource}Spec) ({Resource}Outputs, error) {
    state := drivers.GetState[{Resource}State](ctx)
    api, err := d.apiFactory(spec.Account)
    // 1. Check if exists (Describe)
    // 2. If not found → Create
    // 3. If found → Converge mutable fields
    // 4. Tag with praxis:managed-key
    // 5. Update state (desired, observed, outputs, status=Ready)
    // 6. Schedule reconciliation
    // 7. Return outputs
}

// Import — Exclusive handler: adopt existing resource
func (d *{Resource}Driver) Import(ctx restate.ObjectContext, ref types.ImportRef) ({Resource}Outputs, error) {
    // 1. Describe resource by ID
    // 2. Capture observed as both desired AND observed
    // 3. Set mode=Managed (or Observed if ref says so)
    // 4. Schedule reconciliation
}

// Delete — Exclusive handler
func (d *{Resource}Driver) Delete(ctx restate.ObjectContext) error {
    // 1. Load state
    // 2. Delete from AWS
    // 3. Set status=Deleted
    // 4. Do NOT schedule reconciliation
}

// Reconcile — Exclusive handler: drift detection + correction
func (d *{Resource}Driver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
    // 1. Load state
    // 2. Describe resource (check for external deletion)
    // 3. HasDrift(desired, observed)
    // 4. If managed: correct drift
    // 5. If observed: report only
    // 6. Reschedule timer
    // 7. Emit drift events via drivers.ReportDriftEvent()
}

// GetStatus — Shared handler (concurrent reads)
func (d *{Resource}Driver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
    state := drivers.GetState[{Resource}State](ctx)
    return types.StatusResponse{Status: state.Status, Error: state.Error, Mode: state.Mode}, nil
}

// GetOutputs — Shared handler
func (d *{Resource}Driver) GetOutputs(ctx restate.ObjectSharedContext) ({Resource}Outputs, error) {
    state := drivers.GetState[{Resource}State](ctx)
    return state.Outputs, nil
}
```

### 6. Wire Into Binary

Add the driver to the appropriate `cmd/praxis-{domain}/main.go`:

```go
Bind(restate.Reflect({resource}.New{Resource}Driver(cfg.Auth())))
```

### 7. Create Provider Adapter

See the [add-adapter skill](../add-adapter/SKILL.md) for full procedure.

### 8. Register in Docker Compose

Add or update the service in `docker-compose.yaml` if creating a new pack.

---

## Verification

1. `go build ./internal/drivers/{resource}/...` — compiles
2. `go test ./internal/drivers/{resource}/... -v -count=1` — tests pass
3. `go vet ./internal/drivers/{resource}/...` — no issues
4. Check that CUE schema validates: `cue vet schemas/aws/{resource}/{resource}.cue`

## Common Pitfalls

1. **Error classification outside restate.Run()**: MUST classify inside the callback
2. **Forgetting string fallback classifiers**: Restate wraps errors, losing type info
3. **Scheduling reconciliation after delete**: Never do this
4. **Not filtering praxis:* tags**: Will cause false drift positives
5. **Missing Account field in Spec**: Injected at dispatch time, must be in struct
6. **Non-atomic state updates**: Use single `restate.Set(ctx, drivers.StateKey, state)`

## Reference Implementations

Good examples to follow:
- **Simple**: `internal/drivers/s3/` (Global scope, straightforward CRUD)
- **Region-scoped**: `internal/drivers/vpc/` (Region scope, multiple mutable fields)
- **Custom-scoped**: `internal/drivers/sg/` (Custom scope, complex normalization)
