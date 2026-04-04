# Lambda Layer Driver — Implementation Spec

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
16. [Lambda-Layer-Specific Design Decisions](#lambda-layer-specific-design-decisions)
17. [Checklist](#checklist)

---

## 1. Overview & Scope

The Lambda Layer driver manages the lifecycle of **Lambda layers** only. A layer is a
ZIP archive containing libraries, custom runtimes, or other function dependencies.
Layers are **versioned and immutable per version** — each `PublishLayerVersion` call
creates a new version; existing versions cannot be modified.

### Versioning Model

Lambda layers follow a version-per-publish model:

- **Layer name**: Unique per account+region. Created implicitly by the first
  `PublishLayerVersion` call.
- **Layer version**: Integer (1, 2, 3, …). Each version is an immutable snapshot
  of the layer content and metadata.
- **Layer version ARN**: `arn:aws:lambda:<region>:<account>:layer:<name>:<version>`.
  This is the identifier that functions reference in their `layers[]` list.
- **No default version pointer**: Unlike launch templates, layers have no "default"
  version concept. Functions explicitly reference a specific version ARN.

**Praxis approach**: Each Provision call with changed code content publishes a **new
version**. The driver tracks the latest version number in outputs. Functions that
reference the layer should update their `layers[]` to point to the new version ARN.

### Driver Contract

| Handler | Context | Purpose |
|---|---|---|
| `Provision` | `ObjectContext` (exclusive) | Publish a new layer version or update permissions |
| `Import` | `ObjectContext` (exclusive) | Adopt an existing layer |
| `Delete` | `ObjectContext` (exclusive) | Delete the layer (all versions) |
| `Reconcile` | `ObjectContext` (exclusive) | Detect/report drift |
| `GetStatus` | `ObjectSharedContext` (shared) | Return current status |
| `GetOutputs` | `ObjectSharedContext` (shared) | Return layer outputs |

### Mutable vs Immutable Attributes (Per Version)

| Attribute | Mutability | Notes |
|---|---|---|
| Layer name | Immutable | Created with first version, unique per account+region |
| Version number | Immutable | AWS-assigned, sequential |
| Content (ZIP) | Immutable per version | New content = new version |
| Compatible runtimes | Immutable per version | Set at publish time |
| Compatible architectures | Immutable per version | Set at publish time |
| Description | Immutable per version | Set at publish time |
| License info | Immutable per version | Set at publish time |
| Layer permissions | Mutable | Can be added/removed per version |

### What Is NOT In Scope

- **Version cleanup / retention policies**: The driver publishes new versions but does
  not delete old ones automatically. AWS allows up to 5 versions per layer.
  Version pruning is a future operational extension.
- **Cross-account layer sharing**: Layer permissions allow sharing with other accounts.
  This is modeled in the spec but not a primary concern.
- **Layer content building**: The driver deploys pre-built ZIP archives from S3 or
  inline. Building the archive is a CI/CD concern, not a driver concern.

### Downstream Consumers

```text
${resources.my-layer.outputs.layerVersionArn}  → Lambda function layers[] list
${resources.my-layer.outputs.layerName}        → Informational / cross-references
${resources.my-layer.outputs.version}          → Version tracking
${resources.my-layer.outputs.codeSize}         → Deployment verification
```

---

## 2. Key Strategy

### Key Format: `region~layerName`

Lambda layer names are unique within a region+account. The CUE schema maps
`metadata.name` to the layer name. The adapter produces `region~metadata.name`
as the Virtual Object key.

1. **BuildKey** (adapter, plan-time): returns `region~metadata.name`.
2. **Provision / Delete**: dispatched to the same VO key.
3. **Plan**: reads VO state via `GetOutputs`. If outputs contain a `layerVersionArn`,
   the layer exists. Compares code source to detect changes → `OpUpdate` (new version)
   or `OpNoop`.
4. **Import**: `BuildImportKey(region, resourceID)` returns `region~resourceID`
   where `resourceID` is the layer name. Same key as BuildKey.

### No Ownership Tags

Layer names are unique within an account+region. `PublishLayerVersion` on an existing
layer name simply adds a new version. There is no conflict risk — two Praxis
installations targeting the same layer name would both publish versions to the same
layer, which is detectable via version numbering. Ownership tags are not needed.

---

## 3. File Inventory

```text
✦ internal/drivers/lambdalayer/types.go                — Spec, Outputs, ObservedState, State
✦ internal/drivers/lambdalayer/aws.go                  — LayerAPI interface + realLayerAPI impl
✦ internal/drivers/lambdalayer/drift.go                — HasDrift(), ComputeFieldDiffs()
✦ internal/drivers/lambdalayer/driver.go               — LambdaLayerDriver Virtual Object
✦ internal/drivers/lambdalayer/driver_test.go          — Unit tests for driver
✦ internal/drivers/lambdalayer/aws_test.go             — Unit tests for error classification
✦ internal/drivers/lambdalayer/drift_test.go           — Unit tests for drift detection
✦ internal/core/provider/lambdalayer_adapter.go        — Adapter
✦ internal/core/provider/lambdalayer_adapter_test.go   — Adapter tests
✦ schemas/aws/lambda/layer.cue                         — CUE schema
✦ tests/integration/lambda_layer_driver_test.go        — Integration tests
✎ cmd/praxis-compute/main.go                           — Bind LambdaLayer driver
✎ internal/core/provider/registry.go                   — Add adapter to NewRegistry()
```

---

## Step 1 — CUE Schema

**File**: `schemas/aws/lambda/layer.cue`

```cue
package lambda

#LambdaLayer: {
    apiVersion: "praxis.io/v1"
    kind:       "LambdaLayer"

    metadata: {
        // name is the layer name in AWS.
        name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$"
        labels: [string]: string
    }

    spec: {
        // region is the AWS region to publish the layer in.
        region: string

        // description for this layer version.
        description?: string

        // code defines the layer content source. Exactly one must be set.
        code: {
            // s3 references a ZIP archive in S3.
            s3?: {
                bucket:         string
                key:            string
                objectVersion?: string
            }
            // zipFile is a base64-encoded ZIP archive (max 50 MB).
            zipFile?: string
        }

        // compatibleRuntimes lists the runtimes this layer is compatible with.
        // Optional — omit for custom runtimes or runtime-agnostic layers.
        compatibleRuntimes?: [...string]

        // compatibleArchitectures lists the architectures this layer supports.
        compatibleArchitectures?: ["x86_64"] | ["arm64"] | ["x86_64", "arm64"]

        // licenseInfo is SPDX license identifier or URL for the layer.
        licenseInfo?: string

        // permissions controls which accounts can use this layer.
        permissions?: {
            // accountIds that can use this layer version.
            accountIds?: [...string]
            // public makes the layer usable by all AWS accounts.
            public?: bool
        }
    }

    outputs?: {
        layerArn:        string
        layerVersionArn: string
        layerName:       string
        version:         int
        codeSize:        int
        codeSha256:      string
        createdDate:     string
    }
}
```

### Schema Design Notes

- **Code source**: Only S3 and inline ZIP are supported. Lambda layers cannot be
  sourced from container images (ECR).
- **`compatibleRuntimes`**: Optional list of runtime identifiers. If omitted, the
  layer is compatible with all runtimes.
- **`compatibleArchitectures`**: Optional. Defaults to both architectures if omitted.
- **`permissions`**: Layer-level sharing. This is separate from function permissions.

---

## Step 2 — AWS Client Factory

**File**: `internal/infra/awsclient/client.go` — **NO CHANGES NEEDED**

Layer operations use the same Lambda API client as the function driver. The
`NewLambdaClient()` function added for the Lambda Function driver is reused.

---

## Step 3 — Driver Types

**File**: `internal/drivers/lambdalayer/types.go`

```go
package lambdalayer

import "github.com/shirvan/praxis/pkg/types"

const ServiceName = "LambdaLayer"

type LambdaLayerSpec struct {
    Account                  string   `json:"account,omitempty"`
    Region                   string   `json:"region"`
    LayerName                string   `json:"layerName"`
    Description              string   `json:"description,omitempty"`
    Code                     CodeSpec `json:"code"`
    CompatibleRuntimes       []string `json:"compatibleRuntimes,omitempty"`
    CompatibleArchitectures  []string `json:"compatibleArchitectures,omitempty"`
    LicenseInfo              string   `json:"licenseInfo,omitempty"`
    ManagedKey               string   `json:"managedKey,omitempty"`
}

type CodeSpec struct {
    S3      *S3CodeSpec `json:"s3,omitempty"`
    ZipFile string      `json:"zipFile,omitempty"`
}

type S3CodeSpec struct {
    Bucket        string `json:"bucket"`
    Key           string `json:"key"`
    ObjectVersion string `json:"objectVersion,omitempty"`
}

type PermissionsSpec struct {
    AccountIds []string `json:"accountIds,omitempty"`
    Public     bool     `json:"public,omitempty"`
}

type LambdaLayerOutputs struct {
    LayerArn        string `json:"layerArn"`
    LayerVersionArn string `json:"layerVersionArn"`
    LayerName       string `json:"layerName"`
    Version         int64  `json:"version"`
    CodeSize        int64  `json:"codeSize"`
    CodeSha256      string `json:"codeSha256"`
    CreatedDate     string `json:"createdDate"`
}

type ObservedState struct {
    LayerArn                string          `json:"layerArn"`
    LayerVersionArn         string          `json:"layerVersionArn"`
    LayerName               string          `json:"layerName"`
    Version                 int64           `json:"version"`
    Description             string          `json:"description,omitempty"`
    CompatibleRuntimes      []string        `json:"compatibleRuntimes,omitempty"`
    CompatibleArchitectures []string        `json:"compatibleArchitectures,omitempty"`
    LicenseInfo             string          `json:"licenseInfo,omitempty"`
    CodeSize                int64           `json:"codeSize"`
    CodeSha256              string          `json:"codeSha256,omitempty"`
    CreatedDate             string          `json:"createdDate,omitempty"`
    Permissions             PermissionsSpec `json:"permissions"`
}

type LambdaLayerState struct {
    Desired            LambdaLayerSpec      `json:"desired"`
    Observed           ObservedState        `json:"observed"`
    Outputs            LambdaLayerOutputs   `json:"outputs"`
    Status             types.ResourceStatus `json:"status"`
    Mode               types.Mode           `json:"mode"`
    Error              string               `json:"error,omitempty"`
    Generation         int64                `json:"generation"`
    LastReconcile      string               `json:"lastReconcile,omitempty"`
    ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
```

### Types Design Notes

- **`ObservedState` captures the latest version only**: The driver tracks the most
  recently published version. Older versions exist in AWS but are not tracked by
  Praxis.
- **No tags**: Lambda layers are not taggable resources. Ownership is tracked via
  version numbering and layer name uniqueness.

---

## Step 4 — AWS API Abstraction Layer

**File**: `internal/drivers/lambdalayer/aws.go`

### LayerAPI Interface

```go
type LayerAPI interface {
    // PublishLayerVersion publishes a new layer version.
    PublishLayerVersion(ctx context.Context, spec LambdaLayerSpec) (LambdaLayerOutputs, error)

    // GetLatestLayerVersion returns metadata for the highest version number.
    GetLatestLayerVersion(ctx context.Context, layerName string) (ObservedState, error)

    // DeleteLayerVersion deletes a specific layer version.
    DeleteLayerVersion(ctx context.Context, layerName string, version int64) error

    // ListLayerVersions returns all version numbers for a layer.
    ListLayerVersions(ctx context.Context, layerName string) ([]int64, error)

    // SyncLayerVersionPermissions syncs desired permissions for a layer version.
    // Returns the resulting permissions state.
    SyncLayerVersionPermissions(ctx context.Context, layerName string, version int64, desired PermissionsSpec) (PermissionsSpec, error)
}
```

### Implementation: realLayerAPI

```go
type realLayerAPI struct {
    client  *lambda.Client
    limiter ratelimit.Limiter
}

func NewLayerAPI(client *lambdasdk.Client) LayerAPI {
    return &realLayerAPI{client: client, limiter: ratelimit.New("lambda-layer", 15, 10)}
}
```

### Error Classification

```go
func isNotFound(err error) bool {
    var rnf *types.ResourceNotFoundException
    return errors.As(err, &rnf)
}

func isInvalidParam(err error) bool {
    var ipv *types.InvalidParameterValueException
    return errors.As(err, &ipv)
}

func isThrottled(err error) bool {
    var tmr *types.TooManyRequestsException
    return errors.As(err, &tmr)
}
```

### Key Implementation: GetLatestLayerVersion

```go
func (r *realLayerAPI) GetLatestLayerVersion(ctx context.Context, layerName string) (ObservedState, error) {
    r.limiter.Wait(ctx)

    out, err := r.client.ListLayerVersions(ctx, &lambda.ListLayerVersionsInput{
        LayerName: &layerName,
        MaxItems:  aws.Int32(1),
    })
    if err != nil {
        return ObservedState{}, err
    }
    if len(out.LayerVersions) == 0 {
        return ObservedState{}, &types.ResourceNotFoundException{
            Message: aws.String("no versions found for layer " + layerName),
        }
    }

    latest := out.LayerVersions[0]
    return r.GetLayerVersion(ctx, layerName, latest.Version)
}
```

---

## Step 5 — Drift Detection

**File**: `internal/drivers/lambdalayer/drift.go`

### Drift-Detectable Fields

Layer versions are immutable, so drift detection is limited to:

| Field | Drift Source | Notes |
|---|---|---|
| Layer existence | External deletion | Layer or all versions deleted externally |
| Latest version number | External publish | Someone published a new version outside Praxis |

### Fields NOT Drift-Detected

- **Content**: Versions are immutable. If a version exists, its content matches
  what was published.
- **Compatible runtimes / architectures / description / license**: Immutable per
  version. Cannot drift.

### HasDrift

```go
func HasDrift(state LambdaLayerState, observed ObservedState) bool {
    // Drift if the latest version is not what we published
    if observed.Version != state.Outputs.Version {
        return true
    }
    return false
}
```

### ComputeFieldDiffs

```go
func ComputeFieldDiffs(state LambdaLayerState, observed ObservedState) []types.FieldDiff {
    var diffs []types.FieldDiff
    if observed.Version != state.Outputs.Version {
        diffs = append(diffs, types.FieldDiff{
            Field:    "version",
            Desired:  fmt.Sprintf("%d", state.Outputs.Version),
            Observed: fmt.Sprintf("%d", observed.Version),
        })
    }
    return diffs
}
```

**Design note**: Drift correction for layers is report-only regardless of mode. The
driver cannot "fix" a version number mismatch by deleting externally-published
versions (that could break other functions). Instead, drift detection simply reports
that the latest version has changed. The user can re-run Provision to publish a new
version that becomes the tracked version.

---

## Step 6 — Driver Implementation

**File**: `internal/drivers/lambdalayer/driver.go`

### Constructor

```go
type LambdaLayerDriver struct {
    auth       authservice.AuthClient
    apiFactory func(aws.Config) LayerAPI
}

func NewLambdaLayerDriver(auth authservice.AuthClient) *LambdaLayerDriver {
    return NewLambdaLayerDriverWithFactory(auth, func(cfg aws.Config) LayerAPI {
        return NewLayerAPI(awsclient.NewLambdaClient(cfg))
    })
}

func (d *LambdaLayerDriver) ServiceName() string { return ServiceName }
```

### Provision

```go
func (d *LambdaLayerDriver) Provision(ctx restate.ObjectContext, spec LambdaLayerSpec) (LambdaLayerOutputs, error) {
    state, _ := restate.Get[*LambdaLayerState](ctx, drivers.StateKey)
    api := d.buildAPI(spec.Account, spec.Region)

    // Check if code has changed (new version needed)
    if state != nil && state.Outputs.LayerVersionArn != "" {
        if !codeChanged(spec.Code, state.Desired.Code) &&
           !metadataChanged(spec, state.Desired) {
            // No changes — return existing outputs
            return state.Outputs, nil
        }
    }

    // Publish new layer version
    state = &LambdaLayerState{
        Desired: spec,
        Status:  types.StatusProvisioning,
        Mode:    drivers.DefaultMode(""),
        Generation: stateGeneration(state) + 1,
    }
    restate.Set(ctx, drivers.StateKey, state)

    outputs, err := restate.Run(ctx, func(rc restate.RunContext) (LambdaLayerOutputs, error) {
        return api.PublishLayerVersion(rc, spec)
    })
    if err != nil {
        if isInvalidParam(err) {
            return LambdaLayerOutputs{}, restate.TerminalError(
                fmt.Errorf("invalid layer configuration: %w", err), 400)
        }
        return LambdaLayerOutputs{}, err
    }

    // Describe the published version for full observed state
    observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
        return api.GetLayerVersion(rc, spec.LayerName, outputs.Version)
    })
    if err != nil {
        return LambdaLayerOutputs{}, err
    }

    state.Observed = observed
    state.Outputs = outputs
    state.Status = types.StatusReady
    state.Error = ""
    restate.Set(ctx, drivers.StateKey, state)

    d.scheduleReconcile(ctx)

    return outputs, nil
}
```

### Import

```go
func (d *LambdaLayerDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (LambdaLayerOutputs, error) {
    api := d.buildAPI(ref.Account, ref.Region)

    // Get the latest version of the layer
    observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
        return api.GetLatestLayerVersion(rc, ref.ResourceID)
    })
    if err != nil {
        if isNotFound(err) {
            return LambdaLayerOutputs{}, restate.TerminalError(
                fmt.Errorf("layer %q not found in %s", ref.ResourceID, ref.Region), 404)
        }
        return LambdaLayerOutputs{}, err
    }

    spec := specFromObserved(observed, ref)
    outputs := outputsFromObserved(observed)
    mode := types.ModeObserved
    if ref.Mode != "" {
        mode = ref.Mode
    }

    restate.Set(ctx, drivers.StateKey, &LambdaLayerState{
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
func (d *LambdaLayerDriver) Delete(ctx restate.ObjectContext) error {
    state, err := restate.Get[*LambdaLayerState](ctx, drivers.StateKey)
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

    // List all versions and delete each one
    versions, err := restate.Run(ctx, func(rc restate.RunContext) ([]int64, error) {
        return api.ListLayerVersions(rc, state.Desired.LayerName)
    })
    if err != nil {
        if !isNotFound(err) {
            return err
        }
        versions = nil
    }

    for _, v := range versions {
        version := v // capture for closure
        if _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
            return restate.Void{}, api.DeleteLayerVersion(rc, state.Desired.LayerName, version)
        }); err != nil {
            if !isNotFound(err) {
                return err
            }
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
func (d *LambdaLayerDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
    state, err := restate.Get[*LambdaLayerState](ctx, drivers.StateKey)
    if err != nil {
        return types.ReconcileResult{}, err
    }
    if state == nil {
        return types.ReconcileResult{Status: "no-state"}, nil
    }

    state.ReconcileScheduled = false
    api := d.buildAPI(state.Desired.Account, state.Desired.Region)

    observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
        return api.GetLayerVersion(rc, state.Desired.LayerName, state.Outputs.Version)
    })
    if err != nil {
        if isNotFound(err) {
            state.Status = types.StatusError
            state.Error = "layer version not found in AWS — may have been deleted externally"
            state.Observed = ObservedState{}
            restate.Set(ctx, drivers.StateKey, state)
            d.scheduleReconcile(ctx)
            return types.ReconcileResult{Status: "error", Error: state.Error}, nil
        }
        return types.ReconcileResult{}, err
    }

    state.Observed = observed
    state.LastReconcile = time.Now().UTC().Format(time.RFC3339)

    if !HasDrift(*state, observed) {
        state.Status = types.StatusReady
        state.Error = ""
        restate.Set(ctx, drivers.StateKey, state)
        d.scheduleReconcile(ctx)
        return types.ReconcileResult{Status: "ok"}, nil
    }

    diffs := ComputeFieldDiffs(*state, observed)

    // Layer drift is always report-only — cannot "correct" version number mismatches
    state.Status = types.StatusReady
    state.Error = ""
    restate.Set(ctx, drivers.StateKey, state)
    d.scheduleReconcile(ctx)

    return types.ReconcileResult{
        Status: "drift-detected",
        Drifts: diffs,
    }, nil
}
```

### GetStatus / GetOutputs

```go
func (d *LambdaLayerDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
    state, err := restate.Get[*LambdaLayerState](ctx, drivers.StateKey)
    if err != nil {
        return types.StatusResponse{}, err
    }
    if state == nil {
        return types.StatusResponse{Status: types.StatusPending}, nil
    }
    return types.StatusResponse{
        Status:     state.Status,
        Mode:       state.Mode,
        Generation: state.Generation,
        Error:      state.Error,
    }, nil
}

func (d *LambdaLayerDriver) GetOutputs(ctx restate.ObjectSharedContext) (LambdaLayerOutputs, error) {
    state, err := restate.Get[*LambdaLayerState](ctx, drivers.StateKey)
    if err != nil {
        return LambdaLayerOutputs{}, err
    }
    if state == nil {
        return LambdaLayerOutputs{}, nil
    }
    return state.Outputs, nil
}
```

---

## Step 7 — Provider Adapter

**File**: `internal/core/provider/lambdalayer_adapter.go`

```go
type LambdaLayerAdapter struct {
    accounts *auth.Registry
}

func NewLambdaLayerAdapterWithRegistry(accounts *auth.Registry) *LambdaLayerAdapter {
    return &LambdaLayerAdapter{accounts: accounts}
}

func (a *LambdaLayerAdapter) Kind() string { return lambdalayer.ServiceName }

func (a *LambdaLayerAdapter) Scope() KeyScope { return KeyScopeRegion }

func (a *LambdaLayerAdapter) BuildKey(doc json.RawMessage) (string, error) {
    region, _ := jsonpath.String(doc.Spec, "region")
    name := doc.Metadata.Name
    if region == "" || name == "" {
        return "", fmt.Errorf("LambdaLayer requires spec.region and metadata.name")
    }
    return region + "~" + name, nil
}

func (a *LambdaLayerAdapter) BuildImportKey(region, resourceID string) (string, error) {
    return region + "~" + resourceID, nil
}

func (a *LambdaLayerAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
    spec, err := decodeSpec(doc)
    if err != nil {
        return types.PlanResult{}, err
    }

    if len(currentOutputs) == 0 {
        return types.PlanResult{Op: types.OpCreate, Spec: spec}, nil
    }

    // For layers, any code or metadata change means a new version → OpUpdate
    // Compare spec vs last-provisioned spec from outputs
    return types.PlanResult{Op: types.OpUpdate, Spec: spec}, nil
}
```

---

## Step 8 — Registry Integration

**File**: `internal/core/provider/registry.go` — **MODIFY**

Add `NewLambdaLayerAdapterWithRegistry(accounts)` to the `NewRegistry()` function.

---

## Step 9 — Compute Driver Pack Entry Point

**File**: `cmd/praxis-compute/main.go` — **MODIFY**

Add Lambda Layer driver binding:

```go
import "github.com/shirvan/praxis/internal/drivers/lambdalayer"

Bind(restate.Reflect(lambdalayer.NewLambdaLayerDriver(cfg.Auth())))
```

---

## Step 10 — Docker Compose & Justfile

### Docker Compose

No changes needed — hosted in existing `praxis-compute` container.

### Justfile Additions

```just
test-lambda-layer:
    go test ./internal/drivers/lambdalayer/... -v -count=1 -race

test-lambda-layer-integration:
    go test ./tests/integration/... -run TestLambdaLayer -v -timeout=3m
```

---

## Step 11 — Unit Tests

**File**: `internal/drivers/lambdalayer/driver_test.go`

### Test Cases

| Test | Description |
|---|---|
| `TestProvision_NewLayer` | First publish; verifies outputs, state, and reconcile scheduled |
| `TestProvision_NewVersion` | Code changed; verifies new version published |
| `TestProvision_NoChange` | No code or metadata change; verifies idempotent return |
| `TestProvision_InvalidParam` | Invalid runtime; verifies terminal 400 error |
| `TestImport_Success` | Imports existing layer; verifies observed-mode state |
| `TestImport_NotFound` | Import non-existent layer; verifies terminal 404 error |
| `TestDelete_AllVersions` | Deletes all versions; verifies each version deleted |
| `TestDelete_Observed` | Attempts delete of observed layer; verifies 403 error |
| `TestReconcile_VersionMatch` | No drift; verifies status ok |
| `TestReconcile_VersionMismatch` | External version published; verifies drift report |
| `TestReconcile_LayerGone` | Layer deleted externally; verifies error status |

**File**: `internal/drivers/lambdalayer/drift_test.go`

| Test | Description |
|---|---|
| `TestHasDrift_NoDrift` | Version matches; no drift |
| `TestHasDrift_VersionChanged` | Version mismatch; drift detected |

---

## Step 12 — Integration Tests

**File**: `tests/integration/lambda_layer_driver_test.go`

| Test | Description |
|---|---|
| `TestLambdaLayer_PublishAndDescribe` | Publish layer, verify outputs |
| `TestLambdaLayer_PublishNewVersion` | Publish v1, change code, publish v2, verify version increments |
| `TestLambdaLayer_Import` | Create layer via AWS API, import via driver |
| `TestLambdaLayer_Delete` | Publish then delete all versions, verify cleanup |
| `TestLambdaLayer_Reconcile` | Publish, reconcile, verify no drift |

### Moto Considerations

- Moto supports Lambda layers with basic publish/describe/delete operations.
- Test layers should use minimal ZIP archives (e.g., single empty file).
- Tests should clean up all layer versions in teardown.

---

## Lambda-Layer-Specific Design Decisions

### 1. Version-Per-Publish Model

**Decision**: Every Provision call with changed code or metadata publishes a new
layer version. The driver never modifies existing versions.

**Rationale**: AWS Lambda layer versions are immutable. The only way to "update" a
layer is to publish a new version. This is the same model as Launch Templates.

### 2. Change Detection

**Decision**: Compare S3 bucket/key/objectVersion (or zipFile content) AND metadata
fields (compatibleRuntimes, description, licenseInfo, compatibleArchitectures) to
decide whether to publish a new version.

**Rationale**: Any change in these fields constitutes a new layer version. Even
metadata-only changes (e.g., adding a new compatible runtime) require a new version
because version metadata is immutable.

### 3. Delete Deletes All Versions

**Decision**: The Delete handler lists all versions and deletes each one.

**Rationale**: AWS does not provide a single "delete layer" API call. Each version
must be deleted individually. Deleting all versions effectively removes the layer.
If only the tracked version were deleted, orphaned older versions would leak.

### 4. Import Captures Latest Version

**Decision**: Import discovers the latest (highest-numbered) version and tracks it.

**Rationale**: When importing an externally-created layer, the latest version is the
most relevant. Older versions are immutable and will remain accessible — the driver
only tracks one version in its state.

### 5. Reconcile is Report-Only

**Decision**: Reconcile only reports drift (version mismatch); it does not correct it.

**Rationale**: The only drift that can occur is someone publishing a new version
externally. "Correcting" this would mean deleting the external version, which could
break functions that reference it. The driver reports the mismatch and leaves
correction to the user (re-run Provision to publish a new version).

### 6. Import Default Mode

**Decision**: Import defaults to `ModeObserved`.

**Rationale**: Consistent with the Lambda Function driver and other mutable/stateful
resource drivers.

---

## Checklist

### Implementation

- [ ] `schemas/aws/lambda/layer.cue`
- [ ] `internal/drivers/lambdalayer/types.go`
- [ ] `internal/drivers/lambdalayer/aws.go`
- [ ] `internal/drivers/lambdalayer/drift.go`
- [ ] `internal/drivers/lambdalayer/driver.go`
- [ ] `internal/core/provider/lambdalayer_adapter.go`

### Tests

- [ ] `internal/drivers/lambdalayer/driver_test.go`
- [ ] `internal/drivers/lambdalayer/aws_test.go`
- [ ] `internal/drivers/lambdalayer/drift_test.go`
- [ ] `internal/core/provider/lambdalayer_adapter_test.go`
- [ ] `tests/integration/lambda_layer_driver_test.go`

### Integration

- [ ] `cmd/praxis-compute/main.go` — Bind driver
- [ ] `internal/core/provider/registry.go` — Register adapter
- [ ] `justfile` — Add test targets
