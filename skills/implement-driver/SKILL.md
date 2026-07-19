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
    apiVersion: "praxis.io/alpha"
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

```

Do not define a resource-local lifecycle state for new drivers. The generic
kernel persists `kernel.State[{Resource}Spec, {Resource}Outputs, ObservedState]`
as a versioned envelope.

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

### 5. Create Typed Operations and Descriptor

File: `internal/drivers/{resource}/generic.go`

```go
package {resource}

import (
    restate "github.com/restatedev/sdk-go"
    "github.com/shirvan/praxis/internal/core/authservice"
    "github.com/shirvan/praxis/internal/drivers"
    "github.com/shirvan/praxis/internal/drivers/kernel"
    "github.com/shirvan/praxis/pkg/types"
)

type operations struct { /* auth + API factory */ }

func (o *operations) Observe(ctx restate.ObjectContext, desired {Resource}Spec, outputs {Resource}Outputs) (kernel.Observation[ObservedState], error) { /* Describe */ }
func (o *operations) Create(ctx restate.ObjectContext, desired {Resource}Spec) (kernel.CreateResult[{Resource}Outputs], error) { /* Create + wait */ }
func (o *operations) Converge(ctx restate.ObjectContext, desired {Resource}Spec, observed ObservedState) error { /* mutable fields */ }
func (o *operations) Delete(ctx restate.ObjectContext, desired {Resource}Spec, outputs {Resource}Outputs) error { /* idempotent delete */ }
func (o *operations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[ObservedState], error) { /* lookup */ }

func NewGeneric{Resource}Driver(auth authservice.AuthClient) *kernel.Driver[{Resource}Spec, {Resource}Outputs, ObservedState] {
    ops := &operations{/* configure auth and API factory */}
    return kernel.MustNew(kernel.Descriptor[{Resource}Spec, {Resource}Outputs, ObservedState]{
        ServiceName: ServiceName,
        Capabilities: kernel.Capabilities{
            Declared: true, Import: true, ObservedMode: true,
            Delete: true, ManagedDriftCorrection: true,
        },
        Operations: ops,
        Prepare: prepareSpec,
        Validate: validateSpec,
        DesiredFromObserved: desiredFromObserved,
        OutputsFromObserved: outputsFromObserved,
        HasDrift: HasDrift,
    })
}
```

Use `drivers.RunAWS` for provider calls. Delete must fail clearly when provider
prerequisites are not satisfied; do not add a cleanup handler. For
server-defaulted spec fields, declare
`LateInitialization: true` and supply the pure `LateInitialize` descriptor hook.

### 6. Wire Into Binary

Add the driver to the appropriate `internal/driverpack/{domain}/definitions.go`:

```go
genericbinding.Reflect({resource}.NewGeneric{Resource}Driver(auth), rp)
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
6. **Resource-local lifecycle handlers/state**: New drivers use the generic kernel
7. **Compatibility paths**: Do not add state upgrades, dual reads, aliases, or migrations without owner approval

## Reference Implementations

Good generic examples to follow:
- **Simple/upsert**: `internal/drivers/dashboard/generic.go`
- **Composite**: `internal/drivers/kmskey/generic.go`
- **Async/waiter**: `internal/drivers/dynamodbtable/generic.go`
- **Late initialization/safe delete**: `internal/drivers/s3/generic.go`
- **Generic driver architecture**: `docs/GENERIC_DRIVERS.md`
