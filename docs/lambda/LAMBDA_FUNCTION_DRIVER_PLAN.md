# Lambda Function Driver — Implementation Spec

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
16. [Lambda-Function-Specific Design Decisions](#lambda-function-specific-design-decisions)
17. [Checklist](#checklist)

---

## 1. Overview & Scope

The Lambda Function driver manages the lifecycle of **Lambda functions** only.
Permissions (resource-based policies), event source mappings, aliases, provisioned
concurrency, and function URLs are separate drivers or future extensions. This
document covers function creation, code deployment, configuration
updates, import, deletion, and drift reconciliation.

The driver follows the established Virtual Object contract:

| Handler | Context | Purpose |
|---|---|---|
| `Provision` | `ObjectContext` (exclusive) | Create or converge a Lambda function |
| `Import` | `ObjectContext` (exclusive) | Adopt an existing function |
| `Delete` | `ObjectContext` (exclusive) | Delete a function |
| `Reconcile` | `ObjectContext` (exclusive) | Detect/correct drift (report-only for Observed mode) |
| `GetStatus` | `ObjectSharedContext` (shared) | Return current status |
| `GetOutputs` | `ObjectSharedContext` (shared) | Return function outputs |

### Mutable vs Immutable Attributes

| Attribute | Mutability | API | Notes |
|---|---|---|---|
| Function name | Immutable | — | Set at creation; cannot be changed |
| Function ARN | Immutable | — | AWS-assigned |
| Runtime | Mutable | `UpdateFunctionConfiguration` | e.g., python3.12 → python3.13 |
| Handler | Mutable | `UpdateFunctionConfiguration` | Entry point (e.g., `main.handler`) |
| Role | Mutable | `UpdateFunctionConfiguration` | IAM execution role ARN |
| Memory size | Mutable | `UpdateFunctionConfiguration` | 128 MB – 10,240 MB |
| Timeout | Mutable | `UpdateFunctionConfiguration` | 1 – 900 seconds |
| Environment variables | Mutable | `UpdateFunctionConfiguration` | Key-value pairs |
| Description | Mutable | `UpdateFunctionConfiguration` | Free-form text |
| Layers | Mutable | `UpdateFunctionConfiguration` | Layer version ARNs |
| VPC config | Mutable | `UpdateFunctionConfiguration` | Subnet IDs + security group IDs |
| Dead letter config | Mutable | `UpdateFunctionConfiguration` | DLQ ARN (SQS or SNS) |
| Tracing config | Mutable | `UpdateFunctionConfiguration` | X-Ray tracing mode |
| Architectures | Mutable | `UpdateFunctionConfiguration` | x86_64 or arm64 |
| Ephemeral storage | Mutable | `UpdateFunctionConfiguration` | 512 MB – 10,240 MB |
| Code (S3/zip) | Mutable | `UpdateFunctionCode` | Separate API call |
| Tags | Mutable | `TagResource` / `UntagResource` | Key-value pairs |

### Dual Update Path

Lambda functions have a critical design constraint: **code updates and configuration
updates are separate API calls**. The driver:

1. Calls `UpdateFunctionCode` if code source has changed.
2. Waits for `LastUpdateStatus` to become `Successful` via `WaitForFunctionStable`.
3. Calls `UpdateFunctionConfiguration` if configuration has changed.
4. Waits for `LastUpdateStatus` to become `Successful`.

Calling both simultaneously causes `ResourceConflictException`. The driver handles
this sequentially within a single `Provision` call, using `restate.Run()` for each
AWS API call.

### What Is NOT In Scope

- **Lambda Permissions**: Resource-based policy statements are managed by the
  Lambda Permission driver.
- **Event Source Mappings**: Managed by the Event Source Mapping driver.
- **Aliases**: Version aliases (e.g., `PROD`, `STAGING`) are a future extension.
  The driver always targets `$LATEST`.
- **Provisioned Concurrency**: A future extension to the function driver or a
  separate driver.
- **Function URLs**: A future extension.
- **Code signing**: A future extension via Code Signing Config resources.

### Downstream Consumers

```text
${resources.my-fn.outputs.functionArn}       → Permissions, ESMs, other services
${resources.my-fn.outputs.functionName}      → Permissions, ESMs, CLI references
${resources.my-fn.outputs.version}           → Alias configuration
${resources.my-fn.outputs.codeHash}          → Deployment verification
${resources.my-fn.outputs.vpcId}             → VPC-aware orchestration
```

---

## 2. Key Strategy

### Key Format: `region~functionName`

Lambda function names are unique within a region+account. The CUE schema maps
`metadata.name` to the function name. The adapter produces `region~metadata.name`
as the Virtual Object key.

1. **BuildKey** (adapter, plan-time): returns `region~metadata.name`.
2. **Provision / Delete**: dispatched to the same VO key.
3. **Plan**: reads VO state via `GetOutputs`. If outputs contain a function ARN,
   describes the function by name. Otherwise, `OpCreate`.
4. **Import**: `BuildImportKey(region, resourceID)` returns `region~resourceID`
   where `resourceID` is the function name. Same key as BuildKey — matching the
   S3/KeyPair pattern because function names are AWS-unique per region.

### Ownership Tags

Lambda function names are unique within a region+account, so AWS rejects duplicate
`CreateFunction` calls. However, the driver adds `praxis:managed-key=<region~functionName>`
as a function tag for consistency with the EC2 pattern and to provide cross-Praxis-
installation conflict detection.

**FindByManagedKey** is NOT needed because Lambda function names are AWS-enforced
unique. The `CreateFunction` call itself provides the conflict signal via
`ResourceConflictException`.

### Import Semantics

Import and template-based management produce the **same Virtual Object key** because
function names are globally unique within a region (like S3 bucket names):

- `praxis import --kind LambdaFunction --region us-east-1 --resource-id my-fn`:
  Creates VO key `us-east-1~my-fn`.
- Template with `metadata.name: my-fn` in `us-east-1`:
  Creates VO key `us-east-1~my-fn`.

Both target the same Virtual Object. This is analogous to S3 and Key Pair import
semantics.

---

## 3. File Inventory

The following files comprise the Lambda Function driver:

```text
✦ internal/drivers/lambda/types.go                — Spec, Outputs, ObservedState, State structs
✦ internal/drivers/lambda/aws.go                  — LambdaAPI interface + realLambdaAPI impl
✦ internal/drivers/lambda/drift.go                — HasDrift(), ComputeFieldDiffs()
✦ internal/drivers/lambda/driver.go               — LambdaFunctionDriver Virtual Object
✦ internal/drivers/lambda/driver_test.go          — Unit tests for driver (mocked AWS)
✦ internal/drivers/lambda/aws_test.go             — Unit tests for error classification
✦ internal/drivers/lambda/drift_test.go           — Unit tests for drift detection
✦ internal/core/provider/lambda_adapter.go        — LambdaFunctionAdapter implementing provider.Adapter
✦ internal/core/provider/lambda_adapter_test.go   — Unit tests for adapter
✦ schemas/aws/lambda/function.cue                 — CUE schema for LambdaFunction resource
✦ tests/integration/lambda_driver_test.go         — Integration tests (Testcontainers + Moto)
✎ internal/infra/awsclient/client.go              — Add NewLambdaClient()
✎ cmd/praxis-compute/main.go                      — Bind LambdaFunction driver
✎ internal/core/provider/registry.go              — Add adapter to NewRegistry()
✎ justfile                                        — Add lambda test targets
```

---

## Step 1 — CUE Schema

**File**: `schemas/aws/lambda/function.cue`

```cue
package lambda

#LambdaFunction: {
    apiVersion: "praxis.io/v1"
    kind:       "LambdaFunction"

    metadata: {
        // name is the Lambda function name in AWS.
        // Must be 1-64 characters: [a-zA-Z0-9-_].
        name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$"
        labels: [string]: string
    }

    spec: {
        // region is the AWS region to create the function in.
        region: string

        // role is the ARN of the IAM execution role for this function.
        role: string

        // runtime is the Lambda runtime identifier (e.g., "python3.12", "nodejs20.x", "go1.x", "provided.al2023").
        runtime: string

        // handler is the function entry point (e.g., "main.handler", "index.handler").
        // Not required for container image deployments or custom runtimes using a bootstrap file.
        handler?: string

        // description is a human-readable description of the function.
        description?: string

        // code defines where the function code is sourced from. Exactly one must be set.
        code: {
            // s3 deploys code from an S3 bucket.
            s3?: {
                bucket:         string
                key:            string
                objectVersion?: string
            }
            // zipFile deploys code from a base64-encoded ZIP archive.
            // Suitable for small functions (< 50 MB compressed).
            zipFile?: string
            // imageUri deploys code from a container image in ECR.
            imageUri?: string
        }

        // memorySize is the amount of memory available to the function at runtime (MB).
        memorySize: int & >=128 & <=10240 | *128

        // timeout is the maximum execution time in seconds.
        timeout: int & >=1 & <=900 | *3

        // environment variables available to the function at runtime.
        environment?: [string]: string

        // layers is a list of Lambda layer version ARNs to attach (max 5).
        layers?: [...string] & list.MaxItems(5)

        // vpcConfig connects the function to a VPC for accessing private resources.
        vpcConfig?: {
            subnetIds:        [...string] & list.MinItems(1)
            securityGroupIds: [...string] & list.MinItems(1)
        }

        // deadLetterConfig specifies a DLQ for failed async invocations.
        deadLetterConfig?: {
            targetArn: string
        }

        // tracingConfig controls AWS X-Ray tracing.
        tracingConfig?: {
            mode: "Active" | "PassThrough" | *"PassThrough"
        }

        // architectures specifies the instruction set architecture.
        architectures: ["x86_64"] | ["arm64"] | *["x86_64"]

        // ephemeralStorage configures the /tmp directory size (MB).
        ephemeralStorage?: {
            size: int & >=512 & <=10240 | *512
        }

        // tags on the function resource.
        tags: [string]: string
    }

    outputs?: {
        functionArn:  string
        functionName: string
        version:      string
        codeHash:     string
        codeSizeBytes: int
        lastModified: string
        state:        string
        vpcId?:       string
    }
}
```

### Schema Design Notes

- **`code` union**: Exactly one of `s3`, `zipFile`, or `imageUri` must be provided.
  CUE constraints should enforce mutual exclusivity via a disjunction or custom
  validator. `zipFile` is limited to small functions; production deployments should
  use S3 or ECR.
- **`layers` max 5**: AWS enforces a maximum of 5 layers per function. The CUE schema
  validates this at template time.
- **`handler` optional**: Container image functions and `provided.*` runtimes use a
  bootstrap executable, not a handler string.
- **`architectures` singleton list**: AWS models this as a list but only supports one
  value. The schema constrains it to exactly one element.
- **`environment` is a flat map**: Lambda environment variables are string→string pairs.
  Complex values must be JSON-encoded by the user.

---

## Step 2 — AWS Client Factory

**File**: `internal/infra/awsclient/client.go` — **ADD NewLambdaClient()**

```go
import "github.com/aws/aws-sdk-go-v2/service/lambda"

// NewLambdaClient creates a Lambda API client from the given AWS config.
func NewLambdaClient(cfg aws.Config) *lambda.Client {
    return lambda.NewFromConfig(cfg)
}
```

This follows the exact pattern of `NewEC2Client()` and `NewS3Client()`.

---

## Step 3 — Driver Types

**File**: `internal/drivers/lambda/types.go`

```go
package lambda

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate Virtual Object name for Lambda Functions.
const ServiceName = "LambdaFunction"

// LambdaFunctionSpec is the desired state for a Lambda function.
type LambdaFunctionSpec struct {
    Account          string                `json:"account,omitempty"`
    Region           string                `json:"region"`
    FunctionName     string                `json:"functionName"`
    Role             string                `json:"role"`
    PackageType      string                `json:"packageType,omitempty"`
    Runtime          string                `json:"runtime,omitempty"`
    Handler          string                `json:"handler,omitempty"`
    Description      string                `json:"description,omitempty"`
    Code             CodeSpec              `json:"code"`
    MemorySize       int32                 `json:"memorySize,omitempty"`
    Timeout          int32                 `json:"timeout,omitempty"`
    Environment      map[string]string     `json:"environment,omitempty"`
    Layers           []string              `json:"layers,omitempty"`
    VPCConfig        *VPCConfigSpec        `json:"vpcConfig,omitempty"`
    DeadLetterConfig *DeadLetterConfigSpec `json:"deadLetterConfig,omitempty"`
    TracingConfig    *TracingConfigSpec    `json:"tracingConfig,omitempty"`
    Architectures    []string              `json:"architectures,omitempty"`
    EphemeralStorage *EphemeralStorageSpec `json:"ephemeralStorage,omitempty"`
    Tags             map[string]string     `json:"tags,omitempty"`
    ManagedKey       string                `json:"managedKey,omitempty"`
}

// CodeSpec defines where the function code comes from.
type CodeSpec struct {
    S3       *S3CodeSpec `json:"s3,omitempty"`
    ZipFile  string      `json:"zipFile,omitempty"`
    ImageURI string      `json:"imageUri,omitempty"`
}

// S3CodeSpec references function code stored in S3.
type S3CodeSpec struct {
    Bucket        string `json:"bucket"`
    Key           string `json:"key"`
    ObjectVersion string `json:"objectVersion,omitempty"`
}

// VPCConfigSpec connects the function to a VPC.
type VPCConfigSpec struct {
    SubnetIds        []string `json:"subnetIds,omitempty"`
    SecurityGroupIds []string `json:"securityGroupIds,omitempty"`
}

// DeadLetterConfigSpec specifies a dead letter queue for failed async invocations.
type DeadLetterConfigSpec struct {
    TargetArn string `json:"targetArn"`
}

// TracingConfigSpec controls X-Ray tracing.
type TracingConfigSpec struct {
    Mode string `json:"mode,omitempty"`
}

// EphemeralStorageSpec configures /tmp storage size.
type EphemeralStorageSpec struct {
    Size int32 `json:"size"`
}

// LambdaFunctionOutputs is produced after provisioning and stored in Restate K/V.
type LambdaFunctionOutputs struct {
    FunctionArn      string `json:"functionArn"`
    FunctionName     string `json:"functionName"`
    Version          string `json:"version,omitempty"`
    State            string `json:"state,omitempty"`
    LastModified     string `json:"lastModified,omitempty"`
    LastUpdateStatus string `json:"lastUpdateStatus,omitempty"`
    CodeSha256       string `json:"codeSha256,omitempty"`
}

// ObservedState captures the actual configuration of a function from AWS.
type ObservedState struct {
    FunctionArn      string            `json:"functionArn"`
    FunctionName     string            `json:"functionName"`
    Role             string            `json:"role"`
    PackageType      string            `json:"packageType,omitempty"`
    Runtime          string            `json:"runtime,omitempty"`
    Handler          string            `json:"handler,omitempty"`
    Description      string            `json:"description,omitempty"`
    MemorySize       int32             `json:"memorySize,omitempty"`
    Timeout          int32             `json:"timeout,omitempty"`
    Environment      map[string]string `json:"environment,omitempty"`
    Layers           []string          `json:"layers,omitempty"`
    VpcConfig        VPCConfigSpec     `json:"vpcConfig,omitzero"`
    DeadLetterTarget string            `json:"deadLetterTarget,omitempty"`
    TracingMode      string            `json:"tracingMode,omitempty"`
    Architectures    []string          `json:"architectures,omitempty"`
    EphemeralSize    int32             `json:"ephemeralSize,omitempty"`
    Tags             map[string]string `json:"tags,omitempty"`
    ImageURI         string            `json:"imageUri,omitempty"`
    Version          string            `json:"version,omitempty"`
    State            string            `json:"state,omitempty"`
    LastModified     string            `json:"lastModified,omitempty"`
    LastUpdateStatus string            `json:"lastUpdateStatus,omitempty"`
    CodeSha256       string            `json:"codeSha256,omitempty"`
}

// LambdaFunctionState is the single atomic state object stored under drivers.StateKey.
type LambdaFunctionState struct {
    Desired            LambdaFunctionSpec      `json:"desired"`
    Observed           ObservedState           `json:"observed"`
    Outputs            LambdaFunctionOutputs   `json:"outputs"`
    Status             types.ResourceStatus    `json:"status"`
    Mode               types.Mode              `json:"mode"`
    Error              string                  `json:"error,omitempty"`
    Generation         int64                   `json:"generation"`
    LastReconcile      string                  `json:"lastReconcile,omitempty"`
    ReconcileScheduled bool                    `json:"reconcileScheduled"`
}
```

### Types Design Notes

- **`CodeSpec.ZipFile` is `string`**: The driver accepts base64-encoded ZIP content
  as a string. The CUE schema accepts base64; the adapter passes it through.
- **`PackageType`**: Supports `Zip` (default) or `Image` for container-based functions.
  When `Image` is used, `Runtime` and `Handler` are not required.
- **`ObservedState.Layers` stores ARNs**: The describe call returns layer ARNs
  with version numbers. These are compared directly against the desired `Layers` list.
- **`ObservedState.LastUpdateStatus`**: Critical for the dual-update flow. The driver
  polls via `WaitForFunctionStable` until this becomes `Successful` before issuing the
  next update.

---

## Step 4 — AWS API Abstraction Layer

**File**: `internal/drivers/lambda/aws.go`

### LambdaAPI Interface

```go
type LambdaAPI interface {
    // CreateFunction creates a new Lambda function. Returns the function ARN.
    CreateFunction(ctx context.Context, spec LambdaFunctionSpec) (string, error)

    // UpdateFunctionCode updates the function's deployment package.
    UpdateFunctionCode(ctx context.Context, spec LambdaFunctionSpec) error

    // UpdateFunctionConfiguration updates the function's runtime configuration.
    UpdateFunctionConfiguration(ctx context.Context, spec LambdaFunctionSpec, observed ObservedState) error

    // DescribeFunction returns the current configuration and code metadata.
    DescribeFunction(ctx context.Context, functionName string) (ObservedState, error)

    // DeleteFunction deletes the function and all its versions/aliases.
    DeleteFunction(ctx context.Context, functionName string) error

    // UpdateTags replaces all tags on the function.
    UpdateTags(ctx context.Context, functionArn string, tags map[string]string) error

    // WaitForFunctionStable polls until LastUpdateStatus is Successful.
    // Returns an error if the update fails or times out.
    WaitForFunctionStable(ctx context.Context, functionName string, timeout time.Duration) error
}
```

### Implementation: realLambdaAPI

```go
type realLambdaAPI struct {
    client *lambda.Client
    limiter ratelimit.Limiter
}

func NewLambdaAPI(client *lambdasdk.Client) LambdaAPI {
    return &realLambdaAPI{client: client, limiter: ratelimit.New("lambda-function", 15, 10)}
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

func isThrottled(err error) bool {
    var tmr *types.TooManyRequestsException
    return errors.As(err, &tmr)
}

func isInvalidParam(err error) bool {
    var ipv *types.InvalidParameterValueException
    return errors.As(err, &ipv)
}
```

### Key Implementation Details

#### CreateFunction

```go
func (r *realLambdaAPI) CreateFunction(ctx context.Context, spec LambdaFunctionSpec) (LambdaFunctionOutputs, error) {
    r.limiter.Wait(ctx)

    input := &lambda.CreateFunctionInput{
        FunctionName:  &spec.FunctionName,
        Role:          &spec.Role,
        Runtime:       lambdatypes.Runtime(spec.Runtime),
        Handler:       nilIfEmpty(spec.Handler),
        Description:   nilIfEmpty(spec.Description),
        MemorySize:    &spec.MemorySize,
        Timeout:       &spec.Timeout,
        Architectures: toArchitectures(spec.Architectures),
        Tags:          spec.Tags,
    }

    // Set code source
    input.Code = buildFunctionCode(spec.Code)

    // Set optional configurations
    if spec.Environment != nil {
        input.Environment = &lambdatypes.Environment{Variables: spec.Environment}
    }
    if len(spec.Layers) > 0 {
        input.Layers = spec.Layers
    }
    if spec.VpcConfig != nil {
        input.VpcConfig = &lambdatypes.VpcConfig{
            SubnetIds:        spec.VpcConfig.SubnetIds,
            SecurityGroupIds: spec.VpcConfig.SecurityGroupIds,
        }
    }
    if spec.DeadLetterConfig != nil {
        input.DeadLetterConfig = &lambdatypes.DeadLetterConfig{
            TargetArn: &spec.DeadLetterConfig.TargetArn,
        }
    }
    if spec.TracingConfig != nil {
        input.TracingConfig = &lambdatypes.TracingConfig{
            Mode: lambdatypes.TracingMode(spec.TracingConfig.Mode),
        }
    }
    if spec.EphemeralStorage != nil {
        input.EphemeralStorage = &lambdatypes.EphemeralStorage{
            Size: &spec.EphemeralStorage.Size,
        }
    }

    out, err := r.client.CreateFunction(ctx, input)
    if err != nil {
        return LambdaFunctionOutputs{}, err
    }

    return outputsFromCreateResponse(out), nil
}
```

#### WaitForUpdateComplete

```go
func (r *realLambdaAPI) WaitForUpdateComplete(ctx context.Context, functionName string) error {
    waiter := lambda.NewFunctionUpdatedV2Waiter(r.client)
    return waiter.Wait(ctx, &lambda.GetFunctionConfigurationInput{
        FunctionName: &functionName,
    }, 2*time.Minute)
}
```

The AWS SDK v2 provides a built-in waiter for `FunctionUpdatedV2` that polls
`GetFunctionConfiguration` until `LastUpdateStatus` is `Successful`. The 2-minute
timeout is generous — most configuration updates complete in seconds, but VPC
attachment can take 30-60 seconds.

---

## Step 5 — Drift Detection

**File**: `internal/drivers/lambda/drift.go`

### Drift-Detectable Fields

| Field | Drift Source | Notes |
|---|---|---|
| Role | Console, CLI, IaC | Execution role changed externally |
| Runtime | Console, CLI | Runtime version changed |
| Handler | Console, CLI | Entry point changed |
| MemorySize | Console, CLI | Memory allocation changed |
| Timeout | Console, CLI | Timeout value changed |
| Environment | Console, CLI | Env vars added/removed/changed |
| Layers | Console, CLI | Layer ARNs changed |
| VPC Config | Console, CLI | Subnets or security groups changed |
| Dead Letter Config | Console, CLI | DLQ target changed |
| Tracing Config | Console, CLI | X-Ray mode changed |
| Architectures | Console, CLI | Rare but detectable |
| Ephemeral Storage | Console, CLI | /tmp size changed |
| Description | Console, CLI | Description text changed |
| Tags | Console, CLI, other tools | Tags added/removed/changed |

### Fields NOT Drift-Detected

- **Code hash**: Code is deployed by Praxis, not tracked for external drift.
  If someone deploys code outside Praxis, the next `Provision` with updated code
  spec will overwrite it. Detecting code drift would require storing and comparing
  SHA256 hashes, which adds complexity for a rare scenario.
- **Concurrency settings**: Out of scope for this driver.
- **Aliases and versions**: Out of scope for this driver.

### HasDrift

```go
func HasDrift(desired LambdaFunctionSpec, observed ObservedState) bool {
    if desired.Role != observed.Role { return true }
    if desired.Runtime != observed.Runtime { return true }
    if desired.Handler != observed.Handler { return true }
    if desired.MemorySize != observed.MemorySize { return true }
    if desired.Timeout != observed.Timeout { return true }
    if desired.Description != observed.Description { return true }
    if !slicesEqual(desired.Architectures, observed.Architectures) { return true }
    if !envMatch(desired.Environment, observed.Environment) { return true }
    if !slicesEqual(desired.Layers, observed.Layers) { return true }
    if !vpcConfigMatch(desired.VpcConfig, observed) { return true }
    if !dlqMatch(desired.DeadLetterConfig, observed.DeadLetterArn) { return true }
    if !tracingMatch(desired.TracingConfig, observed.TracingMode) { return true }
    if !ephemeralMatch(desired.EphemeralStorage, observed.EphemeralSize) { return true }
    if !tagsMatch(desired.Tags, observed.Tags) { return true }
    return false
}
```

### ComputeFieldDiffs

```go
func ComputeFieldDiffs(desired LambdaFunctionSpec, observed ObservedState) []types.FieldDiff {
    var diffs []types.FieldDiff
    if desired.Role != observed.Role {
        diffs = append(diffs, types.FieldDiff{Field: "role", Desired: desired.Role, Observed: observed.Role})
    }
    if desired.Runtime != observed.Runtime {
        diffs = append(diffs, types.FieldDiff{Field: "runtime", Desired: desired.Runtime, Observed: observed.Runtime})
    }
    // ... similar for all drift-detectable fields
    return diffs
}
```

---

## Step 6 — Driver Implementation

**File**: `internal/drivers/lambda/driver.go`

### Constructor

```go
type LambdaFunctionDriver struct {
    auth       authservice.AuthClient
    apiFactory func(aws.Config) LambdaAPI
}

func NewLambdaFunctionDriver(auth authservice.AuthClient) *LambdaFunctionDriver {
    return NewLambdaFunctionDriverWithFactory(auth, func(cfg aws.Config) LambdaAPI {
        return NewLambdaAPI(awsclient.NewLambdaClient(cfg))
    })
}

func NewLambdaFunctionDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) LambdaAPI) *LambdaFunctionDriver {
    if factory == nil {
        factory = func(cfg aws.Config) LambdaAPI { return NewLambdaAPI(awsclient.NewLambdaClient(cfg)) }
    }
    return &LambdaFunctionDriver{auth: auth, apiFactory: factory}
}

func (d *LambdaFunctionDriver) ServiceName() string {
    return ServiceName
}
```

### Provision

Provision follows a two-phase approach: create vs. update.

```go
func (d *LambdaFunctionDriver) Provision(ctx restate.ObjectContext, spec LambdaFunctionSpec) (LambdaFunctionOutputs, error) {
    // 1. Load existing state
    state, _ := restate.Get[*LambdaFunctionState](ctx, drivers.StateKey)

    // 2. Build API client
    api := d.buildAPI(spec.Account, spec.Region)

    // 3. If no existing state → CreateFunction
    if state == nil || state.Outputs.FunctionArn == "" {
        return d.createFunction(ctx, api, spec)
    }

    // 4. Existing function → check for code changes, then config changes
    return d.updateFunction(ctx, api, spec, state)
}
```

#### Create Flow

```go
func (d *LambdaFunctionDriver) createFunction(ctx restate.ObjectContext, api LambdaAPI, spec LambdaFunctionSpec) (LambdaFunctionOutputs, error) {
    // Write pending state
    restate.Set(ctx, drivers.StateKey, &LambdaFunctionState{
        Desired: spec,
        Status:  types.StatusProvisioning,
        Mode:    drivers.DefaultMode(""),
    })

    // Create function (journaled via restate.Run)
    outputs, err := restate.Run(ctx, func(rc restate.RunContext) (LambdaFunctionOutputs, error) {
        return api.CreateFunction(rc, spec)
    })
    if err != nil {
        if isConflict(err) {
            return LambdaFunctionOutputs{}, restate.TerminalError(
                fmt.Errorf("function %q already exists in %s", spec.FunctionName, spec.Region), 409)
        }
        return LambdaFunctionOutputs{}, err
    }

    // Describe to populate full observed state
    observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
        return api.GetFunction(rc, spec.FunctionName)
    })
    if err != nil {
        return LambdaFunctionOutputs{}, err
    }

    // Write final state
    restate.Set(ctx, drivers.StateKey, &LambdaFunctionState{
        Desired:  spec,
        Observed: observed,
        Outputs:  outputs,
        Status:   types.StatusReady,
        Mode:     types.ModeManaged,
        Generation: 1,
    })

    // Schedule first reconciliation
    d.scheduleReconcile(ctx)

    return outputs, nil
}
```

#### Update Flow (Dual-Phase)

```go
func (d *LambdaFunctionDriver) updateFunction(ctx restate.ObjectContext, api LambdaAPI, spec LambdaFunctionSpec, state *LambdaFunctionState) (LambdaFunctionOutputs, error) {
    state.Desired = spec
    state.Status = types.StatusProvisioning
    state.Generation++
    restate.Set(ctx, drivers.StateKey, state)

    codeChanged := codeSpecChanged(spec.Code, state.Observed)
    configChanged := configChanged(spec, state.Observed)

    // Phase 1: Update code if changed
    if codeChanged {
        if _, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
            return api.UpdateFunctionCode(rc, spec.FunctionName, spec.Code)
        }); err != nil {
            return LambdaFunctionOutputs{}, err
        }

        // Wait for code update to complete
        if err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
            return restate.Void{}, api.WaitForUpdateComplete(rc, spec.FunctionName)
        }); err != nil {
            return LambdaFunctionOutputs{}, err
        }
    }

    // Phase 2: Update configuration if changed
    if configChanged {
        if err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
            return restate.Void{}, api.UpdateFunctionConfiguration(rc, spec.FunctionName, spec)
        }); err != nil {
            return LambdaFunctionOutputs{}, err
        }

        // Wait for config update to complete
        if err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
            return restate.Void{}, api.WaitForUpdateComplete(rc, spec.FunctionName)
        }); err != nil {
            return LambdaFunctionOutputs{}, err
        }
    }

    // Phase 3: Update tags if changed
    if !tagsMatch(spec.Tags, state.Observed.Tags) {
        if err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
            return restate.Void{}, syncTags(rc, api, state.Outputs.FunctionArn, state.Observed.Tags, spec.Tags)
        }); err != nil {
            return LambdaFunctionOutputs{}, err
        }
    }

    // Describe to refresh observed state
    observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
        return api.GetFunction(rc, spec.FunctionName)
    })
    if err != nil {
        return LambdaFunctionOutputs{}, err
    }

    outputs := outputsFromObserved(observed)
    state.Observed = observed
    state.Outputs = outputs
    state.Status = types.StatusReady
    state.Error = ""
    restate.Set(ctx, drivers.StateKey, state)

    return outputs, nil
}
```

### Import

```go
func (d *LambdaFunctionDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (LambdaFunctionOutputs, error) {
    api := d.buildAPI(ref.Account, ref.Region)

    observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
        return api.GetFunction(rc, ref.ResourceID)
    })
    if err != nil {
        if isNotFound(err) {
            return LambdaFunctionOutputs{}, restate.TerminalError(
                fmt.Errorf("function %q not found in %s", ref.ResourceID, ref.Region), 404)
        }
        return LambdaFunctionOutputs{}, err
    }

    spec := specFromObserved(observed, ref)
    outputs := outputsFromObserved(observed)
    mode := types.ModeObserved
    if ref.Mode != "" {
        mode = ref.Mode
    }

    restate.Set(ctx, drivers.StateKey, &LambdaFunctionState{
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
func (d *LambdaFunctionDriver) Delete(ctx restate.ObjectContext) error {
    state, err := restate.Get[*LambdaFunctionState](ctx, drivers.StateKey)
    if err != nil {
        return err
    }
    if state == nil {
        return nil // already deleted or never created
    }
    if state.Mode == types.ModeObserved {
        return restate.TerminalError(fmt.Errorf("cannot delete observed resource"), 403)
    }

    state.Status = types.StatusDeleting
    restate.Set(ctx, drivers.StateKey, state)

    api := d.buildAPI(state.Desired.Account, state.Desired.Region)

    if _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
        return restate.Void{}, api.DeleteFunction(rc, state.Desired.FunctionName)
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
func (d *LambdaFunctionDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
    state, err := restate.Get[*LambdaFunctionState](ctx, drivers.StateKey)
    if err != nil {
        return types.ReconcileResult{}, err
    }
    if state == nil {
        return types.ReconcileResult{Status: "no-state"}, nil
    }

    state.ReconcileScheduled = false
    api := d.buildAPI(state.Desired.Account, state.Desired.Region)

    // Describe current state
    observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
        return api.GetFunction(rc, state.Desired.FunctionName)
    })
    if err != nil {
        if isNotFound(err) {
            state.Status = types.StatusError
            state.Error = "function not found in AWS — may have been deleted externally"
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
        // Correct drift by re-applying configuration
        if err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
            return restate.Void{}, api.UpdateFunctionConfiguration(rc, state.Desired.FunctionName, state.Desired)
        }); err != nil {
            state.Error = fmt.Sprintf("drift correction failed: %v", err)
            state.Status = types.StatusError
            restate.Set(ctx, drivers.StateKey, state)
            d.scheduleReconcile(ctx)
            return types.ReconcileResult{Status: "error", Error: state.Error}, nil
        }

        // Wait for update to complete
        if err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
            return restate.Void{}, api.WaitForUpdateComplete(rc, state.Desired.FunctionName)
        }); err != nil {
            state.Error = fmt.Sprintf("drift correction wait failed: %v", err)
            state.Status = types.StatusError
            restate.Set(ctx, drivers.StateKey, state)
            d.scheduleReconcile(ctx)
            return types.ReconcileResult{Status: "error", Error: state.Error}, nil
        }

        // Sync tags if drifted
        if !tagsMatch(state.Desired.Tags, observed.Tags) {
            if err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
                return restate.Void{}, syncTags(rc, api, state.Outputs.FunctionArn, observed.Tags, state.Desired.Tags)
            }); err != nil {
                // Tag sync failure is non-fatal; log and continue
            }
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

```go
func (d *LambdaFunctionDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
    state, err := restate.Get[*LambdaFunctionState](ctx, drivers.StateKey)
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

func (d *LambdaFunctionDriver) GetOutputs(ctx restate.ObjectSharedContext) (LambdaFunctionOutputs, error) {
    state, err := restate.Get[*LambdaFunctionState](ctx, drivers.StateKey)
    if err != nil {
        return LambdaFunctionOutputs{}, err
    }
    if state == nil {
        return LambdaFunctionOutputs{}, nil
    }
    return state.Outputs, nil
}
```

### Helper: scheduleReconcile

```go
func (d *LambdaFunctionDriver) scheduleReconcile(ctx restate.ObjectContext) {
    state, _ := restate.Get[*LambdaFunctionState](ctx, drivers.StateKey)
    if state != nil && state.ReconcileScheduled {
        return // deduplication guard
    }
    restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").
        Send(nil, restate.WithDelay(drivers.ReconcileInterval))
    if state != nil {
        state.ReconcileScheduled = true
        restate.Set(ctx, drivers.StateKey, state)
    }
}
```

---

## Step 7 — Provider Adapter

**File**: `internal/core/provider/lambda_adapter.go`

```go
type LambdaFunctionAdapter struct {
    accounts *auth.Registry
}

func NewLambdaFunctionAdapterWithRegistry(accounts *auth.Registry) *LambdaFunctionAdapter {
    return &LambdaFunctionAdapter{accounts: accounts}
}

func (a *LambdaFunctionAdapter) Kind() string { return lambda.ServiceName }

func (a *LambdaFunctionAdapter) Scope() KeyScope { return KeyScopeRegion }

func (a *LambdaFunctionAdapter) BuildKey(doc json.RawMessage) (string, error) {
    region, _ := jsonpath.String(doc.Spec, "region")
    name := doc.Metadata.Name
    if region == "" || name == "" {
        return "", fmt.Errorf("LambdaFunction requires spec.region and metadata.name")
    }
    return region + "~" + name, nil
}

func (a *LambdaFunctionAdapter) BuildImportKey(region, resourceID string) (string, error) {
    return region + "~" + resourceID, nil
}

func (a *LambdaFunctionAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
    // Decode desired spec
    spec, err := decodeSpec(doc)
    if err != nil {
        return types.PlanResult{}, err
    }

    // If no current outputs, resource doesn't exist → OpCreate
    if len(currentOutputs) == 0 {
        return types.PlanResult{Op: types.OpCreate, Spec: spec}, nil
    }

    // Describe current state via Lambda API for drift comparison
    cfg, err := a.accounts.GetConfig(spec.Account, spec.Region)
    if err != nil {
        return types.PlanResult{}, err
    }
    client := awsclient.NewLambdaClient(cfg)
    api := newRealLambdaAPI(client, ratelimit.New("lambda-function", 15, 10))

    observed, err := api.GetFunction(ctx, spec.FunctionName)
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

Add `NewLambdaFunctionAdapterWithRegistry(accounts)` to the `NewRegistry()` function's
adapter list.

---

## Step 9 — Compute Driver Pack Entry Point

**File**: `cmd/praxis-compute/main.go` — **MODIFY**

Add Lambda Function driver binding:

```go
import "github.com/shirvan/praxis/internal/drivers/lambda"

srv := server.NewRestate().
    Bind(restate.Reflect(ami.NewAMIDriver(cfg.Auth()))).
    Bind(restate.Reflect(keypair.NewKeyPairDriver(cfg.Auth()))).
    Bind(restate.Reflect(ec2.NewEC2InstanceDriver(cfg.Auth()))).
    Bind(restate.Reflect(lambda.NewLambdaFunctionDriver(cfg.Auth())))
```

---

## Step 10 — Docker Compose & Justfile

### Docker Compose

No changes needed — Lambda drivers are hosted in the existing `praxis-compute`
container which is already exposed on port 9084.

### Justfile Additions

```just
test-lambda:
    go test ./internal/drivers/lambda/... -v -count=1 -race

test-lambda-integration:
    go test ./tests/integration/... -run TestLambdaFunction -v -timeout=5m
```

---

## Step 11 — Unit Tests

**File**: `internal/drivers/lambda/driver_test.go`

### Test Cases

| Test | Description |
|---|---|
| `TestProvision_NewFunction` | Creates a new function; verifies outputs, state, and reconcile scheduled |
| `TestProvision_UpdateCode` | Changes code source; verifies dual-phase update (code → wait → describe) |
| `TestProvision_UpdateConfig` | Changes memory/timeout; verifies config update and wait |
| `TestProvision_UpdateCodeAndConfig` | Changes both; verifies sequential code → config update |
| `TestProvision_Conflict` | Create returns ResourceConflictException; verifies terminal 409 error |
| `TestProvision_InvalidParam` | Create returns InvalidParameterValueException; verifies terminal error |
| `TestImport_Success` | Imports existing function; verifies observed-mode state and outputs |
| `TestImport_NotFound` | Import of non-existent function; verifies terminal 404 error |
| `TestDelete_Managed` | Deletes managed function; verifies state cleanup |
| `TestDelete_Observed` | Attempts delete of observed function; verifies terminal 403 error |
| `TestDelete_AlreadyGone` | Delete when function already deleted; verifies idempotent success |
| `TestReconcile_NoDrift` | No drift detected; verifies status remains Ready |
| `TestReconcile_DriftDetected_Managed` | Drift detected in managed mode; verifies correction |
| `TestReconcile_DriftDetected_Observed` | Drift detected in observed mode; verifies report-only |
| `TestReconcile_FunctionGone` | Function deleted externally; verifies error status |
| `TestGetStatus_NoState` | No state exists; verifies Pending status |
| `TestGetOutputs_NoState` | No state exists; verifies empty outputs |

**File**: `internal/drivers/lambda/drift_test.go`

| Test | Description |
|---|---|
| `TestHasDrift_NoDrift` | Identical desired and observed; verifies no drift |
| `TestHasDrift_RoleChanged` | Role ARN changed; verifies drift detected |
| `TestHasDrift_MemoryChanged` | Memory size changed; verifies drift detected |
| `TestHasDrift_EnvChanged` | Environment variable added; verifies drift detected |
| `TestHasDrift_LayersChanged` | Layer ARN list changed; verifies drift detected |
| `TestHasDrift_VpcConfigChanged` | Subnet IDs changed; verifies drift detected |
| `TestHasDrift_TagsChanged` | Tag added/removed; verifies drift detected |
| `TestComputeFieldDiffs_MultipleChanges` | Multiple fields drifted; verifies all diffs reported |

---

## Step 12 — Integration Tests

**File**: `tests/integration/lambda_driver_test.go`

Integration tests use Testcontainers to spin up Moto with Lambda support.

### Test Cases

| Test | Description |
|---|---|
| `TestLambdaFunction_CreateAndDescribe` | Create function, verify outputs match GetFunction |
| `TestLambdaFunction_UpdateCode` | Update code S3 location, verify new code hash |
| `TestLambdaFunction_UpdateConfiguration` | Update memory/timeout, verify configuration change |
| `TestLambdaFunction_DualUpdate` | Update code and config in single Provision, verify both applied |
| `TestLambdaFunction_Import` | Create function via AWS API, import via driver, verify state |
| `TestLambdaFunction_Delete` | Create then delete, verify function gone |
| `TestLambdaFunction_Reconcile_NoDrift` | Create function, reconcile, verify no changes |
| `TestLambdaFunction_Reconcile_DriftCorrection` | Create, externally modify, reconcile, verify correction |
| `TestLambdaFunction_VpcAttachment` | Create function with VPC config, verify VPC state |

### Moto Considerations

- Moto Community Edition supports Lambda (including function creation,
  invocation, and basic configuration). However, some features like layers,
  VPC attachment, and code signing have limited support.
- Integration tests should tag functions with cleanup markers and use a test
  teardown to delete all test-created functions.
- Lambda functions in Moto require a deployment package. Tests should use
  a minimal ZIP file with a no-op handler.

---

## Lambda-Function-Specific Design Decisions

### 1. Code Change Detection

**Decision**: Compare S3 bucket + key + objectVersion (or imageUri, or zipFile hash)
to detect code changes. Do NOT compare `CodeSha256` from GetFunction against a
locally computed hash.

**Rationale**: The user declares code source (S3 location or zip), not the hash.
Comparing hashes would require downloading and hashing the S3 object, which is
expensive and unreliable (the object could change between check and deploy). Instead,
if the code spec fields differ from the last-provisioned code spec, the driver calls
`UpdateFunctionCode`.

### 2. VPC Attachment Latency

**Decision**: The driver uses `WaitForUpdateComplete` after any configuration change
that includes VPC settings. VPC attachments create ENIs in the specified subnets,
which can take 30-60 seconds.

**Rationale**: Without waiting, a subsequent Provision or Reconcile call could hit
`ResourceConflictException` because the function is still in an updating state.

### 3. Environment Variable Handling

**Decision**: Empty environment map and nil environment are treated as equivalent.
The driver normalizes empty maps to nil before comparison.

**Rationale**: AWS returns an empty `Environment.Variables` map even when no
environment variables are set. Without normalization, every reconciliation would
detect false drift.

### 4. Layer Version ARN Comparison

**Decision**: Layer references are compared as exact ARN strings including version
number.

**Rationale**: Lambda layer ARNs include the version number
(`arn:aws:lambda:us-east-1:123456789012:layer:my-layer:3`). Changing the version
number in the spec triggers an update, which is the correct behavior — the user
is explicitly selecting a new layer version.

### 5. Tag Synchronization

**Decision**: Tags are synced as a full-replace operation using `TagResource` and
`UntagResource`. The driver computes the diff (keys to add/update, keys to remove)
and makes both calls.

**Rationale**: Lambda tags API requires separate tag/untag calls (no single
put-replace). The driver wraps both calls in a single `restate.Run()` block for
atomicity within the journal.

### 6. Import Default Mode

**Decision**: Import defaults to `ModeObserved`.

**Rationale**: Lambda functions are mutable, stateful compute resources. Like EC2
instances, importing a function should default to observation-only to prevent
accidental modification of a production function. Users can explicitly set
`--mode managed` to opt into full lifecycle management.

### 7. Delete Behavior

**Decision**: `DeleteFunction` removes the function and all versions/aliases in a
single API call.

**Rationale**: AWS `DeleteFunction` without a qualifier deletes the function and all
its versions and aliases. This is the correct behavior for a full lifecycle delete.
The driver does not need to iterate versions.

### 8. Architectures as Singleton List

**Decision**: Store architectures as `[]string` but the CUE schema constrains it
to exactly one element.

**Rationale**: The AWS API models architectures as a list for forward compatibility,
but currently only supports one value (`x86_64` or `arm64`). The driver stores it
as-is from the API response for fidelity.

---

## Checklist

### Implementation

- [ ] `schemas/aws/lambda/function.cue`
- [ ] `internal/drivers/lambda/types.go`
- [ ] `internal/drivers/lambda/aws.go`
- [ ] `internal/drivers/lambda/drift.go`
- [ ] `internal/drivers/lambda/driver.go`
- [ ] `internal/core/provider/lambda_adapter.go`

### Tests

- [ ] `internal/drivers/lambda/driver_test.go`
- [ ] `internal/drivers/lambda/aws_test.go`
- [ ] `internal/drivers/lambda/drift_test.go`
- [ ] `internal/core/provider/lambda_adapter_test.go`
- [ ] `tests/integration/lambda_driver_test.go`

### Integration

- [ ] `internal/infra/awsclient/client.go` — Add `NewLambdaClient()`
- [ ] `cmd/praxis-compute/main.go` — Bind driver
- [ ] `internal/core/provider/registry.go` — Register adapter
- [ ] `justfile` — Add test targets
