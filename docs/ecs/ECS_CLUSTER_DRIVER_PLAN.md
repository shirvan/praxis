# ECS Cluster Driver — Implementation Plan

> NYI
> Target: A Restate Virtual Object driver that manages AWS ECS Clusters, following
> the exact patterns established by the S3, Security Group, EC2, VPC, EBS, Elastic IP,
> Key Pair, AMI, and IAM drivers.
>
> Key scope: `KeyScopeRegion` — key format is `region~clusterName`, permanent and
> immutable for the lifetime of the Virtual Object. The AWS-assigned cluster ARN
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
12. [Step 9 — Compute Driver Pack Entry Point](#step-9--compute-driver-pack-entry-point)
13. [Step 10 — Docker Compose & Justfile](#step-10--docker-compose--justfile)
14. [Step 11 — Unit Tests](#step-11--unit-tests)
15. [Step 12 — Integration Tests](#step-12--integration-tests)
16. [ECS-Cluster-Specific Design Decisions](#ecs-cluster-specific-design-decisions)
17. [Checklist](#checklist)

---

## 1. Overview & Scope

The ECS Cluster driver manages the lifecycle of **ECS clusters** only. Services,
task definitions, tasks, container instances, and capacity providers (as
standalone resources) are not managed by this driver. This document focuses
exclusively on cluster creation, configuration updates, import, deletion, and
drift reconciliation.

The driver follows the established Virtual Object contract:

| Handler | Context | Purpose |
|---|---|---|
| `Provision` | `ObjectContext` (exclusive) | Create or converge an ECS cluster |
| `Import` | `ObjectContext` (exclusive) | Adopt an existing cluster |
| `Delete` | `ObjectContext` (exclusive) | Delete a cluster |
| `Reconcile` | `ObjectContext` (exclusive) | Detect/correct drift (report-only for Observed mode) |
| `GetStatus` | `ObjectSharedContext` (shared) | Return current status |
| `GetOutputs` | `ObjectSharedContext` (shared) | Return cluster outputs |

### Mutable vs Immutable Attributes

| Attribute | Mutability | API | Notes |
|---|---|---|---|
| Cluster name | Immutable | — | Set at creation; cannot be changed |
| Cluster ARN | Immutable | — | AWS-assigned |
| Settings (Container Insights) | Mutable | `UpdateCluster` | `containerInsights` enabled/disabled |
| Configuration (execute command) | Mutable | `UpdateCluster` | Logging config for ECS Exec |
| Capacity providers | Mutable | `PutClusterCapacityProviders` | Separate API call |
| Default capacity provider strategy | Mutable | `PutClusterCapacityProviders` | Must accompany capacity providers |
| Service connect defaults | Mutable | `UpdateCluster` | Default namespace for Service Connect |
| Tags | Mutable | `TagResource` / `UntagResource` | Key-value pairs |

### What Is NOT In Scope

- **ECS Services**: Managed by the ECS Service driver.
- **ECS Task Definitions**: Managed by the ECS Task Definition driver.
- **Tasks**: Transient compute units managed by ECS scheduler; not a Praxis resource.
- **Container Instances**: EC2 instances registered with the cluster; managed by
  EC2/ASG drivers.
- **Capacity Providers (as standalone resources)**: The cluster driver associates
  existing capacity providers (`FARGATE`, `FARGATE_SPOT`, or custom). It does NOT
  create or manage capacity provider resources themselves. Custom capacity providers
  backed by Auto Scaling Groups would be a future driver.

### Downstream Consumers

```
${resources.my-cluster.outputs.clusterArn}       → ECS Services, task runs
${resources.my-cluster.outputs.clusterName}       → ECS Services, CLI references
${resources.my-cluster.outputs.status}            → Health checks, deployment gates
```

---

## 2. Key Strategy

### Key Format: `region~clusterName`

ECS cluster names are unique within a region+account. The CUE schema maps
`metadata.name` to the cluster name. The adapter produces `region~metadata.name`
as the Virtual Object key.

1. **BuildKey** (adapter, plan-time): returns `region~metadata.name`.
2. **Provision / Delete**: dispatched to the same VO key.
3. **Plan**: reads VO state via `GetOutputs`. If outputs contain a cluster ARN,
   describes the cluster by name. Otherwise, `OpCreate`.
4. **Import**: `BuildImportKey(region, resourceID)` returns `region~resourceID`
   where `resourceID` is the cluster name. Same key as BuildKey — matching the
   S3/IAM pattern because cluster names are AWS-unique per region.

### Ownership Tags

ECS cluster names are unique within a region+account, so `CreateCluster` returns
`ClusterAlreadyExistsException` for duplicates. The driver adds
`praxis:managed-key=<region~clusterName>` as a cluster tag for consistency with the
EC2 pattern and cross-Praxis-installation conflict detection.

**FindByManagedKey** is NOT needed because cluster names are AWS-enforced unique.

### Import Semantics

Import and template-based management produce the **same Virtual Object key** because
cluster names are globally unique within a region:

- `praxis import --kind ECSCluster --region us-east-1 --resource-id myapp`:
  Creates VO key `us-east-1~myapp`.
- Template with `metadata.name: myapp` in `us-east-1`:
  Creates VO key `us-east-1~myapp`.

Both target the same Virtual Object.

---

## 3. File Inventory

Create or modify these files (✦ = new file, ✎ = modify existing):

```text
✦ internal/drivers/ecscluster/types.go               — Spec, Outputs, ObservedState, State structs
✦ internal/drivers/ecscluster/aws.go                  — ECSClusterAPI interface + realECSClusterAPI impl
✦ internal/drivers/ecscluster/drift.go                — HasDrift(), ComputeFieldDiffs()
✦ internal/drivers/ecscluster/driver.go               — ECSClusterDriver Virtual Object
✦ internal/drivers/ecscluster/driver_test.go          — Unit tests for driver (mocked AWS)
✦ internal/drivers/ecscluster/aws_test.go             — Unit tests for error classification
✦ internal/drivers/ecscluster/drift_test.go           — Unit tests for drift detection
✦ internal/core/provider/ecscluster_adapter.go        — ECSClusterAdapter implementing provider.Adapter
✦ internal/core/provider/ecscluster_adapter_test.go   — Unit tests for adapter
✦ schemas/aws/ecs/cluster.cue                         — CUE schema for ECSCluster resource
✦ tests/integration/ecscluster_driver_test.go         — Integration tests (Testcontainers + LocalStack)
✎ internal/infra/awsclient/client.go                  — Add NewECSClient()
✎ cmd/praxis-compute/main.go                          — Bind ECSCluster driver
✎ internal/core/provider/registry.go                  — Add adapter to NewRegistry()
✎ justfile                                            — Add ECS test targets
```

---

## Step 1 — CUE Schema

**File**: `schemas/aws/ecs/cluster.cue`

```cue
package ecs

#ECSCluster: {
    apiVersion: "praxis.io/v1"
    kind:       "ECSCluster"

    metadata: {
        // name is the ECS cluster name in AWS.
        // Must be 1-255 characters: letters, numbers, hyphens, and underscores.
        name: string & =~"^[a-zA-Z][a-zA-Z0-9_-]{0,254}$"
        labels: [string]: string
    }

    spec: {
        // region is the AWS region to create the cluster in.
        region: string

        // settings controls cluster-level features.
        settings?: [...#ClusterSetting]

        // configuration controls cluster-level configuration (e.g., execute command).
        configuration?: #ClusterConfiguration

        // capacityProviders is the list of capacity providers to associate.
        // Use "FARGATE" and "FARGATE_SPOT" for Fargate, or custom capacity
        // provider names for EC2 Auto Scaling Group backed providers.
        capacityProviders?: [...string]

        // defaultCapacityProviderStrategy defines the default strategy when
        // launching tasks/services without an explicit strategy.
        defaultCapacityProviderStrategy?: [...#CapacityProviderStrategyItem]

        // serviceConnectDefaults defines the default Service Connect namespace.
        serviceConnectDefaults?: {
            namespace: string
        }

        // tags on the cluster resource.
        tags: [string]: string
    }

    outputs?: {
        clusterArn:  string
        clusterName: string
        status:      string
    }
}

#ClusterSetting: {
    name:  "containerInsights"
    value: "enabled" | "disabled"
}

#ClusterConfiguration: {
    executeCommandConfiguration?: {
        // kmsKeyId for encrypting exec session data.
        kmsKeyId?: string
        // logging controls where exec session logs are sent.
        logging?: "NONE" | "DEFAULT" | "OVERRIDE"
        // logConfiguration is required when logging is "OVERRIDE".
        logConfiguration?: {
            cloudWatchLogGroupName?:  string
            cloudWatchEncryptionEnabled?: bool
            s3BucketName?:               string
            s3EncryptionEnabled?:        bool
            s3KeyPrefix?:                string
        }
    }
}

#CapacityProviderStrategyItem: {
    capacityProvider: string
    weight?:         int & >=0 & <=1000 | *0
    base?:           int & >=0 & <=100000 | *0
}
```

### Schema Design Notes

- **`settings` is an array of named key-value pairs**: Currently the only supported
  setting is `containerInsights`. The schema constrains the name to exactly
  `"containerInsights"` for type safety.
- **`capacityProviders` and `defaultCapacityProviderStrategy` are coupled**: AWS
  requires both to be set together via `PutClusterCapacityProviders`. The driver
  validates this at provision time.
- **`serviceConnectDefaults.namespace`**: References a Cloud Map namespace ARN.
  The driver does not validate the namespace exists — AWS validates at create time.

---

## Step 2 — AWS Client Factory

**File**: `internal/infra/awsclient/client.go` — **ADD NewECSClient()**

```go
import "github.com/aws/aws-sdk-go-v2/service/ecs"

// NewECSClient creates an ECS API client from the given AWS config.
func NewECSClient(cfg aws.Config) *ecs.Client {
    return ecs.NewFromConfig(cfg)
}
```

This follows the exact pattern of `NewEC2Client()`, `NewS3Client()`, etc.

---

## Step 3 — Driver Types

**File**: `internal/drivers/ecscluster/types.go`

```go
package ecscluster

import "github.com/praxiscloud/praxis/pkg/types"

// ServiceName is the Restate Virtual Object name for ECS Clusters.
const ServiceName = "ECSCluster"

// ECSClusterSpec is the desired state for an ECS cluster.
type ECSClusterSpec struct {
    Account                          string                        `json:"account,omitempty"`
    Region                           string                        `json:"region"`
    ClusterName                      string                        `json:"clusterName"`
    Settings                         []ClusterSetting              `json:"settings,omitempty"`
    Configuration                    *ClusterConfiguration         `json:"configuration,omitempty"`
    CapacityProviders                []string                      `json:"capacityProviders,omitempty"`
    DefaultCapacityProviderStrategy  []CapacityProviderStrategyItem `json:"defaultCapacityProviderStrategy,omitempty"`
    ServiceConnectDefaults           *ServiceConnectDefaults       `json:"serviceConnectDefaults,omitempty"`
    Tags                             map[string]string             `json:"tags,omitempty"`
    ManagedKey                       string                        `json:"managedKey,omitempty"`
}

// ClusterSetting is a name-value pair for cluster settings.
type ClusterSetting struct {
    Name  string `json:"name"`
    Value string `json:"value"`
}

// ClusterConfiguration holds execute command configuration.
type ClusterConfiguration struct {
    ExecuteCommandConfiguration *ExecuteCommandConfiguration `json:"executeCommandConfiguration,omitempty"`
}

// ExecuteCommandConfiguration controls ECS Exec behavior.
type ExecuteCommandConfiguration struct {
    KmsKeyId         string                    `json:"kmsKeyId,omitempty"`
    Logging          string                    `json:"logging,omitempty"`
    LogConfiguration *ExecuteCommandLogConfig  `json:"logConfiguration,omitempty"`
}

// ExecuteCommandLogConfig defines log routing for ECS Exec sessions.
type ExecuteCommandLogConfig struct {
    CloudWatchLogGroupName       string `json:"cloudWatchLogGroupName,omitempty"`
    CloudWatchEncryptionEnabled  bool   `json:"cloudWatchEncryptionEnabled,omitempty"`
    S3BucketName                 string `json:"s3BucketName,omitempty"`
    S3EncryptionEnabled          bool   `json:"s3EncryptionEnabled,omitempty"`
    S3KeyPrefix                  string `json:"s3KeyPrefix,omitempty"`
}

// CapacityProviderStrategyItem defines a capacity provider strategy entry.
type CapacityProviderStrategyItem struct {
    CapacityProvider string `json:"capacityProvider"`
    Weight           int32  `json:"weight,omitempty"`
    Base             int32  `json:"base,omitempty"`
}

// ServiceConnectDefaults holds the default Service Connect namespace.
type ServiceConnectDefaults struct {
    Namespace string `json:"namespace"`
}

// ECSClusterOutputs is produced after provisioning and stored in Restate K/V.
type ECSClusterOutputs struct {
    ClusterArn  string `json:"clusterArn"`
    ClusterName string `json:"clusterName"`
    Status      string `json:"status"`
}

// ObservedState captures the actual configuration of a cluster from AWS.
type ObservedState struct {
    ClusterArn                       string                         `json:"clusterArn"`
    ClusterName                      string                         `json:"clusterName"`
    Status                           string                         `json:"status"`
    Settings                         []ClusterSetting               `json:"settings,omitempty"`
    Configuration                    *ClusterConfiguration          `json:"configuration,omitempty"`
    CapacityProviders                []string                       `json:"capacityProviders,omitempty"`
    DefaultCapacityProviderStrategy  []CapacityProviderStrategyItem `json:"defaultCapacityProviderStrategy,omitempty"`
    ServiceConnectDefaults           *ServiceConnectDefaults        `json:"serviceConnectDefaults,omitempty"`
    Tags                             map[string]string              `json:"tags,omitempty"`
    ActiveServicesCount              int32                          `json:"activeServicesCount"`
    RunningTasksCount                int32                          `json:"runningTasksCount"`
    RegisteredContainerInstancesCount int32                         `json:"registeredContainerInstancesCount"`
}

// ECSClusterState is the single atomic state object stored under drivers.StateKey.
type ECSClusterState struct {
    Desired            ECSClusterSpec        `json:"desired"`
    Observed           ObservedState         `json:"observed"`
    Outputs            ECSClusterOutputs     `json:"outputs"`
    Status             types.ResourceStatus  `json:"status"`
    Mode               types.Mode            `json:"mode"`
    Error              string                `json:"error,omitempty"`
    Generation         int64                 `json:"generation"`
    LastReconcile      string                `json:"lastReconcile,omitempty"`
    ReconcileScheduled bool                  `json:"reconcileScheduled"`
}
```

### Types Design Notes

- **`Settings` is an explicit struct array, not a map**: ECS models settings as
  `[]ClusterSetting{Name, Value}` rather than a flat map. This mirrors the AWS
  API shape directly.
- **`ObservedState` includes runtime metrics**: `activeServicesCount`,
  `runningTasksCount`, and `registeredContainerInstancesCount` are informational
  fields from `DescribeClusters`. They are not drift-detected but are useful for
  status reporting.

---

## Step 4 — AWS API Abstraction Layer

**File**: `internal/drivers/ecscluster/aws.go`

### ECSClusterAPI Interface

```go
type ECSClusterAPI interface {
    // CreateCluster creates a new ECS cluster.
    CreateCluster(ctx context.Context, spec ECSClusterSpec) (ECSClusterOutputs, error)

    // DescribeCluster returns the current state of a cluster.
    DescribeCluster(ctx context.Context, clusterName string) (ObservedState, error)

    // UpdateCluster updates cluster settings and configuration.
    UpdateCluster(ctx context.Context, clusterName string, spec ECSClusterSpec) error

    // PutCapacityProviders updates the capacity providers and default strategy.
    PutCapacityProviders(ctx context.Context, clusterName string, providers []string, strategy []CapacityProviderStrategyItem) error

    // DeleteCluster deletes an ECS cluster.
    // The cluster must have no active services, tasks, or container instances.
    DeleteCluster(ctx context.Context, clusterName string) error

    // TagCluster replaces all tags on the cluster.
    TagCluster(ctx context.Context, clusterArn string, tags map[string]string) error

    // UntagCluster removes specific tag keys from the cluster.
    UntagCluster(ctx context.Context, clusterArn string, keys []string) error
}
```

### Implementation: realECSClusterAPI

```go
type realECSClusterAPI struct {
    client  *ecs.Client
    limiter ratelimit.Limiter
}

func newRealECSClusterAPI(client *ecs.Client, limiter ratelimit.Limiter) ECSClusterAPI {
    return &realECSClusterAPI{client: client, limiter: limiter}
}
```

### Error Classification

```go
func isNotFound(err error) bool {
    var cnf *ecstypes.ClusterNotFoundException
    return errors.As(err, &cnf)
}

func isAlreadyExists(err error) bool {
    // ECS does not have a dedicated "already exists" exception for clusters.
    // CreateCluster with an existing name returns the existing cluster
    // (idempotent). The driver detects this by comparing the returned
    // cluster status/ARN with expectations.
    return false
}

func isInvalidParam(err error) bool {
    var ip *ecstypes.InvalidParameterException
    return errors.As(err, &ip)
}

func isServerError(err error) bool {
    var se *ecstypes.ServerException
    return errors.As(err, &se)
}

func isClientError(err error) bool {
    var ce *ecstypes.ClientException
    return errors.As(err, &ce)
}

func isUpdateInProgress(err error) bool {
    var uip *ecstypes.UpdateInProgressException
    return errors.As(err, &uip)
}
```

### Key Implementation Details

#### CreateCluster

```go
func (r *realECSClusterAPI) CreateCluster(ctx context.Context, spec ECSClusterSpec) (ECSClusterOutputs, error) {
    r.limiter.Wait(ctx)

    input := &ecs.CreateClusterInput{
        ClusterName: &spec.ClusterName,
        Tags:        toECSTags(spec.Tags),
    }

    if len(spec.Settings) > 0 {
        input.Settings = toECSSettings(spec.Settings)
    }
    if spec.Configuration != nil {
        input.Configuration = toECSConfiguration(spec.Configuration)
    }
    if len(spec.CapacityProviders) > 0 {
        input.CapacityProviders = spec.CapacityProviders
        input.DefaultCapacityProviderStrategy = toECSCapProviderStrategy(spec.DefaultCapacityProviderStrategy)
    }
    if spec.ServiceConnectDefaults != nil {
        input.ServiceConnectDefaults = &ecstypes.ClusterServiceConnectDefaultsRequest{
            Namespace: &spec.ServiceConnectDefaults.Namespace,
        }
    }

    out, err := r.client.CreateCluster(ctx, input)
    if err != nil {
        return ECSClusterOutputs{}, err
    }

    return ECSClusterOutputs{
        ClusterArn:  aws.ToString(out.Cluster.ClusterArn),
        ClusterName: aws.ToString(out.Cluster.ClusterName),
        Status:      string(out.Cluster.Status),
    }, nil
}
```

#### DescribeCluster

```go
func (r *realECSClusterAPI) DescribeCluster(ctx context.Context, clusterName string) (ObservedState, error) {
    r.limiter.Wait(ctx)

    out, err := r.client.DescribeClusters(ctx, &ecs.DescribeClustersInput{
        Clusters: []string{clusterName},
        Include:  []ecstypes.ClusterField{
            ecstypes.ClusterFieldSettings,
            ecstypes.ClusterFieldConfigurations,
            ecstypes.ClusterFieldTags,
        },
    })
    if err != nil {
        return ObservedState{}, err
    }

    if len(out.Clusters) == 0 || string(out.Clusters[0].Status) == "INACTIVE" {
        return ObservedState{}, &ecstypes.ClusterNotFoundException{
            Message: aws.String("cluster not found or inactive"),
        }
    }

    c := out.Clusters[0]
    return ObservedState{
        ClusterArn:                        aws.ToString(c.ClusterArn),
        ClusterName:                       aws.ToString(c.ClusterName),
        Status:                            string(c.Status),
        Settings:                          fromECSSettings(c.Settings),
        Configuration:                     fromECSConfiguration(c.Configuration),
        CapacityProviders:                 c.CapacityProviders,
        DefaultCapacityProviderStrategy:   fromECSCapProviderStrategy(c.DefaultCapacityProviderStrategy),
        ServiceConnectDefaults:            fromECSServiceConnectDefaults(c.ServiceConnectDefaults),
        Tags:                              fromECSTags(c.Tags),
        ActiveServicesCount:               c.ActiveServicesCount,
        RunningTasksCount:                 c.RunningTasksCount,
        RegisteredContainerInstancesCount: c.RegisteredContainerInstancesCount,
    }, nil
}
```

**Important**: `DescribeClusters` returns an empty result rather than an error for
non-existent clusters. Additionally, deleted clusters return with `status: "INACTIVE"`.
The driver must check both conditions and synthesize a not-found error.

#### DeleteCluster

```go
func (r *realECSClusterAPI) DeleteCluster(ctx context.Context, clusterName string) error {
    r.limiter.Wait(ctx)

    _, err := r.client.DeleteCluster(ctx, &ecs.DeleteClusterInput{
        Cluster: &clusterName,
    })
    return err
}
```

**Pre-deletion requirement**: The cluster must have no active services, running tasks,
or registered container instances. The driver's `Delete` handler checks for these
conditions before calling `DeleteCluster` and returns a terminal error if the cluster
is not empty.

---

## Step 5 — Drift Detection

**File**: `internal/drivers/ecscluster/drift.go`

### Drift-Detectable Fields

| Field | Drift Source | Notes |
|---|---|---|
| Settings (containerInsights) | Console, CLI | Insights toggled externally |
| Configuration (executeCommand) | Console, CLI | Exec config changed |
| Capacity providers | Console, CLI, IaC | Providers added/removed |
| Default capacity provider strategy | Console, CLI | Strategy weights changed |
| Service connect defaults | Console, CLI | Namespace changed |
| Tags | Console, CLI, other tools | Tags added/removed/changed |

### Fields NOT Drift-Detected

- **Status**: Read-only runtime state, not a driftable attribute.
- **Active services count**: Runtime counter, not a configured attribute.
- **Running tasks count**: Runtime counter.
- **Registered container instances count**: Runtime counter.

### HasDrift

```go
func HasDrift(desired ECSClusterSpec, observed ObservedState) bool {
    if !settingsMatch(desired.Settings, observed.Settings) { return true }
    if !configurationMatch(desired.Configuration, observed.Configuration) { return true }
    if !slicesEqual(desired.CapacityProviders, observed.CapacityProviders) { return true }
    if !capProviderStrategyMatch(desired.DefaultCapacityProviderStrategy, observed.DefaultCapacityProviderStrategy) { return true }
    if !serviceConnectMatch(desired.ServiceConnectDefaults, observed.ServiceConnectDefaults) { return true }
    if !tagsMatch(desired.Tags, observed.Tags) { return true }
    return false
}
```

### ComputeFieldDiffs

```go
func ComputeFieldDiffs(desired ECSClusterSpec, observed ObservedState) []types.FieldDiff {
    var diffs []types.FieldDiff
    if !settingsMatch(desired.Settings, observed.Settings) {
        diffs = append(diffs, types.FieldDiff{
            Field: "settings", Desired: fmt.Sprintf("%v", desired.Settings),
            Observed: fmt.Sprintf("%v", observed.Settings),
        })
    }
    // ... similar for all drift-detectable fields
    return diffs
}
```

---

## Step 6 — Driver Implementation

**File**: `internal/drivers/ecscluster/driver.go`

### Constructor

```go
type ECSClusterDriver struct {
    accounts *auth.Registry
}

func NewECSClusterDriver(accounts *auth.Registry) *ECSClusterDriver {
    return &ECSClusterDriver{accounts: accounts}
}

func (ECSClusterDriver) ServiceName() string { return ServiceName }
```

### Provision

```go
func (d *ECSClusterDriver) Provision(ctx restate.ObjectContext, spec ECSClusterSpec) (ECSClusterOutputs, error) {
    // 1. Load existing state
    state, _ := restate.Get[*ECSClusterState](ctx, drivers.StateKey)

    // 2. Build API client
    api := d.buildAPI(spec.Account, spec.Region)

    // 3. If no existing state → CreateCluster
    if state == nil || state.Outputs.ClusterArn == "" {
        return d.createCluster(ctx, api, spec)
    }

    // 4. Existing cluster → update settings, config, capacity providers
    return d.updateCluster(ctx, api, spec, state)
}
```

#### Create Flow

```go
func (d *ECSClusterDriver) createCluster(ctx restate.ObjectContext, api ECSClusterAPI, spec ECSClusterSpec) (ECSClusterOutputs, error) {
    // Write pending state
    restate.Set(ctx, drivers.StateKey, &ECSClusterState{
        Desired: spec,
        Status:  types.StatusProvisioning,
        Mode:    drivers.DefaultMode(""),
    })

    // Create cluster (journaled via restate.Run)
    outputs, err := restate.Run(ctx, func(rc restate.RunContext) (ECSClusterOutputs, error) {
        return api.CreateCluster(rc, spec)
    })
    if err != nil {
        return ECSClusterOutputs{}, err
    }

    // Describe to populate full observed state
    observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
        return api.DescribeCluster(rc, spec.ClusterName)
    })
    if err != nil {
        return ECSClusterOutputs{}, err
    }

    // Write final state
    restate.Set(ctx, drivers.StateKey, &ECSClusterState{
        Desired:    spec,
        Observed:   observed,
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
func (d *ECSClusterDriver) updateCluster(ctx restate.ObjectContext, api ECSClusterAPI, spec ECSClusterSpec, state *ECSClusterState) (ECSClusterOutputs, error) {
    state.Desired = spec
    state.Status = types.StatusProvisioning
    state.Generation++
    restate.Set(ctx, drivers.StateKey, state)

    // Phase 1: Update cluster settings and configuration
    settingsChanged := !settingsMatch(spec.Settings, state.Observed.Settings) ||
        !configurationMatch(spec.Configuration, state.Observed.Configuration) ||
        !serviceConnectMatch(spec.ServiceConnectDefaults, state.Observed.ServiceConnectDefaults)

    if settingsChanged {
        if _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
            return restate.Void{}, api.UpdateCluster(rc, spec.ClusterName, spec)
        }); err != nil {
            return ECSClusterOutputs{}, err
        }
    }

    // Phase 2: Update capacity providers (separate API call)
    capChanged := !slicesEqual(spec.CapacityProviders, state.Observed.CapacityProviders) ||
        !capProviderStrategyMatch(spec.DefaultCapacityProviderStrategy, state.Observed.DefaultCapacityProviderStrategy)

    if capChanged {
        if _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
            return restate.Void{}, api.PutCapacityProviders(rc, spec.ClusterName, spec.CapacityProviders, spec.DefaultCapacityProviderStrategy)
        }); err != nil {
            return ECSClusterOutputs{}, err
        }
    }

    // Phase 3: Update tags
    if !tagsMatch(spec.Tags, state.Observed.Tags) {
        if _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
            removedKeys := computeRemovedKeys(state.Observed.Tags, spec.Tags)
            if len(removedKeys) > 0 {
                if err := api.UntagCluster(rc, state.Outputs.ClusterArn, removedKeys); err != nil {
                    return restate.Void{}, err
                }
            }
            return restate.Void{}, api.TagCluster(rc, state.Outputs.ClusterArn, spec.Tags)
        }); err != nil {
            return ECSClusterOutputs{}, err
        }
    }

    // Describe final state
    observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
        return api.DescribeCluster(rc, spec.ClusterName)
    })
    if err != nil {
        return ECSClusterOutputs{}, err
    }

    outputs := ECSClusterOutputs{
        ClusterArn:  observed.ClusterArn,
        ClusterName: observed.ClusterName,
        Status:      observed.Status,
    }

    restate.Set(ctx, drivers.StateKey, &ECSClusterState{
        Desired:    spec,
        Observed:   observed,
        Outputs:    outputs,
        Status:     types.StatusReady,
        Mode:       types.ModeManaged,
        Generation: state.Generation,
    })

    return outputs, nil
}
```

### Delete

```go
func (d *ECSClusterDriver) Delete(ctx restate.ObjectContext) error {
    state, _ := restate.Get[*ECSClusterState](ctx, drivers.StateKey)
    if state == nil {
        return nil
    }
    if state.Mode == types.ModeObserved {
        restate.Clear(ctx, drivers.StateKey)
        return nil
    }

    api := d.buildAPI(state.Desired.Account, state.Desired.Region)

    // Check if cluster is empty before attempting deletion
    observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
        return api.DescribeCluster(rc, state.Desired.ClusterName)
    })
    if err != nil {
        if isNotFound(err) {
            restate.Clear(ctx, drivers.StateKey)
            return nil
        }
        return err
    }

    if observed.ActiveServicesCount > 0 || observed.RunningTasksCount > 0 {
        return restate.TerminalError(
            fmt.Errorf("cluster %q has %d active services and %d running tasks; delete services first",
                state.Desired.ClusterName, observed.ActiveServicesCount, observed.RunningTasksCount),
            409,
        )
    }

    // Delete the cluster
    if _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
        return restate.Void{}, api.DeleteCluster(rc, state.Desired.ClusterName)
    }); err != nil {
        if isNotFound(err) {
            restate.Clear(ctx, drivers.StateKey)
            return nil
        }
        return err
    }

    restate.Clear(ctx, drivers.StateKey)
    return nil
}
```

### Import

```go
func (d *ECSClusterDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (ECSClusterOutputs, error) {
    api := d.buildAPI(ref.Account, ref.Region)

    observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
        return api.DescribeCluster(rc, ref.ResourceID)
    })
    if err != nil {
        return ECSClusterOutputs{}, err
    }

    outputs := ECSClusterOutputs{
        ClusterArn:  observed.ClusterArn,
        ClusterName: observed.ClusterName,
        Status:      observed.Status,
    }

    mode := types.ModeObserved
    if ref.Mode != "" {
        mode = ref.Mode
    }

    restate.Set(ctx, drivers.StateKey, &ECSClusterState{
        Desired:  specFromObserved(observed),
        Observed: observed,
        Outputs:  outputs,
        Status:   types.StatusReady,
        Mode:     mode,
    })

    d.scheduleReconcile(ctx)
    return outputs, nil
}
```

### Reconcile

Standard reconcile loop: describe cluster, compare with desired, correct drift if
managed mode, report-only if observed mode. Schedule next reconciliation via
`restate.ObjectSend` with `restate.WithDelay(drivers.ReconcileInterval)`.

---

## Step 7 — Provider Adapter

**File**: `internal/core/provider/ecscluster_adapter.go`

```go
type ECSClusterAdapter struct {
    accounts *auth.Registry
}

func NewECSClusterAdapterWithRegistry(accounts *auth.Registry) *ECSClusterAdapter {
    return &ECSClusterAdapter{accounts: accounts}
}

func (a *ECSClusterAdapter) Kind() string { return "ECSCluster" }

func (a *ECSClusterAdapter) ServiceName() string { return ecscluster.ServiceName }

func (a *ECSClusterAdapter) KeyScope() types.KeyScope { return types.KeyScopeRegion }

func (a *ECSClusterAdapter) BuildKey(doc map[string]any) (string, error) {
    region, _ := jsonpath.String(doc, "spec.region")
    name, _ := jsonpath.String(doc, "metadata.name")
    if region == "" || name == "" {
        return "", fmt.Errorf("ECSCluster requires spec.region and metadata.name")
    }
    return region + "~" + name, nil
}

func (a *ECSClusterAdapter) BuildImportKey(region, resourceID string) (string, error) {
    return region + "~" + resourceID, nil
}

func (a *ECSClusterAdapter) BuildSpec(doc map[string]any) (any, error) {
    // Extract fields from evaluated CUE doc, build ECSClusterSpec
    // ...
}

func (a *ECSClusterAdapter) Plan(ctx context.Context, key string, doc map[string]any) (types.PlanOp, []types.FieldDiff, error) {
    // Describe cluster by name, compare with desired spec
    // Return OpCreate, OpUpdate (with diffs), or OpNoop
    // ...
}
```

---

## Step 8 — Registry Integration

**File**: `internal/core/provider/registry.go` — add to `NewRegistry()`:

```go
r.Register(NewECSClusterAdapterWithRegistry(accounts))
```

---

## Step 9 — Compute Driver Pack Entry Point

**File**: `cmd/praxis-compute/main.go` — add Bind call:

```go
Bind(restate.Reflect(ecscluster.NewECSClusterDriver(cfg.Auth())))
```

---

## Step 10 — Docker Compose & Justfile

### Docker Compose

No changes needed — ECS Cluster driver is hosted in the existing `praxis-compute`
service on port 9084.

### Justfile

```just
test-ecs-cluster:
    go test ./internal/drivers/ecscluster/... -v -count=1 -race

test-ecs-cluster-integration:
    go test ./tests/integration/ -run TestECSCluster -v -count=1 -tags=integration -timeout=5m
```

---

## Step 11 — Unit Tests

**File**: `internal/drivers/ecscluster/driver_test.go`

### Test Cases

| Test | Description |
|---|---|
| `TestProvision_NewCluster` | Create a cluster with settings and capacity providers |
| `TestProvision_UpdateSettings` | Update containerInsights on an existing cluster |
| `TestProvision_UpdateCapacityProviders` | Change capacity providers and strategy |
| `TestProvision_UpdateTags` | Add, modify, and remove tags |
| `TestProvision_NoChanges` | Idempotent provision with no drift |
| `TestDelete_EmptyCluster` | Delete a cluster with no services or tasks |
| `TestDelete_NonEmptyCluster` | Attempt to delete a cluster with active services → terminal error |
| `TestDelete_AlreadyGone` | Delete when cluster is already deleted → no error |
| `TestDelete_ObservedMode` | Delete in observed mode → clear state only |
| `TestImport_Success` | Import an existing cluster |
| `TestImport_NotFound` | Import a non-existent cluster → error |
| `TestReconcile_NoDrift` | Reconcile with no changes |
| `TestReconcile_DriftDetected` | Reconcile detects settings change, corrects (managed mode) |
| `TestReconcile_DriftObserved` | Reconcile detects drift, reports only (observed mode) |
| `TestGetStatus` | Returns current status from state |
| `TestGetOutputs` | Returns current outputs from state |

---

## Step 12 — Integration Tests

**File**: `tests/integration/ecscluster_driver_test.go`

Integration tests use Testcontainers (LocalStack) to exercise real AWS API calls.

### Test Scenarios

1. **Create → Describe → Delete**: Full lifecycle of a Fargate cluster.
2. **Create with capacity providers**: Cluster with FARGATE and FARGATE_SPOT.
3. **Update settings**: Toggle Container Insights.
4. **Import → Reconcile**: Import an existing cluster and verify observation.
5. **Delete non-empty cluster**: Verify appropriate error when cluster has services.

**LocalStack consideration**: ECS in LocalStack has limited fidelity. The integration
tests focus on API shape validation (correct parameters, error codes, response
parsing) rather than full container orchestration behavior.

---

## ECS-Cluster-Specific Design Decisions

### CreateCluster Is Idempotent

Unlike most AWS create calls, `ecs.CreateCluster` is idempotent — calling it with
an existing cluster name returns the existing cluster rather than an error. The
driver must handle this by comparing the returned cluster's state with expectations.
If the cluster already exists with a `praxis:managed-key` tag from a different
Praxis installation, the driver returns a terminal 409 conflict error.

### Capacity Provider Updates Are Separate

`UpdateCluster` handles settings, configuration, and service connect defaults.
Capacity providers and the default strategy require a separate
`PutClusterCapacityProviders` call. The driver sequences these as two separate
`restate.Run` blocks when both need updating.

### Cluster Deletion Requires Emptiness

ECS requires clusters to be empty before deletion (no active services, running
tasks, or registered container instances). The driver verifies this precondition
and returns a clear terminal error if violated, rather than attempting forced
cleanup of child resources. The orchestrator handles ordered cleanup via the DAG.

### INACTIVE Clusters

Deleted ECS clusters transition to `INACTIVE` status and remain visible via
`DescribeClusters` for a period. The driver treats `INACTIVE` clusters as
"not found" — a `CreateCluster` call with the same name will reactivate the cluster.

---

## Checklist

- [ ] `schemas/aws/ecs/cluster.cue`
- [ ] `internal/drivers/ecscluster/types.go`
- [ ] `internal/drivers/ecscluster/aws.go`
- [ ] `internal/drivers/ecscluster/drift.go`
- [ ] `internal/drivers/ecscluster/driver.go`
- [ ] `internal/drivers/ecscluster/driver_test.go`
- [ ] `internal/drivers/ecscluster/aws_test.go`
- [ ] `internal/drivers/ecscluster/drift_test.go`
- [ ] `internal/core/provider/ecscluster_adapter.go`
- [ ] `internal/core/provider/ecscluster_adapter_test.go`
- [ ] `tests/integration/ecscluster_driver_test.go`
- [ ] `internal/infra/awsclient/client.go` — Add `NewECSClient()`
- [ ] `cmd/praxis-compute/main.go` — Bind ECSCluster driver
- [ ] `internal/core/provider/registry.go` — Register adapter
- [ ] `justfile` — Add ECS cluster test targets
