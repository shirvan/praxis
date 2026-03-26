# CloudWatch Log Group Driver — Implementation Plan

> Target: A Restate Virtual Object driver that manages CloudWatch Log Groups, following
> the exact patterns established by the S3, Security Group, EC2, VPC, EBS, Elastic IP,
> Key Pair, AMI, and Lambda drivers.
>
> Key scope: `KeyScopeRegion` — key format is `region~logGroupName`, permanent and
> immutable for the lifetime of the Virtual Object. The AWS-assigned log group ARN
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
16. [Log-Group-Specific Design Decisions](#log-group-specific-design-decisions)
17. [Checklist](#checklist)

---

## 1. Overview & Scope

The Log Group driver manages the lifecycle of **CloudWatch log groups** only.
Log streams, subscription filters, metric filters, log data (PutLogEvents), export
tasks, and resource policies are separate resources and are not managed by this
driver. This document focuses exclusively on log group creation, retention
configuration, encryption configuration, import, deletion, and drift reconciliation.

The driver follows the established Virtual Object contract:

| Handler | Context | Purpose |
|---|---|---|
| `Provision` | `ObjectContext` (exclusive) | Create or converge a log group |
| `Import` | `ObjectContext` (exclusive) | Adopt an existing log group |
| `Delete` | `ObjectContext` (exclusive) | Delete a log group |
| `Reconcile` | `ObjectContext` (exclusive) | Detect/correct drift (report-only for Observed mode) |
| `GetStatus` | `ObjectSharedContext` (shared) | Return current status |
| `GetOutputs` | `ObjectSharedContext` (shared) | Return log group outputs |

### Mutable vs Immutable Attributes

| Attribute | Mutability | API | Notes |
|---|---|---|---|
| Log group name | Immutable | — | Set at creation; cannot be renamed |
| Log group ARN | Immutable | — | AWS-assigned |
| Log group class | Immutable | — | `STANDARD` or `INFREQUENT_ACCESS`; set at creation, cannot be changed |
| Retention in days | Mutable | `PutRetentionPolicy` / `DeleteRetentionPolicy` | 1, 3, 5, 7, 14, 30, 60, 90, 120, 150, 180, 365, 400, 545, 731, 1096, 1827, 2192, 2557, 2922, 3288, 3653; or `null` for never expire |
| KMS key ID | Mutable | `AssociateKmsKey` / `DisassociateKmsKey` | KMS key ARN for encryption at rest |
| Tags | Mutable | `TagResource` / `UntagResource` | Key-value pairs on the log group ARN |

### Split API Design

CloudWatch Logs uses a **split API** for log group management — similar to how
Lambda separates code updates from config updates, but simpler:

1. **`CreateLogGroup`** — Creates the log group with optional class and KMS key.
   Does NOT accept retention (that's a separate resource in the API model).
2. **`PutRetentionPolicy`** — Sets the retention period on an existing log group.
3. **`DeleteRetentionPolicy`** — Removes the retention policy (logs never expire).
4. **`AssociateKmsKey`** — Associates a KMS key for encryption.
5. **`DisassociateKmsKey`** — Removes KMS encryption (reverts to default SSE).

The driver handles these as sequential calls within `Provision`, wrapped in
`restate.Run()` for durable journaling.

### What Is NOT In Scope

- **Log Streams**: Individual log streams within a group. Managed by applications
  producing logs or by AWS services (e.g., Lambda).
- **Subscription Filters**: Real-time log forwarding to Lambda, Kinesis, Firehose.
  Future extension.
- **Metric Filters**: Extracting CloudWatch metrics from log data. Future extension.
- **Log Data**: The actual log events. Not a driver concern.
- **Export Tasks**: S3 export of log data. One-time operations, not resource lifecycle.
- **Resource Policies**: Cross-account log delivery policies. Future extension.
- **Log Anomaly Detectors**: ML-based anomaly detection. Future extension.

### Downstream Consumers

```text
${resources.my-log-group.outputs.arn}              → Subscription filters, metric filters, IAM policies
${resources.my-log-group.outputs.logGroupName}     → Lambda function configuration, application config
${resources.my-log-group.outputs.retentionInDays}  → Compliance reporting
```

---

## 2. Key Strategy

### Key Format: `region~logGroupName`

CloudWatch log group names are unique within a region+account. Log group names
can contain forward slashes (e.g., `/aws/lambda/my-function`), which is different
from most other resource names. The CUE schema maps `metadata.name` to the log
group name. The adapter produces `region~metadata.name` as the Virtual Object key.

1. **BuildKey** (adapter, plan-time): returns `region~metadata.name`.
2. **Provision / Delete**: dispatched to the same VO key.
3. **Plan**: reads VO state via `GetOutputs`. If outputs contain an ARN,
   describes the log group by name. Otherwise, `OpCreate`.
4. **Import**: `BuildImportKey(region, resourceID)` returns `region~resourceID`
   where `resourceID` is the log group name. Same key as BuildKey because log
   group names are AWS-unique per region.

### Log Group Names with Slashes

Log group names commonly contain slashes (`/aws/lambda/...`, `/app/myservice/...`).
The Virtual Object key `region~logGroupName` includes the full name with slashes.
This is safe because the key delimiter is `~`, which does not appear in log group
names (AWS restricts log group names to `[\.\-_/#A-Za-z0-9]+`).

### Ownership Tags

Log group names are unique within a region+account, so AWS rejects duplicate
`CreateLogGroup` calls with `ResourceAlreadyExistsException`. The driver adds
`praxis:managed-key=<region~logGroupName>` as a tag for consistency with other
drivers and to provide cross-Praxis-installation conflict detection.

**FindByManagedKey** is NOT needed because log group names are AWS-enforced unique.

### Import Semantics

Import and template-based management produce the **same Virtual Object key** because
log group names are globally unique within a region:

- `praxis import --kind LogGroup --region us-east-1 --resource-id /app/myservice`:
  Creates VO key `us-east-1~/app/myservice`.
- Template with `metadata.name: "/app/myservice"` in `us-east-1`:
  Creates VO key `us-east-1~/app/myservice`.

Both target the same Virtual Object.

**Auto-created log groups**: Many AWS services automatically create log groups (e.g.,
Lambda creates `/aws/lambda/<functionName>`). Importing these groups is a common use
case. The driver should support `--observe` mode on import to avoid modifying
retention or encryption settings that may have been configured externally.

---

## 3. File Inventory

Create or modify these files (✦ = new file, ✎ = modify existing):

```text
✦ internal/drivers/loggroup/types.go                — Spec, Outputs, ObservedState, State structs
✦ internal/drivers/loggroup/aws.go                  — LogGroupAPI interface + realLogGroupAPI impl
✦ internal/drivers/loggroup/drift.go                — HasDrift(), ComputeFieldDiffs()
✦ internal/drivers/loggroup/driver.go               — LogGroupDriver Virtual Object
✦ internal/drivers/loggroup/driver_test.go          — Unit tests for driver (mocked AWS)
✦ internal/drivers/loggroup/aws_test.go             — Unit tests for error classification
✦ internal/drivers/loggroup/drift_test.go           — Unit tests for drift detection
✦ internal/core/provider/loggroup_adapter.go        — LogGroupAdapter implementing provider.Adapter
✦ internal/core/provider/loggroup_adapter_test.go   — Unit tests for adapter
✦ schemas/aws/cloudwatch/log_group.cue              — CUE schema for LogGroup resource
✦ tests/integration/loggroup_driver_test.go         — Integration tests (Testcontainers + LocalStack)
✎ internal/infra/awsclient/client.go                — Add NewCloudWatchLogsClient()
✎ cmd/praxis-monitoring/main.go                     — Bind LogGroup driver
✎ internal/core/provider/registry.go                — Add adapter to NewRegistry()
✎ justfile                                          — Add loggroup test targets
```

---

## Step 1 — CUE Schema

**File**: `schemas/aws/cloudwatch/log_group.cue`

```cue
package cloudwatch

#LogGroup: {
    apiVersion: "praxis.io/v1"
    kind:       "LogGroup"

    metadata: {
        // name is the CloudWatch log group name in AWS.
        // Can contain: a-zA-Z0-9 . - _ / #
        // Commonly uses path-style names like "/app/myservice" or "/aws/lambda/my-fn".
        name: string & =~"^[\\.\\-_/#A-Za-z0-9]{1,512}$"
        labels: [string]: string
    }

    spec: {
        // region is the AWS region to create the log group in.
        region: string

        // account is the optional AWS account alias for credential resolution.
        account?: string

        // logGroupClass determines the storage class for the log group.
        // STANDARD provides full feature access. INFREQUENT_ACCESS provides
        // reduced cost with limited query and metric extraction capabilities.
        // Cannot be changed after creation.
        logGroupClass: "STANDARD" | "INFREQUENT_ACCESS" | *"STANDARD"

        // retentionInDays is the number of days to retain log events.
        // If omitted, log events are retained indefinitely (never expire).
        // Must be one of the AWS-supported values.
        retentionInDays?: 1 | 3 | 5 | 7 | 14 | 30 | 60 | 90 | 120 | 150 |
                          180 | 365 | 400 | 545 | 731 | 1096 | 1827 | 2192 |
                          2557 | 2922 | 3288 | 3653

        // kmsKeyId is the ARN of the KMS key to use for encrypting log data.
        // If omitted, the log group uses default server-side encryption (SSE).
        kmsKeyId?: string

        // tags on the log group resource.
        tags: [string]: string
    }

    outputs?: {
        arn:             string
        logGroupName:    string
        logGroupClass:   string
        retentionInDays: int
        kmsKeyId:        string
        creationTime:    int
        storedBytes:     int
    }
}
```

### Schema Design Notes

- **`name` regex**: Log group names support a surprisingly broad character set
  including `/`, `#`, `.`, `-`, and `_`. Max length is 512 characters.
- **`retentionInDays` enum**: AWS only accepts specific values. The CUE schema
  enforces this at template time, avoiding runtime errors from the API.
- **`logGroupClass` default**: Defaults to `STANDARD`. The `INFREQUENT_ACCESS`
  class is cheaper but doesn't support real-time log processing (subscription
  filters), metric filters, or contributor insights.
- **`kmsKeyId` optional**: Omitting KMS uses AWS-managed SSE. Customer-managed
  KMS keys provide additional control but require KMS key policy configuration.

---

## Step 2 — AWS Client Factory

**File**: `internal/infra/awsclient/client.go` — **ADD NewCloudWatchLogsClient()**

```go
import "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"

// NewCloudWatchLogsClient creates a CloudWatch Logs API client from the given AWS config.
func NewCloudWatchLogsClient(cfg aws.Config) *cloudwatchlogs.Client {
    return cloudwatchlogs.NewFromConfig(cfg)
}
```

This follows the exact pattern of `NewEC2Client()` and `NewS3Client()`.

---

## Step 3 — Driver Types

**File**: `internal/drivers/loggroup/types.go`

```go
package loggroup

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate Virtual Object name for CloudWatch Log Groups.
const ServiceName = "LogGroup"

// LogGroupSpec is the desired state for a CloudWatch log group.
type LogGroupSpec struct {
    Account         string            `json:"account,omitempty"`
    Region          string            `json:"region"`
    LogGroupName    string            `json:"logGroupName"`
    LogGroupClass   string            `json:"logGroupClass"`
    RetentionInDays *int32            `json:"retentionInDays,omitempty"`
    KmsKeyId        string            `json:"kmsKeyId,omitempty"`
    Tags            map[string]string `json:"tags,omitempty"`
    ManagedKey      string            `json:"managedKey,omitempty"`
}

// LogGroupOutputs is produced after provisioning and stored in Restate K/V.
type LogGroupOutputs struct {
    Arn             string `json:"arn"`
    LogGroupName    string `json:"logGroupName"`
    LogGroupClass   string `json:"logGroupClass"`
    RetentionInDays int32  `json:"retentionInDays"`
    KmsKeyId        string `json:"kmsKeyId,omitempty"`
    CreationTime    int64  `json:"creationTime"`
    StoredBytes     int64  `json:"storedBytes"`
}

// ObservedState captures the actual configuration of a log group from AWS.
type ObservedState struct {
    Arn             string            `json:"arn"`
    LogGroupName    string            `json:"logGroupName"`
    LogGroupClass   string            `json:"logGroupClass"`
    RetentionInDays *int32            `json:"retentionInDays,omitempty"`
    KmsKeyId        string            `json:"kmsKeyId,omitempty"`
    CreationTime    int64             `json:"creationTime"`
    StoredBytes     int64             `json:"storedBytes"`
    Tags            map[string]string `json:"tags,omitempty"`
}

// LogGroupState is the single atomic state object stored under drivers.StateKey.
type LogGroupState struct {
    Desired            LogGroupSpec         `json:"desired"`
    Observed           ObservedState        `json:"observed"`
    Outputs            LogGroupOutputs      `json:"outputs"`
    Status             types.ResourceStatus `json:"status"`
    Mode               types.Mode           `json:"mode"`
    Error              string               `json:"error,omitempty"`
    Generation         int64                `json:"generation"`
    LastReconcile      string               `json:"lastReconcile,omitempty"`
    ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
```

### Types Design Notes

- **`RetentionInDays` is `*int32`**: A nil pointer means "never expire" (no retention
  policy set). This is semantically different from 0. When the user omits
  `retentionInDays` in CUE, the driver calls `DeleteRetentionPolicy`.
- **`ObservedState.RetentionInDays` is also `*int32`**: AWS returns `retentionInDays`
  as 0 or omits it entirely when no retention policy is set. The driver normalizes
  this to nil for consistent drift comparison.
- **`LogGroupClass`**: Stored as a string (`"STANDARD"` or `"INFREQUENT_ACCESS"`).
  Immutable after creation — if the desired class differs from observed, this is a
  terminal error (must delete and recreate).

---

## Step 4 — AWS API Abstraction Layer

**File**: `internal/drivers/loggroup/aws.go`

### LogGroupAPI Interface

```go
type LogGroupAPI interface {
    // CreateLogGroup creates a new CloudWatch log group.
    CreateLogGroup(ctx context.Context, spec LogGroupSpec) error

    // DescribeLogGroup returns the current configuration of a log group.
    // Returns (nil, nil) if the log group does not exist.
    DescribeLogGroup(ctx context.Context, logGroupName string) (*ObservedState, error)

    // PutRetentionPolicy sets the retention period on a log group.
    PutRetentionPolicy(ctx context.Context, logGroupName string, retentionInDays int32) error

    // DeleteRetentionPolicy removes the retention policy (logs never expire).
    DeleteRetentionPolicy(ctx context.Context, logGroupName string) error

    // AssociateKmsKey associates a KMS key for encryption.
    AssociateKmsKey(ctx context.Context, logGroupName string, kmsKeyId string) error

    // DisassociateKmsKey removes KMS encryption.
    DisassociateKmsKey(ctx context.Context, logGroupName string) error

    // DeleteLogGroup deletes the log group and all its log streams.
    DeleteLogGroup(ctx context.Context, logGroupName string) error

    // TagResource adds or overwrites tags on the log group.
    TagResource(ctx context.Context, logGroupArn string, tags map[string]string) error

    // UntagResource removes tag keys from the log group.
    UntagResource(ctx context.Context, logGroupArn string, keys []string) error

    // ListTagsForResource returns all tags on the log group.
    ListTagsForResource(ctx context.Context, logGroupArn string) (map[string]string, error)
}
```

### Implementation: realLogGroupAPI

```go
type realLogGroupAPI struct {
    client  *cloudwatchlogs.Client
    limiter ratelimit.Limiter
}

func newRealLogGroupAPI(client *cloudwatchlogs.Client, limiter ratelimit.Limiter) LogGroupAPI {
    return &realLogGroupAPI{client: client, limiter: limiter}
}
```

### Error Classification

```go
func isNotFound(err error) bool {
    var rnf *cwltypes.ResourceNotFoundException
    return errors.As(err, &rnf)
}

func isAlreadyExists(err error) bool {
    var rae *cwltypes.ResourceAlreadyExistsException
    return errors.As(err, &rae)
}

func isInvalidParam(err error) bool {
    var ipe *cwltypes.InvalidParameterException
    return errors.As(err, &ipe)
}

func isThrottled(err error) bool {
    var te *cwltypes.ThrottlingException
    return errors.As(err, &te)
}

func isLimitExceeded(err error) bool {
    var le *cwltypes.LimitExceededException
    return errors.As(err, &le)
}

func isServiceUnavailable(err error) bool {
    var su *cwltypes.ServiceUnavailableException
    return errors.As(err, &su)
}
```

### Key Implementation Details

#### CreateLogGroup

```go
func (r *realLogGroupAPI) CreateLogGroup(ctx context.Context, spec LogGroupSpec) error {
    r.limiter.Wait(ctx)

    input := &cloudwatchlogs.CreateLogGroupInput{
        LogGroupName: &spec.LogGroupName,
    }

    // Set log group class (STANDARD is the default)
    if spec.LogGroupClass != "" && spec.LogGroupClass != "STANDARD" {
        input.LogGroupClass = cwltypes.LogGroupClass(spec.LogGroupClass)
    }

    // Set KMS key if provided
    if spec.KmsKeyId != "" {
        input.KmsKeyId = &spec.KmsKeyId
    }

    // Set tags
    if len(spec.Tags) > 0 {
        input.Tags = spec.Tags
    }

    _, err := r.client.CreateLogGroup(ctx, input)
    return err
}
```

#### DescribeLogGroup

```go
func (r *realLogGroupAPI) DescribeLogGroup(ctx context.Context, logGroupName string) (*ObservedState, error) {
    r.limiter.Wait(ctx)

    out, err := r.client.DescribeLogGroups(ctx, &cloudwatchlogs.DescribeLogGroupsInput{
        LogGroupNameExactMatch: &logGroupName,
    })
    if err != nil {
        return nil, err
    }

    if len(out.LogGroups) == 0 {
        return nil, nil
    }

    lg := out.LogGroups[0]
    observed := &ObservedState{
        Arn:           deref(lg.Arn),
        LogGroupName:  deref(lg.LogGroupName),
        LogGroupClass: string(lg.LogGroupClass),
        CreationTime:  deref(lg.CreationTime),
        StoredBytes:   deref(lg.StoredBytes),
    }

    if lg.RetentionInDays != nil && *lg.RetentionInDays > 0 {
        days := int32(*lg.RetentionInDays)
        observed.RetentionInDays = &days
    }

    if lg.KmsKeyId != nil {
        observed.KmsKeyId = *lg.KmsKeyId
    }

    return observed, nil
}
```

**Note**: `DescribeLogGroups` does NOT return tags. Tags must be fetched separately
via `ListTagsForResource` using the log group ARN. The driver calls both in sequence
during observe/reconcile.

---

## Step 5 — Drift Detection

**File**: `internal/drivers/loggroup/drift.go`

### Drift-Detectable Fields

| Field | Drift Source | Notes |
|---|---|---|
| RetentionInDays | Console, CLI, IaC | Retention period changed externally |
| KmsKeyId | Console, CLI | Encryption key associated/disassociated externally |
| Tags | Console, CLI, other tools | Tags added/removed/changed |

### Fields NOT Drift-Detected

- **Log group class**: Immutable. If it differs from desired, this is a terminal
  error, not drift.
- **Stored bytes**: Read-only metric, not a managed attribute.
- **Creation time**: Read-only metadata.

### HasDrift

```go
func HasDrift(desired LogGroupSpec, observed ObservedState) bool {
    if !retentionMatch(desired.RetentionInDays, observed.RetentionInDays) {
        return true
    }
    if desired.KmsKeyId != observed.KmsKeyId {
        return true
    }
    if !tagsMatch(desired.Tags, observed.Tags) {
        return true
    }
    return false
}
```

### ComputeFieldDiffs

```go
func ComputeFieldDiffs(desired LogGroupSpec, observed ObservedState) []types.FieldDiff {
    var diffs []types.FieldDiff

    if !retentionMatch(desired.RetentionInDays, observed.RetentionInDays) {
        diffs = append(diffs, types.FieldDiff{
            Field:    "retentionInDays",
            Desired:  formatRetention(desired.RetentionInDays),
            Observed: formatRetention(observed.RetentionInDays),
        })
    }

    if desired.KmsKeyId != observed.KmsKeyId {
        diffs = append(diffs, types.FieldDiff{
            Field:    "kmsKeyId",
            Desired:  desired.KmsKeyId,
            Observed: observed.KmsKeyId,
        })
    }

    if !tagsMatch(desired.Tags, observed.Tags) {
        diffs = append(diffs, types.FieldDiff{
            Field:    "tags",
            Desired:  fmt.Sprintf("%v", desired.Tags),
            Observed: fmt.Sprintf("%v", observed.Tags),
        })
    }

    return diffs
}
```

### Retention Matching

```go
// retentionMatch compares desired and observed retention.
// nil means "never expire" (no retention policy).
func retentionMatch(desired *int32, observed *int32) bool {
    if desired == nil && observed == nil {
        return true
    }
    if desired == nil || observed == nil {
        return false
    }
    return *desired == *observed
}
```

---

## Step 6 — Driver Implementation

**File**: `internal/drivers/loggroup/driver.go`

### Constructor

```go
type LogGroupDriver struct {
    accounts   *auth.Registry
    apiFactory func(aws.Config) LogGroupAPI
}

func NewLogGroupDriver(accounts *auth.Registry) *LogGroupDriver {
    return NewLogGroupDriverWithFactory(accounts, func(cfg aws.Config) LogGroupAPI {
        return NewLogGroupAPI(awsclient.NewCloudWatchLogsClient(cfg))
    })
}

func NewLogGroupDriverWithFactory(accounts *auth.Registry, factory func(aws.Config) LogGroupAPI) *LogGroupDriver {
    if accounts == nil {
        accounts = auth.LoadFromEnv()
    }
    if factory == nil {
        factory = func(cfg aws.Config) LogGroupAPI {
            return NewLogGroupAPI(awsclient.NewCloudWatchLogsClient(cfg))
        }
    }
    return &LogGroupDriver{accounts: accounts, apiFactory: factory}
}

func (LogGroupDriver) ServiceName() string { return ServiceName }
```

### Provision

```go
func (d *LogGroupDriver) Provision(ctx restate.ObjectContext, spec LogGroupSpec) (LogGroupOutputs, error) {
    // 1. Load existing state
    state, _ := restate.Get[*LogGroupState](ctx, drivers.StateKey)

    // 2. Build API client
    api := d.buildAPI(spec.Account, spec.Region)

    // 3. If no existing state → CreateLogGroup
    if state == nil || state.Outputs.Arn == "" {
        return d.createLogGroup(ctx, api, spec)
    }

    // 4. Existing log group → converge retention, KMS, tags
    return d.updateLogGroup(ctx, api, spec, state)
}
```

#### Create Flow

```go
func (d *LogGroupDriver) createLogGroup(ctx restate.ObjectContext, api LogGroupAPI, spec LogGroupSpec) (LogGroupOutputs, error) {
    // Write pending state
    restate.Set(ctx, drivers.StateKey, &LogGroupState{
        Desired: spec,
        Status:  types.StatusProvisioning,
        Mode:    drivers.DefaultMode(""),
    })

    // Step 1: Create log group (journaled)
    if _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
        return restate.Void{}, api.CreateLogGroup(rc, spec)
    }); err != nil {
        if isAlreadyExists(err) {
            return LogGroupOutputs{}, restate.TerminalError(
                fmt.Errorf("log group %q already exists in %s", spec.LogGroupName, spec.Region), 409)
        }
        return LogGroupOutputs{}, err
    }

    // Step 2: Set retention policy if specified
    if spec.RetentionInDays != nil {
        if _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
            return restate.Void{}, api.PutRetentionPolicy(rc, spec.LogGroupName, *spec.RetentionInDays)
        }); err != nil {
            return LogGroupOutputs{}, err
        }
    }

    // Step 3: Describe to populate full observed state
    observed, err := restate.Run(ctx, func(rc restate.RunContext) (*ObservedState, error) {
        return api.DescribeLogGroup(rc, spec.LogGroupName)
    })
    if err != nil || observed == nil {
        return LogGroupOutputs{}, fmt.Errorf("failed to describe log group after creation: %w", err)
    }

    // Step 4: Fetch tags separately
    tags, err := restate.Run(ctx, func(rc restate.RunContext) (map[string]string, error) {
        return api.ListTagsForResource(rc, observed.Arn)
    })
    if err != nil {
        return LogGroupOutputs{}, err
    }
    observed.Tags = tags

    outputs := outputsFromObserved(observed)

    // Write final state
    restate.Set(ctx, drivers.StateKey, &LogGroupState{
        Desired:    spec,
        Observed:   *observed,
        Outputs:    outputs,
        Status:     types.StatusReady,
        Mode:       types.ModeManaged,
        Generation: 1,
    })

    // Schedule first reconciliation
    d.scheduleReconcile(ctx)

    return outputs, nil
}
```

#### Update Flow

```go
func (d *LogGroupDriver) updateLogGroup(ctx restate.ObjectContext, api LogGroupAPI, spec LogGroupSpec, state *LogGroupState) (LogGroupOutputs, error) {
    // Check immutable field: logGroupClass
    if spec.LogGroupClass != "" && spec.LogGroupClass != state.Observed.LogGroupClass {
        return LogGroupOutputs{}, restate.TerminalError(
            fmt.Errorf("logGroupClass is immutable: current=%s, desired=%s; delete and recreate the log group",
                state.Observed.LogGroupClass, spec.LogGroupClass), 409)
    }

    state.Desired = spec
    state.Status = types.StatusProvisioning
    state.Generation++
    restate.Set(ctx, drivers.StateKey, state)

    // Converge retention
    if !retentionMatch(spec.RetentionInDays, state.Observed.RetentionInDays) {
        if spec.RetentionInDays != nil {
            if _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
                return restate.Void{}, api.PutRetentionPolicy(rc, spec.LogGroupName, *spec.RetentionInDays)
            }); err != nil {
                return LogGroupOutputs{}, err
            }
        } else {
            if _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
                return restate.Void{}, api.DeleteRetentionPolicy(rc, spec.LogGroupName)
            }); err != nil {
                return LogGroupOutputs{}, err
            }
        }
    }

    // Converge KMS key
    if spec.KmsKeyId != state.Observed.KmsKeyId {
        if spec.KmsKeyId != "" {
            if _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
                return restate.Void{}, api.AssociateKmsKey(rc, spec.LogGroupName, spec.KmsKeyId)
            }); err != nil {
                return LogGroupOutputs{}, err
            }
        } else if state.Observed.KmsKeyId != "" {
            if _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
                return restate.Void{}, api.DisassociateKmsKey(rc, spec.LogGroupName)
            }); err != nil {
                return LogGroupOutputs{}, err
            }
        }
    }

    // Converge tags
    if !tagsMatch(spec.Tags, state.Observed.Tags) {
        if err := convergeTags(ctx, api, state.Outputs.Arn, spec.Tags, state.Observed.Tags); err != nil {
            return LogGroupOutputs{}, err
        }
    }

    // Re-describe to get final state
    observed, err := restate.Run(ctx, func(rc restate.RunContext) (*ObservedState, error) {
        return api.DescribeLogGroup(rc, spec.LogGroupName)
    })
    if err != nil || observed == nil {
        return LogGroupOutputs{}, fmt.Errorf("failed to describe log group after update: %w", err)
    }

    tags, err := restate.Run(ctx, func(rc restate.RunContext) (map[string]string, error) {
        return api.ListTagsForResource(rc, observed.Arn)
    })
    if err != nil {
        return LogGroupOutputs{}, err
    }
    observed.Tags = tags

    outputs := outputsFromObserved(observed)

    restate.Set(ctx, drivers.StateKey, &LogGroupState{
        Desired:    spec,
        Observed:   *observed,
        Outputs:    outputs,
        Status:     types.StatusReady,
        Mode:       types.ModeManaged,
        Generation: state.Generation,
    })

    return outputs, nil
}
```

### Import

```go
func (d *LogGroupDriver) Import(ctx restate.ObjectContext, req drivers.ImportRequest) (LogGroupOutputs, error) {
    api := d.buildAPI(req.Account, req.Region)

    // Describe the log group
    observed, err := restate.Run(ctx, func(rc restate.RunContext) (*ObservedState, error) {
        return api.DescribeLogGroup(rc, req.ResourceID)
    })
    if err != nil {
        return LogGroupOutputs{}, err
    }
    if observed == nil {
        return LogGroupOutputs{}, restate.TerminalError(
            fmt.Errorf("log group %q not found in %s", req.ResourceID, req.Region), 404)
    }

    // Fetch tags
    tags, err := restate.Run(ctx, func(rc restate.RunContext) (map[string]string, error) {
        return api.ListTagsForResource(rc, observed.Arn)
    })
    if err != nil {
        return LogGroupOutputs{}, err
    }
    observed.Tags = tags

    outputs := outputsFromObserved(observed)
    mode := drivers.ImportMode(req.Observe)

    restate.Set(ctx, drivers.StateKey, &LogGroupState{
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
func (d *LogGroupDriver) Delete(ctx restate.ObjectContext) error {
    state, _ := restate.Get[*LogGroupState](ctx, drivers.StateKey)
    if state == nil {
        return nil // Already gone
    }

    api := d.buildAPI(state.Desired.Account, state.Desired.Region)

    // Delete the log group (this also deletes all log streams and data)
    if _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
        return restate.Void{}, api.DeleteLogGroup(rc, state.Desired.LogGroupName)
    }); err != nil {
        if isNotFound(err) {
            // Already deleted externally — clear state and return
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
func (d *LogGroupDriver) Reconcile(ctx restate.ObjectContext) error {
    state, _ := restate.Get[*LogGroupState](ctx, drivers.StateKey)
    if state == nil {
        return nil
    }

    api := d.buildAPI(state.Desired.Account, state.Desired.Region)

    // Describe current state
    observed, err := restate.Run(ctx, func(rc restate.RunContext) (*ObservedState, error) {
        return api.DescribeLogGroup(rc, state.Desired.LogGroupName)
    })
    if err != nil {
        return err
    }

    if observed == nil {
        // Resource deleted externally
        state.Status = types.StatusDrifted
        state.Error = "log group deleted externally"
        state.Observed = ObservedState{}
        restate.Set(ctx, drivers.StateKey, state)
        d.scheduleReconcile(ctx)
        return nil
    }

    // Fetch tags
    tags, err := restate.Run(ctx, func(rc restate.RunContext) (map[string]string, error) {
        return api.ListTagsForResource(rc, observed.Arn)
    })
    if err != nil {
        return err
    }
    observed.Tags = tags

    state.Observed = *observed

    if HasDrift(state.Desired, *observed) {
        state.Status = types.StatusDrifted

        if state.Mode == types.ModeManaged {
            // Auto-correct drift
            if _, err := d.updateLogGroup(ctx, api, state.Desired, state); err != nil {
                state.Error = err.Error()
                restate.Set(ctx, drivers.StateKey, state)
                d.scheduleReconcile(ctx)
                return nil
            }
            return nil
        }
    } else {
        state.Status = types.StatusReady
        state.Error = ""
    }

    state.Outputs = outputsFromObserved(observed)
    restate.Set(ctx, drivers.StateKey, state)
    d.scheduleReconcile(ctx)
    return nil
}
```

### GetStatus / GetOutputs

```go
func (d *LogGroupDriver) GetStatus(ctx restate.ObjectSharedContext) (types.ResourceStatus, error) {
    state, _ := restate.Get[*LogGroupState](ctx, drivers.StateKey)
    if state == nil {
        return types.StatusNotFound, nil
    }
    return state.Status, nil
}

func (d *LogGroupDriver) GetOutputs(ctx restate.ObjectSharedContext) (LogGroupOutputs, error) {
    state, _ := restate.Get[*LogGroupState](ctx, drivers.StateKey)
    if state == nil {
        return LogGroupOutputs{}, nil
    }
    return state.Outputs, nil
}
```

---

## Step 7 — Provider Adapter

**File**: `internal/core/provider/loggroup_adapter.go`

```go
type LogGroupAdapter struct {
    accounts   *auth.Registry
    apiFactory func(aws.Config) loggroup.LogGroupAPI
}

func NewLogGroupAdapterWithRegistry(accounts *auth.Registry) *LogGroupAdapter {
    if accounts == nil {
        accounts = auth.LoadFromEnv()
    }
    return &LogGroupAdapter{
        accounts: accounts,
        apiFactory: func(cfg aws.Config) loggroup.LogGroupAPI {
            return loggroup.NewLogGroupAPI(awsclient.NewCloudWatchLogsClient(cfg))
        },
    }
}

func (a *LogGroupAdapter) Kind() string        { return "LogGroup" }
func (a *LogGroupAdapter) ServiceName() string { return loggroup.ServiceName }
func (a *LogGroupAdapter) Scope() KeyScope     { return KeyScopeRegion }

func (a *LogGroupAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
    var doc struct {
        Metadata struct{ Name string `json:"name"` } `json:"metadata"`
        Spec     struct{ Region string `json:"region"` } `json:"spec"`
    }
    if err := json.Unmarshal(resourceDoc, &doc); err != nil {
        return "", fmt.Errorf("LogGroupAdapter.BuildKey: %w", err)
    }
    return JoinKey(doc.Spec.Region, doc.Metadata.Name), nil
}

func (a *LogGroupAdapter) BuildImportKey(region, resourceID string) (string, error) {
    return JoinKey(region, resourceID), nil
}

func (a *LogGroupAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
    return decodeSpec[loggroup.LogGroupSpec](resourceDoc)
}
```

---

## Step 8 — Registry Integration

**File**: `internal/core/provider/registry.go` — add to `NewRegistry()`:

```go
// Add to the NewRegistryWithAdapters(...) call:
NewLogGroupAdapterWithRegistry(accounts),
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

CloudWatch drivers are hosted in the new `praxis-monitoring` service on port 9086.
See the [pack overview](CLOUDWATCH_DRIVER_PACK_OVERVIEW.md) for the docker-compose
service definition.

### Justfile

```just
test-loggroup:    go test ./internal/drivers/loggroup/...    -v -count=1 -race
```

---

## Step 11 — Unit Tests

**File**: `internal/drivers/loggroup/driver_test.go`

### Test Cases

| Test | Description |
|---|---|
| `TestProvision_CreateNew` | Creates a new log group with retention and tags |
| `TestProvision_CreateWithKMS` | Creates with KMS encryption |
| `TestProvision_CreateNoRetention` | Creates with no retention (never expire) |
| `TestProvision_AlreadyExists` | Returns terminal error on conflict |
| `TestProvision_UpdateRetention` | Changes retention days |
| `TestProvision_RemoveRetention` | Switches from 30 days to never expire |
| `TestProvision_AddKMS` | Associates KMS key to existing log group |
| `TestProvision_RemoveKMS` | Disassociates KMS key |
| `TestProvision_ImmutableClassChange` | Terminal error when logGroupClass differs |
| `TestImport_Existing` | Imports existing log group in managed mode |
| `TestImport_Observed` | Imports existing log group in observed mode |
| `TestImport_NotFound` | Returns 404 terminal error |
| `TestDelete_Success` | Deletes log group and clears state |
| `TestDelete_AlreadyGone` | No-op when already deleted |
| `TestDelete_NoState` | No-op when no state exists |
| `TestReconcile_NoDrift` | Status remains `Ready` |
| `TestReconcile_RetentionDrift` | Detects and corrects retention change |
| `TestReconcile_TagDrift` | Detects and corrects tag changes |
| `TestReconcile_ExternalDeletion` | Detects external deletion, sets `Drifted` |
| `TestReconcile_ObservedMode` | Detects drift but does not correct |
| `TestGetStatus_NotFound` | Returns `StatusNotFound` for missing state |
| `TestGetOutputs_Exists` | Returns correct outputs |

**File**: `internal/drivers/loggroup/drift_test.go`

| Test | Description |
|---|---|
| `TestHasDrift_NoDrift` | Same retention, KMS, tags → no drift |
| `TestHasDrift_RetentionChanged` | 30 → 60 days → drift |
| `TestHasDrift_RetentionRemoved` | 30 → nil → drift |
| `TestHasDrift_RetentionAdded` | nil → 30 → drift |
| `TestHasDrift_KmsAdded` | Empty → ARN → drift |
| `TestHasDrift_KmsRemoved` | ARN → empty → drift |
| `TestHasDrift_KmsChanged` | ARN-A → ARN-B → drift |
| `TestHasDrift_TagsAdded` | Tags added externally → drift |
| `TestHasDrift_TagsRemoved` | Tags removed externally → drift |
| `TestHasDrift_TagsChanged` | Tag value changed → drift |

**File**: `internal/drivers/loggroup/aws_test.go`

| Test | Description |
|---|---|
| `TestIsNotFound` | Classifies `ResourceNotFoundException` |
| `TestIsAlreadyExists` | Classifies `ResourceAlreadyExistsException` |
| `TestIsInvalidParam` | Classifies `InvalidParameterException` |
| `TestIsThrottled` | Classifies `ThrottlingException` |

---

## Step 12 — Integration Tests

**File**: `tests/integration/loggroup_driver_test.go`

### Prerequisites

- LocalStack with CloudWatch Logs support
- Restate dev server
- praxis-monitoring pack running

### Test Scenarios

| Test | Description |
|---|---|
| `TestLogGroup_CreateAndDescribe` | Create → describe → verify retention and class |
| `TestLogGroup_UpdateRetention` | Create → change retention → verify |
| `TestLogGroup_RemoveRetention` | Create with retention → remove → verify never-expire |
| `TestLogGroup_KmsEncryption` | Create → associate KMS → verify → disassociate → verify |
| `TestLogGroup_Tags` | Create with tags → add/remove tags → verify |
| `TestLogGroup_Import` | Create externally → import → verify outputs |
| `TestLogGroup_Delete` | Create → delete → verify deletion |
| `TestLogGroup_DriftCorrection` | Create → externally change retention → reconcile → verify corrected |
| `TestLogGroup_InfrequentAccess` | Create with `INFREQUENT_ACCESS` class → verify |

---

## Log-Group-Specific Design Decisions

### 1. Retention Is a Separate API Call

AWS CloudWatch Logs models retention as a separate resource (`PutRetentionPolicy` /
`DeleteRetentionPolicy`) rather than part of the log group creation. The driver
always calls `CreateLogGroup` first, then `PutRetentionPolicy` if a retention
period is specified. This split is similar to Lambda's code/config separation but
simpler — there's no state machine or polling required between calls.

### 2. KMS Key Association Is Also Separate

Like retention, KMS encryption is configured via separate API calls
(`AssociateKmsKey` / `DisassociateKmsKey`). The driver handles this in the same
sequential pattern after creation.

### 3. Log Group Class Is Immutable

The `logGroupClass` attribute (`STANDARD` or `INFREQUENT_ACCESS`) cannot be changed
after creation. If the desired class differs from the observed class, the driver
returns a terminal error advising the user to delete and recreate the log group.
This is analogous to changing the engine of an RDS instance.

### 4. DescribeLogGroups Does Not Return Tags

The `DescribeLogGroups` API does not include tags in its response. Tags must be
fetched separately via `ListTagsForResource` using the log group ARN. The driver
makes both calls during observe and reconcile operations.

### 5. Log Group Names with Slashes

Log group names frequently contain forward slashes (e.g., `/aws/lambda/my-fn`).
The Virtual Object key uses `~` as the delimiter between region and name, avoiding
any conflict with slashes in the name. The CUE schema `metadata.name` accepts the
full path-style name.

### 6. Never-Expire Semantics

When `retentionInDays` is omitted (nil), the driver calls `DeleteRetentionPolicy`
to ensure logs never expire. This is the AWS default for new log groups, but
explicit deletion ensures convergence if someone previously set a retention period.

---

## Checklist

### Files

- [x] `schemas/aws/cloudwatch/log_group.cue`
- [x] `internal/drivers/loggroup/types.go`
- [x] `internal/drivers/loggroup/aws.go`
- [x] `internal/drivers/loggroup/drift.go`
- [x] `internal/drivers/loggroup/driver.go`
- [x] `internal/drivers/loggroup/driver_test.go`
- [x] `internal/drivers/loggroup/aws_test.go`
- [x] `internal/drivers/loggroup/drift_test.go`
- [x] `internal/core/provider/loggroup_adapter.go`
- [x] `internal/core/provider/loggroup_adapter_test.go`
- [x] `tests/integration/loggroup_driver_test.go`

### Modifications

- [x] `internal/infra/awsclient/client.go` — `NewCloudWatchLogsClient()`
- [x] `cmd/praxis-monitoring/main.go` — Bind LogGroupDriver
- [x] `internal/core/provider/registry.go` — Register adapter
- [x] `justfile` — Add `test-loggroup` target
