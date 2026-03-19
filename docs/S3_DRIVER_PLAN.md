# S3 Bucket Driver — Implementation Plan

> Target: A Restate Virtual Object driver that manages S3 buckets, providing full
> lifecycle management including creation, configuration (versioning, encryption,
> tags), import, deletion, drift detection, and drift correction.
>
> Key scope: `KeyScopeGlobal` — key format is the bucket name itself, which is
> globally unique across all AWS accounts. No region or VPC prefix is needed.

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
15. [S3-Specific Design Decisions](#s3-specific-design-decisions)
16. [Checklist](#checklist)

---

## 1. Overview & Scope

The S3 driver manages the lifecycle of S3 **buckets** only. Object management,
replication rules, lifecycle policies, bucket policies, and CORS configuration are
out of scope and would be implemented as separate features or extensions. This plan
focuses exclusively on bucket creation, configuration (versioning, encryption, ACL,
tags), import, deletion, and drift reconciliation.

The driver follows the established Virtual Object contract:

| Handler | Context | Purpose |
|---|---|---|
| `Provision` | `ObjectContext` (exclusive) | Create or converge a bucket |
| `Import` | `ObjectContext` (exclusive) | Adopt an existing bucket |
| `Delete` | `ObjectContext` (exclusive) | Remove an empty bucket |
| `Reconcile` | `ObjectContext` (exclusive) | Detect/correct drift (report-only for Observed mode) |
| `GetStatus` | `ObjectSharedContext` (shared) | Return current status |
| `GetOutputs` | `ObjectSharedContext` (shared) | Return bucket outputs |

### Mutable vs Immutable Attributes

| Attribute | Mutability | Notes |
|---|---|---|
| `bucketName` | Immutable | The Virtual Object key; globally unique across AWS |
| `region` | Immutable | AWS does not allow moving a bucket between regions |
| `versioning` | Mutable | Toggled via `PutBucketVersioning` (Enabled/Suspended) |
| `encryption` | Mutable | Configured via `PutBucketEncryption` |
| `acl` | Mutable | Configured at create time; drift detection NOT implemented (see Design Decisions) |
| `tags` | Mutable | Full replace via `PutBucketTagging` |

---

## 2. Key Strategy

### Key Scope: `KeyScopeGlobal`

S3 bucket names are globally unique across all AWS accounts worldwide. This makes
them the simplest key strategy:

```
bucketName
```

No region prefix, no VPC prefix, no metadata.name indirection. The bucket name
**is** the key.

### BuildKey and BuildImportKey — Same Key

- **`BuildKey(resourceDoc)`**: Extracts `metadata.name` from the resource document
  and returns it as-is. The CUE schema maps `metadata.name` to the bucket name.

- **`BuildImportKey(region, resourceID)`**: Returns `resourceID` as-is. For S3, the
  `resourceID` is the bucket name, which is the same as `BuildKey` would produce.

This is unique among drivers: S3 is the only driver where `BuildKey` and
`BuildImportKey` produce the same key for the same underlying resource. SG produces
`vpcId~groupName` vs `sg-id`, and EC2 produces `region~name` vs `region~instanceId`.

### Key Validation

The bucket name is validated via `ValidateKeyPart()` at BuildKey time to ensure it's
non-empty and contains no delimiters.

---

## 3. File Inventory

All files below exist in the repository (✓ = implemented):

```
✓ schemas/aws/s3/s3.cue                    — CUE schema for S3Bucket resource
✓ internal/drivers/s3/types.go              — Spec, Outputs, ObservedState, State structs
✓ internal/drivers/s3/aws.go               — S3API interface + realS3API implementation
✓ internal/drivers/s3/drift.go             — HasDrift, ComputeFieldDiffs, tagsMatch
✓ internal/drivers/s3/driver.go            — S3BucketDriver Virtual Object
✓ internal/drivers/s3/driver_test.go       — Unit tests: specFromObserved, ServiceName
✓ internal/drivers/s3/aws_test.go          — Unit tests: error classification (IsBucketNotEmpty)
✓ internal/drivers/s3/drift_test.go        — Unit tests: drift detection, field diffs
✓ internal/core/provider/s3_adapter.go     — S3Adapter implementing provider.Adapter
✓ internal/core/provider/registry.go       — (modified) registers S3Adapter
✓ cmd/praxis-storage/main.go               — Storage driver pack entry point (S3 bound here)
✓ cmd/praxis-storage/Dockerfile            — Multi-stage Docker build
✓ docker-compose.yaml                      — (modified) praxis-storage service on port 9081
✓ justfile                                 — (modified) storage build/test/register targets
✓ tests/integration/s3_driver_test.go      — Integration tests (Testcontainers + LocalStack)
```

---

## Step 1 — CUE Schema

**File**: `schemas/aws/s3/s3.cue`

Defines the shape of an `S3Bucket` resource document. The template engine validates
user templates against this schema before dispatch.

```cue
package s3

#S3Bucket: {
    apiVersion: "praxis.io/v1"
    kind:       "S3Bucket"

    metadata: {
        name: string & =~"^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$"
        labels: [string]: string
    }

    spec: {
        region:     string
        versioning: bool | *true
        acl:        "private" | "public-read" | *"private"
        encryption: {
            enabled:   bool | *true
            algorithm: *"AES256" | "aws:kms"
        }
        tags: [string]: string
    }

    outputs?: {
        arn:        string
        bucketName: string
        region:     string
        domainName: string
    }
}
```

### Key Design Decisions

- **metadata.name IS the bucket name**: Unlike other drivers where `metadata.name`
  is a logical identifier and `spec` contains the AWS resource name, S3 uses
  `metadata.name` directly as the bucket name. The adapter's `decodeSpec` extracts
  `metadata.name` and sets it as `BucketName` in the spec. This works because bucket
  names are globally unique — they are both the logical identity and the AWS identity.

- **Default versioning enabled**: `versioning: bool | *true` — defaults to enabled
  because it protects against accidental deletion. This is an opinionated best
  practice default.

- **Default encryption enabled with AES256**: `enabled: *true`, `algorithm: *"AES256"` —
  defaults match AWS's own SSE-S3 default since January 2023. Users can override to
  `"aws:kms"` for SSE-KMS.

- **ACL limited to two values**: Only `"private"` and `"public-read"` are supported.
  AWS's public access block settings make most other ACLs obsolete. Default is
  `"private"`.

- **S3 naming validation**: The regex `^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$` enforces
  S3's naming rules: 3-63 characters, lowercase, numbers, hyphens, and periods. The
  schema rejects uppercase characters at validation time.

---

## Step 2 — Driver Types

**File**: `internal/drivers/s3/types.go`

### S3BucketSpec

```go
type S3BucketSpec struct {
    Account    string            `json:"account,omitempty"`
    BucketName string            `json:"bucketName"`
    Region     string            `json:"region"`
    Versioning bool              `json:"versioning"`
    Encryption EncryptionSpec    `json:"encryption"`
    ACL        string            `json:"acl"`
    Tags       map[string]string `json:"tags"`
}

type EncryptionSpec struct {
    Enabled   bool   `json:"enabled"`
    Algorithm string `json:"algorithm"`
}
```

The spec maps 1:1 to the CUE schema fields. The `Account` field is injected at
dispatch time (by the adapter/pipeline), not from the template.

### S3BucketOutputs

```go
type S3BucketOutputs struct {
    ARN        string `json:"arn"`
    BucketName string `json:"bucketName"`
    Region     string `json:"region"`
    DomainName string `json:"domainName"`
}
```

Outputs are referenced by dependent resources via output expressions:
`${ resources.bucket.outputs.arn }`, `${ resources.bucket.outputs.domainName }`.

### ObservedState

```go
type ObservedState struct {
    BucketName       string            `json:"bucketName"`
    Region           string            `json:"region"`
    VersioningStatus string            `json:"versioningStatus"` // "Enabled" | "Suspended" | ""
    EncryptionAlgo   string            `json:"encryptionAlgo"`
    Tags             map[string]string `json:"tags"`
}
```

Unlike the SG driver which uses a specialized `NormalizedRule` for observed state,
the S3 `ObservedState` uses simple scalar fields. This reflects the simpler drift
model: S3 bucket attributes are key-value comparisons, not set-based comparisons.

### S3BucketState

```go
type S3BucketState struct {
    Desired            S3BucketSpec     `json:"desired"`
    Observed           ObservedState    `json:"observed"`
    Outputs            S3BucketOutputs  `json:"outputs"`
    Status             types.ResourceStatus `json:"status"`
    Mode               types.Mode       `json:"mode"`
    Error              string           `json:"error,omitempty"`
    Generation         int64            `json:"generation"`
    LastReconcile      string           `json:"lastReconcile,omitempty"`
    ReconcileScheduled bool             `json:"reconcileScheduled"`
}
```

All fields are documented with their purpose. The `Generation` counter enables
cheap conflict detection and status correlation. The `ReconcileScheduled` flag
prevents fan-out of delayed messages.

---

## Step 3 — AWS API Abstraction Layer

**File**: `internal/drivers/s3/aws.go`

### S3API Interface

```go
type S3API interface {
    HeadBucket(ctx context.Context, name string) error
    CreateBucket(ctx context.Context, name, region string) error
    ConfigureBucket(ctx context.Context, spec S3BucketSpec) error
    DescribeBucket(ctx context.Context, name string) (ObservedState, error)
    DeleteBucket(ctx context.Context, name string) error
}
```

The S3 interface is simpler than the SG interface (5 methods vs 9) because S3 bucket
configuration uses a single `ConfigureBucket` call that handles versioning,
encryption, and tags in sequence. SG needs separate authorize/revoke calls per
direction.

### Key Architectural Decisions

**`context.Context`, not `restate.RunContext`**: Same pattern as all drivers. The
AWS layer knows nothing about Restate. The driver wraps every S3API call inside
`restate.Run()`.

**Rate limiting**: `ratelimit.New("s3", 100, 20)` — 100 RPS sustained, burst of 20.
Higher than EC2/SG because S3 API limits are more generous.

### CreateBucket — us-east-1 Quirk

```go
if region != "us-east-1" {
    input.CreateBucketConfiguration = &s3types.CreateBucketConfiguration{
        LocationConstraint: s3types.BucketLocationConstraint(region),
    }
}
```

AWS requires `LocationConstraint` for all regions **except** us-east-1. Specifying
us-east-1 as the LocationConstraint returns an error. This is a longstanding AWS
API quirk that must be handled at the SDK layer.

### ConfigureBucket — Convergent Application

`ConfigureBucket` applies three configurations in sequence:

1. **Versioning**: `PutBucketVersioning` with `Enabled` or `Suspended`.
2. **Encryption**: `PutBucketEncryption` with `AES256` or `aws:kms` (only if
   `spec.Encryption.Enabled` is true).
3. **Tags**: `PutBucketTagging` with the full tag set (only if tags exist).

This runs on both create and update paths precisely because it's convergent —
applying the same configuration twice produces the same result.

### DescribeBucket — Multi-Probe Approach

`DescribeBucket` queries four separate S3 APIs to build the `ObservedState`:

1. **`HeadBucket`**: Confirms bucket existence.
2. **`GetBucketLocation`**: Returns the region (empty string for us-east-1,
   normalized to `"us-east-1"`).
3. **`GetBucketVersioning`**: Returns versioning status.
4. **`GetBucketEncryption`**: Returns encryption algorithm. Handles
   `ServerSideEncryptionConfigurationNotFoundError` gracefully (no encryption
   configured → empty string).
5. **`GetBucketTagging`**: Returns tags. Handles `NoSuchTagSet` gracefully (no tags
   → empty map).

AWS quirks handled:
- `GetBucketLocation` returns empty string for us-east-1 → normalized to `"us-east-1"`.
- `GetBucketEncryption` returns error (not empty) for buckets with no explicit
  encryption configuration.
- `GetBucketTagging` returns error (not empty) for buckets with no tags.

### DeleteBucket — Safety by Design

`DeleteBucket` does NOT auto-empty buckets. It calls `s3:DeleteBucket` directly,
which fails if the bucket contains objects. This is an intentional safety decision:
automatically emptying a bucket is destructive and can hide data-loss events behind
routine infrastructure operations.

### Error Classification

Three error classifiers with string fallback for Restate-wrapped errors:

| Function | Error Type(s) | Semantics |
|---|---|---|
| `IsNotFound` | `NoSuchKey`, `NoSuchBucket`, `NotFound` | Bucket doesn't exist |
| `IsBucketNotEmpty` | `BucketNotEmpty` | Bucket has objects (terminal for Delete) |
| `IsConflict` | `BucketAlreadyOwnedByYou`, `BucketAlreadyExists` | Ownership conflict |

**`IsNotFound` — Triple typed check + string fallback**: Uses `errors.As()` against
three S3 error types (`NoSuchKey`, `NoSuchBucket`, `NotFound`) plus the generic
`smithy.APIError` with code `"NotFound"` (returned by `HeadBucket`). Falls back to
string matching for `"NoSuchBucket"`, `"NoSuchKey"`, and `"api error NotFound"`.

**`IsBucketNotEmpty` — String fallback for Restate-wrapped errors**: Matches
`"BucketNotEmpty"`, `"bucket you tried to delete is not empty"`, and
`"You must delete all versions in the bucket"` to handle errors that have been
through the `restate.Run()` panic/recovery boundary.

**`IsConflict` — No string fallback needed**: Uses only typed error checks
(`BucketAlreadyOwnedByYou`, `BucketAlreadyExists`). These errors only occur during
`CreateBucket`, which is classified inside the `restate.Run()` callback before the
structured type is lost.

---

## Step 4 — Drift Detection

**File**: `internal/drivers/s3/drift.go`

S3 drift detection is simpler than SG because bucket attributes are scalar
comparisons, not set-based rule comparisons. There is no `Normalize` function or
`NormalizedRule` type.

### HasDrift

```go
func HasDrift(desired S3BucketSpec, observed ObservedState) bool
```

Compares three attributes:

1. **Versioning**: Desired `true` → expects `"Enabled"`. Desired `false` → expects
   `"Suspended"` or `""` (empty = never configured, treated as disabled).
2. **Encryption**: Only checked when `desired.Encryption.Enabled` is true. Compares
   `desired.Encryption.Algorithm` against `observed.EncryptionAlgo`.
3. **Tags**: Uses `tagsMatch()` for order-independent map comparison. `nil` and
   empty maps are treated as equivalent.

**What is NOT checked**: ACL is not compared because `GetBucketAcl` returns a
complex grant structure that doesn't map cleanly to the simple `"private"` /
`"public-read"` string. Region is not checked because it's immutable.

### ComputeFieldDiffs

```go
func ComputeFieldDiffs(desired S3BucketSpec, observed ObservedState) []FieldDiffEntry
```

Produces field-level diffs for the plan renderer. Returns entries for:
- `spec.versioning`: Shows `"Suspended"` → `"Enabled"` (or reverse).
- `spec.encryption.algorithm`: Shows algorithm changes.
- `tags.<key>`: Shows added, changed, or removed tags individually.

Returns `nil` if there is no drift (used by the adapter's `Plan()` to distinguish
`OpNoOp` from `OpUpdate`).

### FieldDiffEntry

```go
type FieldDiffEntry struct {
    Path     string
    OldValue any
    NewValue any
}
```

The provider-specific diff unit. `OldValue` is `nil` for new fields, `NewValue` is
`nil` for removed fields.

### tagsMatch

```go
func tagsMatch(a, b map[string]string) bool
```

Semantic equality for tag maps. Treats `nil` and empty maps as equivalent.

---

## Step 5 — Driver Implementation

**File**: `internal/drivers/s3/driver.go`

### Service Registration

```go
const ServiceName = "S3Bucket"
```

The driver is registered as a Restate Virtual Object named `"S3Bucket"`. Each
instance is keyed by the bucket name.

### Constructor Pattern

```go
func NewS3BucketDriver(accounts *auth.Registry) *S3BucketDriver
func NewS3BucketDriverWithFactory(accounts *auth.Registry, factory func(aws.Config) S3API) *S3BucketDriver
```

Same pattern as all drivers. `NewS3BucketDriver` for production,
`NewS3BucketDriverWithFactory` for tests. Both fall back to `auth.LoadFromEnv()` and
default factory if nil.

### Provision Handler

The Provision handler is convergent — it handles both create and update in a single
code path:

1. **Input validation**: `bucketName` and `region` must be non-empty.
   Returns `TerminalError(400)`.

2. **Load current state**: Reads state, sets status to `Provisioning`, increments
   generation.

3. **Head check**: Calls `HeadBucket` via `restate.Run()` to check if the bucket
   already exists.
   - 404 → bucket doesn't exist, proceed to create.
   - 200 → bucket exists, skip creation, proceed to configure.
   - Conflict → terminal error (bucket owned by another account).

4. **Create bucket**: If bucket doesn't exist, calls `CreateBucket`.
   - `IsConflict` from `CreateBucket` is absorbed (race condition between
     `HeadBucket` and `CreateBucket` — another request created it mid-flight;
     proceed to configure).

5. **Configure bucket**: Always runs `ConfigureBucket` (versioning + encryption +
   tags). This makes Provision convergent — same spec applied twice produces the same
   result, and changed specs are applied correctly.

6. **Build outputs**: Synthesizes ARN (`arn:aws:s3:::{name}`), bucket name, region,
   and domain name (`{name}.s3.{region}.amazonaws.com`).

7. **Commit state**: Sets status to `Ready`, schedules reconciliation.

### Import Handler

1. Describes the bucket by `ref.ResourceID` (the bucket name).
2. Synthesizes a spec via `specFromObserved()` — maps observed versioning status,
   encryption algorithm, and tags back to spec types.
3. Commits state with matching desired and observed, ensuring first reconcile sees
   no drift.
4. Schedules reconciliation.

**`specFromObserved(name string, obs ObservedState) S3BucketSpec`**:
- `VersioningStatus == "Enabled"` → `Versioning: true`, else `false`.
- Empty `EncryptionAlgo` defaults to `"AES256"` (AWS default since Jan 2023).
- Tags are passed through directly.

### Delete Handler

1. Sets status to `Deleting`.
2. Calls `api.DeleteBucket` inside `restate.Run()`.
3. **Error classification inside the callback**:
   - `IsBucketNotEmpty` → `TerminalError(409)` with clear message: "bucket is not
     empty — empty it before deleting."
   - `IsNotFound` → silent success (already gone).
   - Other errors → returned for Restate retry.
4. On success, replaces state with minimal `S3BucketState{Status: StatusDeleted}`.
5. **Delete never schedules reconciliation** — this is an explicit invariant.

### Reconcile Handler

Same structure as all drivers:

1. Clears `ReconcileScheduled` flag.
2. Skips if status is not `Ready` or `Error`.
3. Describes current AWS state.
4. **External deletion (404)** → Error status, does NOT re-provision automatically.
5. **Error status** → Read-only describe, no correction. Reports drift but does not
   fix. Re-schedules.
6. **Ready + Managed + drift** → Corrects by calling `ConfigureBucket`. Reports
   `{Drift: true, Correcting: true}`. Re-schedules.
7. **Ready + Observed + drift** → Reports only. `{Drift: true, Correcting: false}`.
   Re-schedules.
8. **No drift** → Reports clean. Re-schedules.

**Drift correction is atomic for S3**: Unlike the SG driver which has a multi-step
add-before-remove rule application, the S3 driver corrects all drift in a single
`ConfigureBucket` call. This is possible because S3 configuration APIs are
idempotent and convergent — there's no ordering concern.

### GetStatus / GetOutputs (Shared Handlers)

- `GetStatus` → `types.StatusResponse` (Status, Mode, Generation, Error)
- `GetOutputs` → `S3BucketOutputs` (ARN, BucketName, Region, DomainName)

### scheduleReconcile

Same pattern as all drivers. Uses `ReconcileScheduled` flag for dedup. Sends
delayed self-invocation with `drivers.ReconcileInterval` (5 minutes). Detailed
comment in the source explains why delayed one-way messages are used instead of
`Sleep`:
1. Handler completes immediately (releases exclusive lock).
2. Other requests can be processed during the delay.
3. No long-running invocation ties up a service deployment version.

### apiForAccount

Resolves AWS config from the auth registry and creates an S3API via the factory.
Returns only the API (no region string, unlike the SG driver which needs region for
ARN synthesis — S3 ARNs don't contain a region).

---

## Step 6 — Provider Adapter

**File**: `internal/core/provider/s3_adapter.go`

### S3Adapter

```go
type S3Adapter struct {
    auth              *auth.Registry
    staticPlanningAPI s3.S3API
    apiFactory        func(aws.Config) s3.S3API
}
```

### Constructors

- `NewS3Adapter()`: Production, loads auth from env.
- `NewS3AdapterWithRegistry(accounts)`: Production, explicit auth.
- `NewS3AdapterWithAPI(api)`: Test-only, injects a fixed planning API.

### Methods

**`Scope() KeyScope`** → `KeyScopeGlobal`

**`Kind() string`** → `"S3Bucket"`

**`ServiceName() string`** → `"S3Bucket"`

**`BuildKey(resourceDoc json.RawMessage) (string, error)`**:
Decodes the resource document, extracts the bucket name (from `metadata.name` via
`decodeSpec`), validates via `ValidateKeyPart()`, and returns the bucket name.

**`BuildImportKey(region, resourceID string) (string, error)`**:
Returns `resourceID` directly (the bucket name). For S3, this produces the same key
as `BuildKey`, which is unique among drivers.

**`DecodeSpec(resourceDoc json.RawMessage) (any, error)`**:
Decodes and returns `s3.S3BucketSpec`. The `decodeSpec` helper:
- Extracts `metadata.name` as `BucketName`.
- Validates that both `metadata.name` and `spec.region` are non-empty.
- Clears the `Account` field (injected at dispatch time).

**`Provision(ctx, key, account, spec) (ProvisionInvocation, error)`**:
Dispatches to the S3 Virtual Object's Provision handler.

**`Delete(ctx, key) (DeleteInvocation, error)`**:
Dispatches to the S3 Virtual Object's Delete handler.

**`NormalizeOutputs(raw any) (map[string]any, error)`**:
Converts `s3.S3BucketOutputs` to a generic map with keys: `arn`, `bucketName`,
`region`, `domainName`.

**`Import(ctx, key, account, ref) (types.ResourceStatus, map[string]any, error)`**:
Dispatches to the S3 Virtual Object's Import handler.

**`Plan(ctx, key, account, desiredSpec) (types.DiffOperation, []types.FieldDiff, error)`**:
Queries AWS via `DescribeBucket` to determine current state:

1. Calls `DescribeBucket(bucketName)` via `restate.Run()`.
2. If not found → returns `OpCreate` with field diffs synthesized from the spec.
3. If found → calls `s3.ComputeFieldDiffs()` to compare desired vs observed.
4. If no diffs → returns `OpNoOp`.
5. If diffs → returns `OpUpdate` with the field diffs.

The describe call wraps not-found as a successful journal entry
(`describePlanResult{Found: false}`) rather than an error, preventing Restate from
retrying a normally expected outcome.

---

## Step 7 — Registry Integration

**File**: `internal/core/provider/registry.go` (modified)

```go
NewS3AdapterWithRegistry(accounts),
```

Registered alongside all other adapters in `NewRegistry()`.

---

## Step 8 — Storage Driver Pack Entry Point & Dockerfile

### Entry Point

**File**: `cmd/praxis-storage/main.go`

The S3 driver is added to the **storage** driver pack. The Restate SDK supports binding multiple Virtual Objects to one server via chained `.Bind()` calls, so the storage pack hosts all storage-related drivers (S3, and in the future RDS, DynamoDB, SQS, SNS).

```go
func main() {
    cfg := config.Load()

    srv := server.NewRestate().
        Bind(restate.Reflect(s3.NewS3BucketDriver(cfg.Auth())))

    slog.Info("starting storage driver pack", "addr", cfg.ListenAddr)
    if err := srv.Start(context.Background(), cfg.ListenAddr); err != nil {
        slog.Error("storage driver pack exited", "err", err.Error())
        os.Exit(1)
    }
}
```

Standard pattern: load config, create driver, bind to Restate, start server.

### Dockerfile

**File**: `cmd/praxis-storage/Dockerfile`

```dockerfile
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /praxis-storage ./cmd/praxis-storage

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /praxis-storage /praxis-storage
ENTRYPOINT ["/praxis-storage"]
```

Multi-stage build: Go 1.25 Alpine for compilation, distroless for runtime.
`CGO_ENABLED=0` for a fully static binary. `nonroot` base image for security.

---

## Step 9 — Docker Compose & Justfile

### Docker Compose

**File**: `docker-compose.yaml` (modified)

```yaml
praxis-storage:
    build:
      context: .
      dockerfile: cmd/praxis-storage/Dockerfile
    container_name: praxis-storage
    env_file:
      - .env
    depends_on:
      restate:
        condition: service_healthy
      localstack:
        condition: service_healthy
    ports:
      - "9081:9080"
    environment:
      - PRAXIS_LISTEN_ADDR=0.0.0.0:9080
```

Listens on container port 9080, mapped to host port 9081 (the first driver pack port;
Network is 9082, Core is 9083, Compute is 9084). Depends on both Restate and LocalStack being healthy.

### Justfile Targets

| Target | Command |
|---|---|
| `logs-storage` | `docker compose logs -f praxis-storage` |
| `test-s3` | `go test ./internal/drivers/s3/... -v -count=1 -race` |
| `ls-s3` | `aws --endpoint-url=http://localhost:4566 s3 ls` |
| `build` (shared) | `go build -o bin/praxis-storage ./cmd/praxis-storage` |
| `register` (shared) | Registers storage pack with Restate at `http://praxis-storage:9080` |
| `up` (shared) | `docker compose up -d --build praxis-core praxis-storage praxis-network praxis-compute` |

> **Note**: `ls-s3` is a convenience target that lists buckets in LocalStack directly
> — useful for debugging integration tests.

---

## Step 10 — Unit Tests

### `internal/drivers/s3/drift_test.go`

Tests the drift detection logic with AWS quirk handling:

| Test | Purpose |
|---|---|
| `TestHasDrift_NoDrift` | Matching versioning, encryption, and tags → no drift |
| `TestHasDrift_VersioningDrift` | Versioning desired `true` vs observed `"Suspended"` → drift |
| `TestHasDrift_EncryptionDrift` | Algorithm mismatch (`aws:kms` desired vs `AES256` observed) → drift |
| `TestHasDrift_TagDrift` | Tag value change → drift |
| `TestHasDrift_EmptyTagsNoDrift` | `{}` vs `nil` tags → no drift |
| `TestHasDrift_DefaultEncryptionNoDrift` | Encryption not enabled in spec, `AES256` observed → no drift |
| `TestHasDrift_VersioningSuspendedNoDrift` | Desired `false` vs observed `"Suspended"` → no drift |
| `TestHasDrift_VersioningEmptyStringNoDrift` | Desired `false` vs observed `""` (never configured) → no drift |
| `TestComputeFieldDiffs_NoDrift` | No diffs → empty slice |
| `TestComputeFieldDiffs_AllDrifts` | Versioning + encryption + tags all drifted → 3 diffs with correct paths |

### `internal/drivers/s3/driver_test.go`

Tests driver-level functions:

| Test | Purpose |
|---|---|
| `TestSpecFromObserved_FullyPopulated` | Round-trip: observed → spec preserves all fields; versioning `"Enabled"` → `true`; `aws:kms` preserved |
| `TestSpecFromObserved_VersioningSuspended` | `"Suspended"` → `Versioning: false` |
| `TestSpecFromObserved_VersioningNeverEnabled` | `""` (never configured) → `Versioning: false` |
| `TestSpecFromObserved_NoEncryption` | Empty encryption algo defaults to `"AES256"` |
| `TestSpecFromObserved_NilTags` | Nil tags preserved (not converted to empty map) |
| `TestServiceName` | `NewS3BucketDriver(nil).ServiceName()` returns `"S3Bucket"` |

### `internal/drivers/s3/aws_test.go`

Tests error classification:

| Test | Purpose |
|---|---|
| `TestIsBucketNotEmpty_MatchesWrappedErrorText` | String fallback matches `api error BucketNotEmpty:` pattern |
| `TestIsBucketNotEmpty_MatchesRestateWrappedPanicText` | String fallback matches Restate's double-wrapped panic error format with 409 status |

---

## Step 11 — Integration Tests

**File**: `tests/integration/s3_driver_test.go`

Integration tests run against Testcontainers (Restate) + LocalStack (AWS). They
use `restatetest.Start()` to spin up a real Restate environment with the S3 driver
registered. Each test gets a unique bucket name via `uniqueBucket(t)`.

### Helper Functions

- `uniqueBucket(t)`: Generates a test-unique bucket name (sanitized + lowercased
  test name + timestamp). Ensures S3 naming rules (max 63 chars, lowercase, no
  underscores).
- `setupS3Driver(t)`: Configures LocalStack account, creates Restate test env,
  returns ingress client and S3 SDK client.

### Test Cases

| Test | Description |
|---|---|
| `TestS3Provision_CreatesRealBucket` | Creates a bucket with versioning, encryption, and tags. Verifies the bucket exists in LocalStack via `HeadBucket`. |
| `TestS3Provision_Idempotent` | Provisions the same spec twice on the same key. Verifies same bucket name is returned (no error on second call). |
| `TestS3Import_ExistingBucket` | Creates a bucket directly via S3 API, then imports it via the driver. Verifies correct outputs. |
| `TestS3Delete_RemovesBucket` | Provisions then deletes. Verifies bucket is gone from LocalStack. |
| `TestS3Delete_NonEmptyBucketFails` | Provisions, uploads an object directly, then attempts delete. Verifies terminal error (409) is returned. |
| `TestS3Reconcile_DetectsAndFixesDrift` | Provisions with versioning enabled, then suspends versioning directly via S3 API. Triggers reconcile, verifies `Drift: true, Correcting: true`. Verifies versioning was re-enabled in LocalStack. |
| `TestS3GetStatus_ReturnsReady` | Provisions and checks `GetStatus` returns `Ready`, `Managed`, generation > 0. |

---

## S3-Specific Design Decisions

### 1. Global Key Scope — The Simplest Strategy

S3 bucket names are globally unique across all AWS accounts. This means:
- No region prefix needed (unlike EC2's `region~name`).
- No VPC prefix needed (unlike SG's `vpcId~groupName`).
- `BuildKey` returns the bucket name directly from `metadata.name`.
- `BuildImportKey` returns the same value as `BuildKey` for the same resource.

This makes S3 the baseline driver: the simplest key strategy, the simplest drift
model, and the most straightforward lifecycle.

### 2. ConfigureBucket — Single Convergent Call

Rather than separate update methods for each attribute (like the SG driver's separate
`AuthorizeIngress`, `RevokeIngress`, `UpdateTags`), the S3 driver uses a single
`ConfigureBucket` method that applies all configuration atomically. This works
because:
- `PutBucketVersioning` is idempotent.
- `PutBucketEncryption` is idempotent.
- `PutBucketTagging` replaces all tags (not additive).

No add-before-remove safety ordering is needed.

### 3. BucketNotEmpty — Terminal Error, Not Auto-Empty

Deleting a non-empty bucket fails with `TerminalError(409)` and a clear message:
"bucket is not empty — empty it before deleting." Praxis **never** auto-empties
buckets. This is an intentional safety decision:
- Data loss from accidental deletion is catastrophic.
- Users must explicitly empty the bucket (via AWS console, CLI, or another tool).
- This matches the principle of least surprise for infrastructure tools.

### 4. ACL Drift Detection — Not Implemented

`HasDrift` does NOT compare ACLs because AWS's `GetBucketAcl` returns a complex
grant structure (owner, grantee ARNs, permission types) that doesn't map cleanly to
the simple `"private"` / `"public-read"` string the spec uses. Setting ACL works at
creation time, but detecting drift would require interpreting the full grant structure.
This is documented for future improvement.

### 5. Versioning — Three-State Normalization

AWS versioning has three states:
1. `"Enabled"` — versioning is on.
2. `"Suspended"` — versioning was explicitly disabled.
3. `""` (empty string) — versioning was never configured.

Both `"Suspended"` and `""` mean "versioning is off." The drift detection normalizes
empty string to `"Suspended"` before comparison. The `specFromObserved` function
maps both `"Suspended"` and `""` to `Versioning: false`.

### 6. Encryption Default — AES256 Fallback

When importing a bucket with no explicit encryption configuration, `specFromObserved`
defaults the algorithm to `"AES256"`. Since January 2023, AWS enables SSE-S3 (AES256)
by default on all new buckets. This default prevents phantom drift after import
where the observed state shows no encryption but the synthesized spec expects AES256.

### 7. us-east-1 — Two Quirks

1. **CreateBucket**: Must NOT specify `LocationConstraint` for us-east-1.
2. **GetBucketLocation**: Returns empty string for us-east-1, normalized to
   `"us-east-1"`.

Both are longstanding AWS API quirks that affect all S3 SDK integrations.

### 8. No Ownership Tags (Unlike EC2)

S3 bucket names are globally unique — there is no risk of two deployments creating
the same bucket without knowing about each other. The `IsConflict` error classifier
(`BucketAlreadyOwnedByYou`, `BucketAlreadyExists`) handles any collision. No
ownership tag is needed.

### 9. Error State Reconciliation — Read-Only

When the driver is in Error status, reconciliation performs a read-only describe to
update the observed state but does NOT attempt corrective action. The operator must
explicitly re-trigger `Provision` to recover. This prevents the driver from
auto-recovering into a state the operator didn't intend (e.g., after an external
deletion, auto-recreating the bucket might not be desired).

### 10. Delete Never Schedules Reconciliation

After successful deletion, the state is set to `StatusDeleted` and no reconciliation
is scheduled. This is an explicit invariant: a deleted resource should not have
background reconciliation running. If someone re-provisions the same key, a new
reconciliation loop starts from the `Provision` handler.

---

## Checklist

- [x] CUE schema at `schemas/aws/s3/s3.cue`
  - [x] `#S3Bucket` definition with metadata, spec, optional outputs
  - [x] S3 naming regex validation on `metadata.name`
  - [x] Default versioning enabled
  - [x] Default encryption AES256
  - [x] Default ACL private
  - [x] ACL limited to `"private"` and `"public-read"`
- [x] Driver types at `internal/drivers/s3/types.go`
  - [x] `S3BucketSpec` with all fields (BucketName, Region, Versioning, Encryption, ACL, Tags)
  - [x] `EncryptionSpec` (Enabled, Algorithm)
  - [x] `S3BucketOutputs` (ARN, BucketName, Region, DomainName)
  - [x] `ObservedState` with scalar fields
  - [x] `S3BucketState` with all lifecycle fields (documented)
- [x] AWS API layer at `internal/drivers/s3/aws.go`
  - [x] `S3API` interface with 5 methods
  - [x] `realS3API` implementation with rate limiting (100 RPS, burst 20)
  - [x] `HeadBucket` for existence check
  - [x] `CreateBucket` with us-east-1 LocationConstraint handling
  - [x] `ConfigureBucket` — convergent versioning + encryption + tags
  - [x] `DescribeBucket` — multi-probe with graceful error handling
  - [x] `DeleteBucket` — no auto-empty
  - [x] Error classifiers: `IsNotFound`, `IsBucketNotEmpty`, `IsConflict`
  - [x] String fallback in `IsNotFound` and `IsBucketNotEmpty` for Restate-wrapped errors
- [x] Drift detection at `internal/drivers/s3/drift.go`
  - [x] `HasDrift()` — versioning + encryption + tags comparison
  - [x] Versioning three-state normalization (`""` → `"Suspended"`)
  - [x] `ComputeFieldDiffs()` — field-level diffs for plan renderer
  - [x] `tagsMatch()` — order-independent map comparison
  - [x] ACL drift explicitly NOT checked (documented)
- [x] Driver at `internal/drivers/s3/driver.go`
  - [x] `Provision` — create or converge, HeadBucket + CreateBucket + ConfigureBucket
  - [x] `Import` — describe, synthesize spec, no-drift baseline
  - [x] `Delete` — BucketNotEmpty terminal error, error classification inside callback
  - [x] `Reconcile` — drift detection, Managed correction via ConfigureBucket, Observed report-only
  - [x] `GetStatus` (shared handler)
  - [x] `GetOutputs` (shared handler)
  - [x] `scheduleReconcile` with dedup flag (documented why delayed messages instead of Sleep)
  - [x] `apiForAccount` — per-request AWS config resolution (no region return)
  - [x] `specFromObserved` — import round-trip with encryption default
  - [x] Delete never schedules reconciliation (explicit invariant)
- [x] Provider adapter at `internal/core/provider/s3_adapter.go`
  - [x] `Scope()` → `KeyScopeGlobal`
  - [x] `BuildKey()` → bucket name from `metadata.name`
  - [x] `BuildImportKey()` → bucket name (same as BuildKey)
  - [x] `Plan()` with `DescribeBucket` + `ComputeFieldDiffs`
  - [x] `Provision()`, `Delete()`, `Import()`
  - [x] `NormalizeOutputs()`
  - [x] `decodeSpec()` — extracts `metadata.name` as `BucketName`, validates name + region
  - [x] `planningAPI()` with static override for tests
- [x] Registry integration — `NewS3AdapterWithRegistry` in `NewRegistry()`
- [x] Binary entry point in `cmd/praxis-storage/main.go`
- [x] Dockerfile at `cmd/praxis-storage/Dockerfile`
- [x] Docker Compose service (port 9081)
- [x] Justfile targets: `logs-storage`, `test-s3`, `ls-s3`, build, register
- [x] Unit tests
  - [x] `drift_test.go` — 10 tests covering drift detection, versioning normalization, field diffs
  - [x] `driver_test.go` — 6 tests covering specFromObserved (5 scenarios) and ServiceName
  - [x] `aws_test.go` — 2 tests covering IsBucketNotEmpty string matching
- [x] Integration tests at `tests/integration/s3_driver_test.go`
  - [x] `TestS3Provision_CreatesRealBucket`
  - [x] `TestS3Provision_Idempotent`
  - [x] `TestS3Import_ExistingBucket`
  - [x] `TestS3Delete_RemovesBucket`
  - [x] `TestS3Delete_NonEmptyBucketFails` (unique to S3 — data safety)
  - [x] `TestS3Reconcile_DetectsAndFixesDrift` (versioning drift correction)
  - [x] `TestS3GetStatus_ReturnsReady`
