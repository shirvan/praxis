# ECS Task Definition Driver — Implementation Plan

> NYI
> Target: A Restate Virtual Object driver that manages AWS ECS Task Definitions,
> following the exact patterns established by the S3, Security Group, EC2, VPC, EBS,
> Elastic IP, Key Pair, AMI, and Lambda Layer drivers.
>
> Key scope: `KeyScopeRegion` — key format is `region~family`, permanent and
> immutable for the lifetime of the Virtual Object. The AWS-assigned task definition
> ARN (including revision number) lives only in state/outputs.

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
16. [Task-Definition-Specific Design Decisions](#task-definition-specific-design-decisions)
17. [Checklist](#checklist)

---

## 1. Overview & Scope

The ECS Task Definition driver manages the lifecycle of **ECS task definition families
and their revisions**. Task definitions in ECS are versioned and immutable per
revision — each `RegisterTaskDefinition` call creates a new numbered revision under
a family name. "Updating" a task definition means registering a new revision.

This driver is analogous to the Lambda Layer driver: both manage versioned, immutable
artifacts where the "current" version is the latest active revision.

The driver follows the established Virtual Object contract:

| Handler | Context | Purpose |
|---|---|---|
| `Provision` | `ObjectContext` (exclusive) | Register a new task definition revision (create or update) |
| `Import` | `ObjectContext` (exclusive) | Adopt an existing task definition family |
| `Delete` | `ObjectContext` (exclusive) | Deregister all active revisions of the family |
| `Reconcile` | `ObjectContext` (exclusive) | Detect/correct drift (report-only for Observed mode) |
| `GetStatus` | `ObjectSharedContext` (shared) | Return current status |
| `GetOutputs` | `ObjectSharedContext` (shared) | Return task definition outputs |

### Versioning Model

```
family: "myapp-web"
├── myapp-web:1  (INACTIVE — deregistered)
├── myapp-web:2  (INACTIVE — deregistered)
├── myapp-web:3  (ACTIVE — current)
```

The driver always registers a new revision on `Provision` when the desired spec
differs from the currently active revision. It tracks the latest active revision
ARN in outputs. Old revisions are optionally deregistered (configurable).

### Mutable vs Immutable Attributes (Per Revision)

**Every attribute of a task definition revision is immutable.** Once registered,
a revision cannot be modified. The only mutable aspect is the task definition
family itself, which accumulates revisions over time.

| Attribute | Scope | Notes |
|---|---|---|
| Family name | Family-level, immutable | Set at first registration; all revisions share it |
| Revision number | Revision-level, immutable | AWS-assigned, auto-incrementing |
| Task definition ARN | Revision-level, immutable | `arn:aws:ecs:region:account:task-definition/family:revision` |
| Container definitions | Revision-level, immutable | Image, ports, env, resources, health check, etc. |
| CPU / Memory | Revision-level, immutable | Task-level resource limits (required for Fargate) |
| Network mode | Revision-level, immutable | `awsvpc`, `bridge`, `host`, or `none` |
| Task role ARN | Revision-level, immutable | IAM role for the containers |
| Execution role ARN | Revision-level, immutable | IAM role for ECS agent (pull images, push logs) |
| Volumes | Revision-level, immutable | EFS, Docker, bind mounts |
| Placement constraints | Revision-level, immutable | `memberOf` expressions for EC2 launch type |
| Runtime platform | Revision-level, immutable | OS family + CPU architecture (Fargate) |
| Requires compatibilities | Revision-level, immutable | `EC2`, `FARGATE`, or both |
| Tags | Family-level, mutable | Tags on the task definition (propagated to latest revision) |

### What Is NOT In Scope

- **Running tasks**: The task definition is a blueprint. Running containers
  (tasks) are managed by ECS Services or `RunTask` invocations.
- **ECS Services**: Managed by the ECS Service driver.
- **Provisioned capacity**: This is a service-level concern.
- **Task definition compatibility validation**: The driver does not validate
  Fargate/EC2 compatibility constraints — AWS validates at registration time.

### Downstream Consumers

```
${resources.my-taskdef.outputs.taskDefinitionArn}    → ECS Services, RunTask
${resources.my-taskdef.outputs.family}                → ECS Services, CLI references
${resources.my-taskdef.outputs.revision}              → Deployment versioning
```

---

## 2. Key Strategy

### Key Format: `region~family`

ECS task definition families are unique within a region+account. The CUE schema maps
`metadata.name` to the task definition family. The adapter produces
`region~metadata.name` as the Virtual Object key.

1. **BuildKey** (adapter, plan-time): returns `region~metadata.name`.
2. **Provision / Delete**: dispatched to the same VO key.
3. **Plan**: reads VO state via `GetOutputs`. If outputs contain a task definition
   ARN, describes the latest active revision. Compares the desired spec with the
   current revision — if they differ, `OpUpdate` (register new revision).
4. **Import**: `BuildImportKey(region, resourceID)` returns `region~resourceID`
   where `resourceID` is the task definition family name (not `family:revision`).

### No Ownership Tags on Revisions

Task definition families are unique per account per region. AWS rejects duplicate
family+revision combinations naturally. The driver does not add ownership tags to
individual revisions — instead, it uses family-level tags on the task definition
resource.

- **Family-level tags**: `praxis:managed-key=<region~family>` via `TagResource`
  on the task definition ARN (without revision suffix).
- **Revision-level tags**: Passed via `RegisterTaskDefinition` for resource-level
  metadata (e.g., deployment version, git hash).

### Import Semantics

Import and template-based management produce the **same Virtual Object key** because
family names are unique per region:

- `praxis import --kind ECSTaskDefinition --region us-east-1 --resource-id myapp-web`:
  Creates VO key `us-east-1~myapp-web`. The driver describes the latest active
  revision and stores it as the observed state.

---

## 3. File Inventory

Create or modify these files (✦ = new file, ✎ = modify existing):

```text
✦ internal/drivers/ecstaskdef/types.go               — Spec, Outputs, ObservedState, State structs
✦ internal/drivers/ecstaskdef/aws.go                  — ECSTaskDefAPI interface + realECSTaskDefAPI impl
✦ internal/drivers/ecstaskdef/drift.go                — HasDrift(), ComputeFieldDiffs()
✦ internal/drivers/ecstaskdef/driver.go               — ECSTaskDefinitionDriver Virtual Object
✦ internal/drivers/ecstaskdef/driver_test.go          — Unit tests for driver (mocked AWS)
✦ internal/drivers/ecstaskdef/aws_test.go             — Unit tests for error classification
✦ internal/drivers/ecstaskdef/drift_test.go           — Unit tests for drift detection
✦ internal/core/provider/ecstaskdef_adapter.go        — ECSTaskDefinitionAdapter implementing provider.Adapter
✦ internal/core/provider/ecstaskdef_adapter_test.go   — Unit tests for adapter
✦ schemas/aws/ecs/task_definition.cue                 — CUE schema for ECSTaskDefinition resource
✦ tests/integration/ecstaskdef_driver_test.go         — Integration tests (Testcontainers + LocalStack)
✎ cmd/praxis-compute/main.go                          — Bind ECSTaskDefinition driver
✎ internal/core/provider/registry.go                  — Add adapter to NewRegistry()
✎ justfile                                            — Add task definition test targets
```

---

## Step 1 — CUE Schema

**File**: `schemas/aws/ecs/task_definition.cue`

```cue
package ecs

import "list"

#ECSTaskDefinition: {
    apiVersion: "praxis.io/v1"
    kind:       "ECSTaskDefinition"

    metadata: {
        // name maps to the ECS task definition family name.
        // Must be 1-255 characters: letters, numbers, hyphens, and underscores.
        name: string & =~"^[a-zA-Z][a-zA-Z0-9_-]{0,254}$"
        labels: [string]: string
    }

    spec: {
        // region is the AWS region to register the task definition in.
        region: string

        // containerDefinitions defines the containers in this task.
        containerDefinitions: [...#ContainerDefinition] & list.MinItems(1)

        // cpu is the task-level CPU units (required for Fargate).
        // Valid values: "256", "512", "1024", "2048", "4096"
        cpu?: string

        // memory is the task-level memory in MiB (required for Fargate).
        // Valid values depend on CPU: e.g., "512", "1024", "2048", "4096", etc.
        memory?: string

        // networkMode is the Docker networking mode.
        networkMode: "awsvpc" | "bridge" | "host" | "none" | *"awsvpc"

        // requiresCompatibilities defines the launch type compatibility.
        requiresCompatibilities?: [...("EC2" | "FARGATE")]

        // taskRoleArn is the IAM role ARN for task containers to call AWS APIs.
        taskRoleArn?: string

        // executionRoleArn is the IAM role ARN for the ECS agent
        // (pull images from ECR, push logs to CloudWatch).
        executionRoleArn?: string

        // volumes defines data volumes for the task.
        volumes?: [...#Volume]

        // placementConstraints limits EC2 placement (EC2 launch type only).
        placementConstraints?: [...#PlacementConstraint]

        // runtimePlatform defines the OS and CPU architecture (Fargate only).
        runtimePlatform?: {
            cpuArchitecture:       "X86_64" | "ARM64" | *"X86_64"
            operatingSystemFamily: "LINUX" | "WINDOWS_SERVER_2019_FULL" | "WINDOWS_SERVER_2019_CORE" | "WINDOWS_SERVER_2022_FULL" | "WINDOWS_SERVER_2022_CORE" | *"LINUX"
        }

        // proxyConfiguration for App Mesh integration.
        proxyConfiguration?: {
            containerName: string
            type:          "APPMESH"
            properties?: [...{
                name:  string
                value: string
            }]
        }

        // ephemeralStorage configures Fargate ephemeral storage (20-200 GiB).
        ephemeralStorage?: {
            sizeInGiB: int & >=20 & <=200 | *20
        }

        // pidMode for process namespace sharing.
        pidMode?: "host" | "task"

        // ipcMode for IPC namespace sharing.
        ipcMode?: "host" | "task" | "none"

        // tags on the task definition.
        tags: [string]: string
    }

    outputs?: {
        taskDefinitionArn: string
        family:            string
        revision:          int
        status:            string
    }
}

#ContainerDefinition: {
    name:  string
    image: string

    // Resource limits
    cpu?:               int
    memory?:            int
    memoryReservation?: int

    // Port mappings
    portMappings?: [...{
        containerPort: int
        hostPort?:     int
        protocol?:     "tcp" | "udp" | *"tcp"
        name?:         string
        appProtocol?:  "http" | "http2" | "grpc"
    }]

    // Container health check
    healthCheck?: {
        command:     [...string] & list.MinItems(1)
        interval?:   int & >=5 & <=300 | *30
        timeout?:    int & >=2 & <=120 | *5
        retries?:    int & >=1 & <=10 | *3
        startPeriod?: int & >=0 & <=300 | *0
    }

    essential?: bool | *true

    // Environment
    environment?: [...{
        name:  string
        value: string
    }]

    // Secrets from SSM Parameter Store or Secrets Manager
    secrets?: [...{
        name:      string
        valueFrom: string
    }]

    // Environment files from S3
    environmentFiles?: [...{
        value: string
        type:  "s3"
    }]

    // Logging
    logConfiguration?: {
        logDriver: "awslogs" | "fluentd" | "gelf" | "journald" | "json-file" | "splunk" | "syslog" | "awsfirelens"
        options?: [string]: string
        secretOptions?: [...{
            name:      string
            valueFrom: string
        }]
    }

    // Mount points from task volumes
    mountPoints?: [...{
        sourceVolume:  string
        containerPath: string
        readOnly?:     bool | *false
    }]

    // Volumes from other containers
    volumesFrom?: [...{
        sourceContainer: string
        readOnly?:       bool | *false
    }]

    // Linux parameters
    linuxParameters?: {
        capabilities?: {
            add?:  [...string]
            drop?: [...string]
        }
        initProcessEnabled?: bool
    }

    // Entry point and command overrides
    entryPoint?: [...string]
    command?:    [...string]
    workingDirectory?: string
    user?: string

    // Dependencies on other containers in the task
    dependsOn?: [...{
        containerName: string
        condition:     "START" | "COMPLETE" | "SUCCESS" | "HEALTHY"
    }]

    // Stop timeout
    stopTimeout?: int & >=0 & <=120

    // Ulimits
    ulimits?: [...{
        name:      string
        softLimit: int
        hardLimit: int
    }]

    // Docker labels
    dockerLabels?: [string]: string

    // Privileged mode (EC2 only)
    privileged?: bool

    // Read-only root filesystem
    readonlyRootFilesystem?: bool
}

#Volume: {
    name: string
    // Host path (EC2 only)
    host?: {
        sourcePath: string
    }
    // EFS volume
    efsVolumeConfiguration?: {
        fileSystemId:          string
        rootDirectory?:        string
        transitEncryption?:    "ENABLED" | "DISABLED"
        transitEncryptionPort?: int
        authorizationConfig?: {
            accessPointId?: string
            iam?:           "ENABLED" | "DISABLED"
        }
    }
    // Docker volume
    dockerVolumeConfiguration?: {
        scope?:         "task" | "shared"
        autoprovision?: bool
        driver?:        string
        driverOpts?: [string]: string
        labels?: [string]:     string
    }
}

#PlacementConstraint: {
    type:        "memberOf"
    expression?: string
}
```

### Schema Design Notes

- **`containerDefinitions` requires at least one container**: A task definition
  without containers is invalid.
- **`cpu` and `memory` are strings**: AWS models these as string values for Fargate
  (e.g., `"256"`, `"512"`). For EC2, they can be omitted and container-level
  limits are used instead.
- **`secrets[].valueFrom`**: References SSM Parameter Store parameters or Secrets
  Manager secrets by ARN. The ECS agent resolves these at task launch time.
- **`logConfiguration.logDriver`**: The most common driver for AWS is `"awslogs"`
  (CloudWatch Logs). The schema allows all standard Docker log drivers.
- **Container `essential` defaults to `true`**: If an essential container stops,
  the entire task is stopped. At least one container must be essential.

---

## Step 2 — AWS Client Factory

**File**: `internal/infra/awsclient/client.go` — shared with ECS Cluster driver.

The `NewECSClient()` factory is added as part of the ECS Cluster driver (Phase 1).
No additional client factory is needed.

---

## Step 3 — Driver Types

**File**: `internal/drivers/ecstaskdef/types.go`

```go
package ecstaskdef

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate Virtual Object name for ECS Task Definitions.
const ServiceName = "ECSTaskDefinition"

// ECSTaskDefinitionSpec is the desired state for an ECS task definition.
type ECSTaskDefinitionSpec struct {
    Account                  string                    `json:"account,omitempty"`
    Region                   string                    `json:"region"`
    Family                   string                    `json:"family"`
    ContainerDefinitions     []ContainerDefinition     `json:"containerDefinitions"`
    CPU                      string                    `json:"cpu,omitempty"`
    Memory                   string                    `json:"memory,omitempty"`
    NetworkMode              string                    `json:"networkMode"`
    RequiresCompatibilities  []string                  `json:"requiresCompatibilities,omitempty"`
    TaskRoleArn              string                    `json:"taskRoleArn,omitempty"`
    ExecutionRoleArn         string                    `json:"executionRoleArn,omitempty"`
    Volumes                  []Volume                  `json:"volumes,omitempty"`
    PlacementConstraints     []PlacementConstraint     `json:"placementConstraints,omitempty"`
    RuntimePlatform          *RuntimePlatform          `json:"runtimePlatform,omitempty"`
    ProxyConfiguration       *ProxyConfiguration       `json:"proxyConfiguration,omitempty"`
    EphemeralStorage         *EphemeralStorage         `json:"ephemeralStorage,omitempty"`
    PidMode                  string                    `json:"pidMode,omitempty"`
    IpcMode                  string                    `json:"ipcMode,omitempty"`
    Tags                     map[string]string         `json:"tags,omitempty"`
    ManagedKey               string                    `json:"managedKey,omitempty"`
}

// ContainerDefinition defines a container within a task.
type ContainerDefinition struct {
    Name                    string                 `json:"name"`
    Image                   string                 `json:"image"`
    CPU                     int32                  `json:"cpu,omitempty"`
    Memory                  int32                  `json:"memory,omitempty"`
    MemoryReservation       int32                  `json:"memoryReservation,omitempty"`
    PortMappings            []PortMapping          `json:"portMappings,omitempty"`
    HealthCheck             *HealthCheck           `json:"healthCheck,omitempty"`
    Essential               *bool                  `json:"essential,omitempty"`
    Environment             []KeyValuePair         `json:"environment,omitempty"`
    Secrets                 []Secret               `json:"secrets,omitempty"`
    EnvironmentFiles        []EnvironmentFile      `json:"environmentFiles,omitempty"`
    LogConfiguration        *LogConfiguration      `json:"logConfiguration,omitempty"`
    MountPoints             []MountPoint           `json:"mountPoints,omitempty"`
    VolumesFrom             []VolumeFrom           `json:"volumesFrom,omitempty"`
    LinuxParameters         *LinuxParameters       `json:"linuxParameters,omitempty"`
    EntryPoint              []string               `json:"entryPoint,omitempty"`
    Command                 []string               `json:"command,omitempty"`
    WorkingDirectory        string                 `json:"workingDirectory,omitempty"`
    User                    string                 `json:"user,omitempty"`
    DependsOn               []ContainerDependency  `json:"dependsOn,omitempty"`
    StopTimeout             *int32                 `json:"stopTimeout,omitempty"`
    Ulimits                 []Ulimit               `json:"ulimits,omitempty"`
    DockerLabels            map[string]string      `json:"dockerLabels,omitempty"`
    Privileged              *bool                  `json:"privileged,omitempty"`
    ReadonlyRootFilesystem  *bool                  `json:"readonlyRootFilesystem,omitempty"`
}

// PortMapping maps a container port to a host port.
type PortMapping struct {
    ContainerPort int32  `json:"containerPort"`
    HostPort      int32  `json:"hostPort,omitempty"`
    Protocol      string `json:"protocol,omitempty"`
    Name          string `json:"name,omitempty"`
    AppProtocol   string `json:"appProtocol,omitempty"`
}

// HealthCheck defines a container health check.
type HealthCheck struct {
    Command     []string `json:"command"`
    Interval    int32    `json:"interval,omitempty"`
    Timeout     int32    `json:"timeout,omitempty"`
    Retries     int32    `json:"retries,omitempty"`
    StartPeriod int32    `json:"startPeriod,omitempty"`
}

// KeyValuePair is a name-value environment variable.
type KeyValuePair struct {
    Name  string `json:"name"`
    Value string `json:"value"`
}

// Secret defines a secret injected from SSM or Secrets Manager.
type Secret struct {
    Name      string `json:"name"`
    ValueFrom string `json:"valueFrom"`
}

// EnvironmentFile references an S3 object containing environment variables.
type EnvironmentFile struct {
    Value string `json:"value"`
    Type  string `json:"type"`
}

// LogConfiguration defines container log routing.
type LogConfiguration struct {
    LogDriver     string            `json:"logDriver"`
    Options       map[string]string `json:"options,omitempty"`
    SecretOptions []Secret          `json:"secretOptions,omitempty"`
}

// MountPoint mounts a task volume into a container.
type MountPoint struct {
    SourceVolume  string `json:"sourceVolume"`
    ContainerPath string `json:"containerPath"`
    ReadOnly      bool   `json:"readOnly,omitempty"`
}

// VolumeFrom mounts volumes from another container.
type VolumeFrom struct {
    SourceContainer string `json:"sourceContainer"`
    ReadOnly        bool   `json:"readOnly,omitempty"`
}

// LinuxParameters for the container.
type LinuxParameters struct {
    Capabilities       *Capabilities `json:"capabilities,omitempty"`
    InitProcessEnabled *bool         `json:"initProcessEnabled,omitempty"`
}

// Capabilities defines Linux capabilities to add or drop.
type Capabilities struct {
    Add  []string `json:"add,omitempty"`
    Drop []string `json:"drop,omitempty"`
}

// ContainerDependency defines startup ordering between containers.
type ContainerDependency struct {
    ContainerName string `json:"containerName"`
    Condition     string `json:"condition"`
}

// Ulimit defines a Linux ulimit override.
type Ulimit struct {
    Name      string `json:"name"`
    SoftLimit int32  `json:"softLimit"`
    HardLimit int32  `json:"hardLimit"`
}

// Volume defines a task-level data volume.
type Volume struct {
    Name                       string                      `json:"name"`
    Host                       *HostVolume                 `json:"host,omitempty"`
    EfsVolumeConfiguration     *EFSVolumeConfiguration     `json:"efsVolumeConfiguration,omitempty"`
    DockerVolumeConfiguration  *DockerVolumeConfiguration  `json:"dockerVolumeConfiguration,omitempty"`
}

// HostVolume references a path on the host (EC2 only).
type HostVolume struct {
    SourcePath string `json:"sourcePath"`
}

// EFSVolumeConfiguration defines an EFS volume.
type EFSVolumeConfiguration struct {
    FileSystemId          string                `json:"fileSystemId"`
    RootDirectory         string                `json:"rootDirectory,omitempty"`
    TransitEncryption     string                `json:"transitEncryption,omitempty"`
    TransitEncryptionPort *int32                `json:"transitEncryptionPort,omitempty"`
    AuthorizationConfig   *EFSAuthConfig        `json:"authorizationConfig,omitempty"`
}

// EFSAuthConfig for EFS access points and IAM.
type EFSAuthConfig struct {
    AccessPointId string `json:"accessPointId,omitempty"`
    IAM           string `json:"iam,omitempty"`
}

// DockerVolumeConfiguration defines a Docker-managed volume.
type DockerVolumeConfiguration struct {
    Scope         string            `json:"scope,omitempty"`
    Autoprovision *bool             `json:"autoprovision,omitempty"`
    Driver        string            `json:"driver,omitempty"`
    DriverOpts    map[string]string `json:"driverOpts,omitempty"`
    Labels        map[string]string `json:"labels,omitempty"`
}

// PlacementConstraint limits EC2 task placement.
type PlacementConstraint struct {
    Type       string `json:"type"`
    Expression string `json:"expression,omitempty"`
}

// RuntimePlatform defines the OS and CPU architecture.
type RuntimePlatform struct {
    CPUArchitecture       string `json:"cpuArchitecture,omitempty"`
    OperatingSystemFamily string `json:"operatingSystemFamily,omitempty"`
}

// ProxyConfiguration for App Mesh.
type ProxyConfiguration struct {
    ContainerName string           `json:"containerName"`
    Type          string           `json:"type"`
    Properties    []KeyValuePair   `json:"properties,omitempty"`
}

// EphemeralStorage configures Fargate ephemeral storage.
type EphemeralStorage struct {
    SizeInGiB int32 `json:"sizeInGiB"`
}

// ECSTaskDefinitionOutputs is produced after registration and stored in Restate K/V.
type ECSTaskDefinitionOutputs struct {
    TaskDefinitionArn string `json:"taskDefinitionArn"`
    Family            string `json:"family"`
    Revision          int32  `json:"revision"`
    Status            string `json:"status"`
}

// ObservedState captures the actual configuration of the latest active revision.
type ObservedState struct {
    TaskDefinitionArn        string                `json:"taskDefinitionArn"`
    Family                   string                `json:"family"`
    Revision                 int32                 `json:"revision"`
    Status                   string                `json:"status"`
    ContainerDefinitions     []ContainerDefinition `json:"containerDefinitions"`
    CPU                      string                `json:"cpu,omitempty"`
    Memory                   string                `json:"memory,omitempty"`
    NetworkMode              string                `json:"networkMode"`
    RequiresCompatibilities  []string              `json:"requiresCompatibilities,omitempty"`
    TaskRoleArn              string                `json:"taskRoleArn,omitempty"`
    ExecutionRoleArn         string                `json:"executionRoleArn,omitempty"`
    Volumes                  []Volume              `json:"volumes,omitempty"`
    PlacementConstraints     []PlacementConstraint `json:"placementConstraints,omitempty"`
    RuntimePlatform          *RuntimePlatform      `json:"runtimePlatform,omitempty"`
    EphemeralStorage         *EphemeralStorage     `json:"ephemeralStorage,omitempty"`
    Tags                     map[string]string     `json:"tags,omitempty"`
}

// ECSTaskDefinitionState is the single atomic state object stored under drivers.StateKey.
type ECSTaskDefinitionState struct {
    Desired            ECSTaskDefinitionSpec     `json:"desired"`
    Observed           ObservedState             `json:"observed"`
    Outputs            ECSTaskDefinitionOutputs  `json:"outputs"`
    Status             types.ResourceStatus      `json:"status"`
    Mode               types.Mode                `json:"mode"`
    Error              string                    `json:"error,omitempty"`
    Generation         int64                     `json:"generation"`
    LastReconcile      string                    `json:"lastReconcile,omitempty"`
    ReconcileScheduled bool                      `json:"reconcileScheduled"`
}
```

### Types Design Notes

- **`ContainerDefinition` is complex**: This is one of the largest type structures
  in the driver ecosystem. Each container can have ports, health checks, env vars,
  secrets, log config, mount points, and more.
- **`Essential` is a pointer**: Go's zero value for `bool` is `false`, but ECS
  defaults `essential` to `true`. Using `*bool` allows distinguishing between
  "not set" (nil → default true) and "explicitly false".
- **`ObservedState` mirrors the spec shape**: The describe response returns the
  full task definition, making comparison straightforward.

---

## Step 4 — AWS API Abstraction Layer

**File**: `internal/drivers/ecstaskdef/aws.go`

### ECSTaskDefAPI Interface

```go
type ECSTaskDefAPI interface {
    // RegisterTaskDefinition registers a new task definition revision.
    RegisterTaskDefinition(ctx context.Context, spec ECSTaskDefinitionSpec) (ECSTaskDefinitionOutputs, error)

    // DescribeTaskDefinition returns the specified task definition.
    // If revision is 0, returns the latest ACTIVE revision for the family.
    DescribeTaskDefinition(ctx context.Context, familyOrArn string) (ObservedState, error)

    // DeregisterTaskDefinition marks a specific revision as INACTIVE.
    DeregisterTaskDefinition(ctx context.Context, taskDefinitionArn string) error

    // ListTaskDefinitions lists all ACTIVE revisions for a family.
    ListTaskDefinitions(ctx context.Context, family string) ([]string, error)

    // TagTaskDefinition replaces tags on the task definition.
    TagTaskDefinition(ctx context.Context, resourceArn string, tags map[string]string) error

    // UntagTaskDefinition removes tag keys from the task definition.
    UntagTaskDefinition(ctx context.Context, resourceArn string, keys []string) error
}
```

### Implementation: realECSTaskDefAPI

```go
type realECSTaskDefAPI struct {
    client  *ecs.Client
    limiter ratelimit.Limiter
}

func newRealECSTaskDefAPI(client *ecs.Client, limiter ratelimit.Limiter) ECSTaskDefAPI {
    return &realECSTaskDefAPI{client: client, limiter: limiter}
}
```

### Error Classification

```go
func isNotFound(err error) bool {
    var nf *ecstypes.ClientException
    if errors.As(err, &nf) {
        // DescribeTaskDefinition returns ClientException for non-existent families
        return strings.Contains(nf.Error(), "Unable to describe task definition")
    }
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
```

### Key Implementation Details

#### RegisterTaskDefinition

```go
func (r *realECSTaskDefAPI) RegisterTaskDefinition(ctx context.Context, spec ECSTaskDefinitionSpec) (ECSTaskDefinitionOutputs, error) {
    r.limiter.Wait(ctx)

    input := &ecs.RegisterTaskDefinitionInput{
        Family:                  &spec.Family,
        ContainerDefinitions:    toECSContainerDefs(spec.ContainerDefinitions),
        NetworkMode:             ecstypes.NetworkMode(spec.NetworkMode),
        Tags:                    toECSTags(spec.Tags),
    }

    if spec.CPU != "" {
        input.Cpu = &spec.CPU
    }
    if spec.Memory != "" {
        input.Memory = &spec.Memory
    }
    if len(spec.RequiresCompatibilities) > 0 {
        input.RequiresCompatibilities = toCompatibilities(spec.RequiresCompatibilities)
    }
    if spec.TaskRoleArn != "" {
        input.TaskRoleArn = &spec.TaskRoleArn
    }
    if spec.ExecutionRoleArn != "" {
        input.ExecutionRoleArn = &spec.ExecutionRoleArn
    }
    if len(spec.Volumes) > 0 {
        input.Volumes = toECSVolumes(spec.Volumes)
    }
    if len(spec.PlacementConstraints) > 0 {
        input.PlacementConstraints = toECSPlacementConstraints(spec.PlacementConstraints)
    }
    if spec.RuntimePlatform != nil {
        input.RuntimePlatform = &ecstypes.RuntimePlatform{
            CpuArchitecture:       ecstypes.CPUArchitecture(spec.RuntimePlatform.CPUArchitecture),
            OperatingSystemFamily: ecstypes.OSFamily(spec.RuntimePlatform.OperatingSystemFamily),
        }
    }
    if spec.EphemeralStorage != nil {
        input.EphemeralStorage = &ecstypes.EphemeralStorage{
            SizeInGiB: spec.EphemeralStorage.SizeInGiB,
        }
    }
    if spec.PidMode != "" {
        input.PidMode = ecstypes.PidMode(spec.PidMode)
    }
    if spec.IpcMode != "" {
        input.IpcMode = ecstypes.IpcMode(spec.IpcMode)
    }
    if spec.ProxyConfiguration != nil {
        input.ProxyConfiguration = toECSProxyConfig(spec.ProxyConfiguration)
    }

    out, err := r.client.RegisterTaskDefinition(ctx, input)
    if err != nil {
        return ECSTaskDefinitionOutputs{}, err
    }

    td := out.TaskDefinition
    return ECSTaskDefinitionOutputs{
        TaskDefinitionArn: aws.ToString(td.TaskDefinitionArn),
        Family:            aws.ToString(td.Family),
        Revision:          td.Revision,
        Status:            string(td.Status),
    }, nil
}
```

#### DescribeTaskDefinition

```go
func (r *realECSTaskDefAPI) DescribeTaskDefinition(ctx context.Context, familyOrArn string) (ObservedState, error) {
    r.limiter.Wait(ctx)

    out, err := r.client.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{
        TaskDefinition: &familyOrArn,
        Include:        []ecstypes.TaskDefinitionField{ecstypes.TaskDefinitionFieldTags},
    })
    if err != nil {
        return ObservedState{}, err
    }

    td := out.TaskDefinition
    return ObservedState{
        TaskDefinitionArn:       aws.ToString(td.TaskDefinitionArn),
        Family:                  aws.ToString(td.Family),
        Revision:                td.Revision,
        Status:                  string(td.Status),
        ContainerDefinitions:    fromECSContainerDefs(td.ContainerDefinitions),
        CPU:                     aws.ToString(td.Cpu),
        Memory:                  aws.ToString(td.Memory),
        NetworkMode:             string(td.NetworkMode),
        RequiresCompatibilities: fromCompatibilities(td.RequiresCompatibilities),
        TaskRoleArn:             aws.ToString(td.TaskRoleArn),
        ExecutionRoleArn:        aws.ToString(td.ExecutionRoleArn),
        Volumes:                 fromECSVolumes(td.Volumes),
        PlacementConstraints:    fromECSPlacementConstraints(td.PlacementConstraints),
        RuntimePlatform:         fromECSRuntimePlatform(td.RuntimePlatform),
        EphemeralStorage:        fromECSEphemeralStorage(td.EphemeralStorage),
        Tags:                    fromECSTags(out.Tags),
    }, nil
}
```

**Note**: When `familyOrArn` is just a family name (no `:revision` suffix),
`DescribeTaskDefinition` returns the latest ACTIVE revision.

---

## Step 5 — Drift Detection

**File**: `internal/drivers/ecstaskdef/drift.go`

### Drift Detection Strategy

Task definitions are immutable per revision. "Drift" in this context means:

1. **Spec changed**: The desired spec differs from the currently active revision.
   This is detected by comparing the desired `ECSTaskDefinitionSpec` fields against
   the `ObservedState` of the latest revision.
2. **External registration**: Someone registered a new revision outside Praxis.
   The driver detects this by comparing the stored revision number with the actual
   latest revision.

### Drift-Detectable Fields

| Field | Notes |
|---|---|
| Container definitions | Image, ports, env vars, health check, etc. |
| CPU / Memory | Task-level resource limits |
| Network mode | awsvpc, bridge, host, none |
| Task role ARN | Application IAM role |
| Execution role ARN | ECS agent IAM role |
| Volumes | EFS, Docker, host volumes |
| Placement constraints | memberOf expressions |
| Runtime platform | OS family, CPU architecture |
| Requires compatibilities | EC2, FARGATE |
| Tags | Family-level tags |

### HasDrift

```go
func HasDrift(desired ECSTaskDefinitionSpec, observed ObservedState) bool {
    if !containerDefsMatch(desired.ContainerDefinitions, observed.ContainerDefinitions) { return true }
    if desired.CPU != observed.CPU { return true }
    if desired.Memory != observed.Memory { return true }
    if desired.NetworkMode != observed.NetworkMode { return true }
    if desired.TaskRoleArn != observed.TaskRoleArn { return true }
    if desired.ExecutionRoleArn != observed.ExecutionRoleArn { return true }
    if !slicesEqual(desired.RequiresCompatibilities, observed.RequiresCompatibilities) { return true }
    if !volumesMatch(desired.Volumes, observed.Volumes) { return true }
    if !placementConstraintsMatch(desired.PlacementConstraints, observed.PlacementConstraints) { return true }
    if !runtimePlatformMatch(desired.RuntimePlatform, observed.RuntimePlatform) { return true }
    if !tagsMatch(desired.Tags, observed.Tags) { return true }
    return false
}
```

### containerDefsMatch

Container definition comparison is the most complex part of drift detection.
The function compares containers by name and checks each field:

```go
func containerDefsMatch(desired, observed []ContainerDefinition) bool {
    if len(desired) != len(observed) { return false }
    // Sort both by name for order-independent comparison
    dSorted := sortByName(desired)
    oSorted := sortByName(observed)
    for i := range dSorted {
        if !singleContainerMatch(dSorted[i], oSorted[i]) { return false }
    }
    return true
}
```

---

## Step 6 — Driver Implementation

**File**: `internal/drivers/ecstaskdef/driver.go`

### Constructor

```go
type ECSTaskDefinitionDriver struct {
    accounts *auth.Registry
}

func NewECSTaskDefinitionDriver(accounts *auth.Registry) *ECSTaskDefinitionDriver {
    return &ECSTaskDefinitionDriver{accounts: accounts}
}

func (ECSTaskDefinitionDriver) ServiceName() string { return ServiceName }
```

### Provision

The Provision handler always compares the desired spec against the current revision.
If they differ, it registers a new revision.

```go
func (d *ECSTaskDefinitionDriver) Provision(ctx restate.ObjectContext, spec ECSTaskDefinitionSpec) (ECSTaskDefinitionOutputs, error) {
    // 1. Load existing state
    state, _ := restate.Get[*ECSTaskDefinitionState](ctx, drivers.StateKey)

    // 2. Build API client
    api := d.buildAPI(spec.Account, spec.Region)

    // 3. If no existing state → register first revision
    if state == nil || state.Outputs.TaskDefinitionArn == "" {
        return d.registerTaskDefinition(ctx, api, spec)
    }

    // 4. Existing family — compare spec with current revision
    if !HasDrift(spec, state.Observed) {
        // No changes needed; update desired in state and return current outputs
        state.Desired = spec
        restate.Set(ctx, drivers.StateKey, state)
        return state.Outputs, nil
    }

    // 5. Spec changed → register new revision
    return d.registerNewRevision(ctx, api, spec, state)
}
```

#### Register First Revision

```go
func (d *ECSTaskDefinitionDriver) registerTaskDefinition(ctx restate.ObjectContext, api ECSTaskDefAPI, spec ECSTaskDefinitionSpec) (ECSTaskDefinitionOutputs, error) {
    restate.Set(ctx, drivers.StateKey, &ECSTaskDefinitionState{
        Desired: spec,
        Status:  types.StatusProvisioning,
        Mode:    drivers.DefaultMode(""),
    })

    outputs, err := restate.Run(ctx, func(rc restate.RunContext) (ECSTaskDefinitionOutputs, error) {
        return api.RegisterTaskDefinition(rc, spec)
    })
    if err != nil {
        return ECSTaskDefinitionOutputs{}, err
    }

    observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
        return api.DescribeTaskDefinition(rc, outputs.TaskDefinitionArn)
    })
    if err != nil {
        return ECSTaskDefinitionOutputs{}, err
    }

    restate.Set(ctx, drivers.StateKey, &ECSTaskDefinitionState{
        Desired:    spec,
        Observed:   observed,
        Outputs:    outputs,
        Status:     types.StatusReady,
        Mode:       types.ModeManaged,
        Generation: 1,
    })

    d.scheduleReconcile(ctx)
    return outputs, nil
}
```

#### Register New Revision (Update)

```go
func (d *ECSTaskDefinitionDriver) registerNewRevision(ctx restate.ObjectContext, api ECSTaskDefAPI, spec ECSTaskDefinitionSpec, state *ECSTaskDefinitionState) (ECSTaskDefinitionOutputs, error) {
    state.Desired = spec
    state.Status = types.StatusProvisioning
    state.Generation++
    restate.Set(ctx, drivers.StateKey, state)

    // Register new revision
    outputs, err := restate.Run(ctx, func(rc restate.RunContext) (ECSTaskDefinitionOutputs, error) {
        return api.RegisterTaskDefinition(rc, spec)
    })
    if err != nil {
        return ECSTaskDefinitionOutputs{}, err
    }

    // Optionally deregister old revision
    oldArn := state.Outputs.TaskDefinitionArn
    if oldArn != "" && oldArn != outputs.TaskDefinitionArn {
        if _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
            return restate.Void{}, api.DeregisterTaskDefinition(rc, oldArn)
        }); err != nil {
            // Non-fatal: old revision cleanup failure shouldn't block provision
            slog.Warn("failed to deregister old revision", "arn", oldArn, "err", err)
        }
    }

    observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
        return api.DescribeTaskDefinition(rc, outputs.TaskDefinitionArn)
    })
    if err != nil {
        return ECSTaskDefinitionOutputs{}, err
    }

    restate.Set(ctx, drivers.StateKey, &ECSTaskDefinitionState{
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
func (d *ECSTaskDefinitionDriver) Delete(ctx restate.ObjectContext) error {
    state, _ := restate.Get[*ECSTaskDefinitionState](ctx, drivers.StateKey)
    if state == nil {
        return nil
    }
    if state.Mode == types.ModeObserved {
        restate.Clear(ctx, drivers.StateKey)
        return nil
    }

    api := d.buildAPI(state.Desired.Account, state.Desired.Region)

    // List all ACTIVE revisions and deregister them
    arns, err := restate.Run(ctx, func(rc restate.RunContext) ([]string, error) {
        return api.ListTaskDefinitions(rc, state.Desired.Family)
    })
    if err != nil {
        return err
    }

    for _, arn := range arns {
        if _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
            return restate.Void{}, api.DeregisterTaskDefinition(rc, arn)
        }); err != nil {
            if !isNotFound(err) {
                return err
            }
        }
    }

    restate.Clear(ctx, drivers.StateKey)
    return nil
}
```

### Import

```go
func (d *ECSTaskDefinitionDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (ECSTaskDefinitionOutputs, error) {
    api := d.buildAPI(ref.Account, ref.Region)

    // Describe latest active revision
    observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
        return api.DescribeTaskDefinition(rc, ref.ResourceID)
    })
    if err != nil {
        return ECSTaskDefinitionOutputs{}, err
    }

    outputs := ECSTaskDefinitionOutputs{
        TaskDefinitionArn: observed.TaskDefinitionArn,
        Family:            observed.Family,
        Revision:          observed.Revision,
        Status:            observed.Status,
    }

    mode := types.ModeObserved
    if ref.Mode != "" {
        mode = ref.Mode
    }

    restate.Set(ctx, drivers.StateKey, &ECSTaskDefinitionState{
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

For task definitions, reconcile checks whether:
1. The latest active revision matches what the driver expects (detecting external
   registrations or deregistrations).
2. Tags have been modified externally (tags are mutable even on task definitions).

In managed mode, if someone registered a new revision externally, the driver
re-registers a revision matching the desired spec. In observed mode, it updates
the observed state silently.

---

## Step 7 — Provider Adapter

**File**: `internal/core/provider/ecstaskdef_adapter.go`

```go
type ECSTaskDefinitionAdapter struct {
    accounts *auth.Registry
}

func NewECSTaskDefinitionAdapterWithRegistry(accounts *auth.Registry) *ECSTaskDefinitionAdapter {
    return &ECSTaskDefinitionAdapter{accounts: accounts}
}

func (a *ECSTaskDefinitionAdapter) Kind() string { return "ECSTaskDefinition" }

func (a *ECSTaskDefinitionAdapter) ServiceName() string { return ecstaskdef.ServiceName }

func (a *ECSTaskDefinitionAdapter) Scope() KeyScope { return KeyScopeRegion }

func (a *ECSTaskDefinitionAdapter) BuildKey(doc json.RawMessage) (string, error) {
    region, _ := jsonpath.String(doc, "spec.region")
    name, _ := jsonpath.String(doc, "metadata.name")
    if region == "" || name == "" {
        return "", fmt.Errorf("ECSTaskDefinition requires spec.region and metadata.name")
    }
    return region + "~" + name, nil
}

func (a *ECSTaskDefinitionAdapter) BuildImportKey(region, resourceID string) (string, error) {
    return region + "~" + resourceID, nil
}
```

---

## Step 8 — Registry Integration

**File**: `internal/core/provider/registry.go` — add to `NewRegistry()`:

```go
// Added to NewRegistryWithAdapters() call:
NewECSTaskDefinitionAdapterWithRegistry(accounts),
```

---

## Step 9 — Compute Driver Pack Entry Point

**File**: `cmd/praxis-compute/main.go` — add Bind call:

```go
Bind(restate.Reflect(ecstaskdef.NewECSTaskDefinitionDriver(cfg.Auth())))
```

---

## Step 10 — Docker Compose & Justfile

### Docker Compose

No changes needed — hosted in existing `praxis-compute` service.

### Justfile

```just
test-ecs-taskdef:
    go test ./internal/drivers/ecstaskdef/... -v -count=1 -race

test-ecs-taskdef-integration:
    go test ./tests/integration/ -run TestECSTaskDefinition -v -count=1 -tags=integration -timeout=5m
```

---

## Step 11 — Unit Tests

**File**: `internal/drivers/ecstaskdef/driver_test.go`

### Test Cases

| Test | Description |
|---|---|
| `TestProvision_FirstRevision` | Register the first revision of a task definition |
| `TestProvision_NewRevision` | Spec changed → register new revision, deregister old |
| `TestProvision_NoChanges` | Spec unchanged → no new revision registered |
| `TestProvision_MultipleContainers` | Task definition with 3 containers, sidecars |
| `TestProvision_FargateTaskDef` | Fargate-compatible with cpu/memory/networkMode constraints |
| `TestProvision_EC2TaskDef` | EC2-compatible with bridge networking |
| `TestProvision_WithVolumes` | Task definition with EFS and Docker volumes |
| `TestDelete_AllRevisions` | Deregister all active revisions |
| `TestDelete_AlreadyGone` | Family doesn't exist → no error |
| `TestDelete_ObservedMode` | Delete in observed mode → clear state only |
| `TestImport_Success` | Import existing task definition family |
| `TestImport_NotFound` | Import non-existent family → error |
| `TestReconcile_ExternalRevision` | Someone registered a new revision externally |
| `TestReconcile_NoDrift` | Latest revision matches expected |
| `TestGetStatus` | Returns current status from state |
| `TestGetOutputs` | Returns current outputs from state |

---

## Step 12 — Integration Tests

**File**: `tests/integration/ecstaskdef_driver_test.go`

### Test Scenarios

1. **Register → Describe → Deregister**: Full lifecycle of a simple task definition.
2. **Register → Update → Verify new revision**: Spec change triggers new revision.
3. **Multi-container task**: Task with web + sidecar containers.
4. **Fargate task definition**: With cpu, memory, awsvpc, runtime platform.
5. **Import → Verify observed state**: Import existing family and verify snapshot.

---

## Task-Definition-Specific Design Decisions

### Versioned Immutable Model (Analogous to Lambda Layer)

Like Lambda Layers, task definitions are versioned and immutable per revision.
The driver does NOT attempt to "update" an existing revision — it always registers
a new one. This is a fundamental ECS design constraint, not a driver choice.

### Old Revision Cleanup

When a new revision is registered, the driver deregisters the previous revision.
This prevents revision accumulation (ECS families can have up to 1,000,000
revisions per family). Deregistration is best-effort — failure does not block
the provision operation.

**Deregistered revisions are not deleted**: `DeregisterTaskDefinition` marks a
revision as `INACTIVE`, but it remains visible via `DescribeTaskDefinition` with
its full ARN. AWS eventually garbage-collects INACTIVE revisions.

### Container Definition Comparison Must Be Order-Independent

ECS returns container definitions in the order they were registered, but the user
might reorder them in the CUE template. The drift detection function sorts
containers by name before comparing to avoid false-positive drift signals.

### Task Definition ARN vs Family for DescribeTaskDefinition

- `DescribeTaskDefinition("myapp-web")` → latest ACTIVE revision
- `DescribeTaskDefinition("myapp-web:3")` → specific revision 3
- `DescribeTaskDefinition("arn:aws:ecs:…:task-definition/myapp-web:3")` → specific revision by ARN

The driver uses the family name for discovery and the full ARN for precise reference.

### Tags on Task Definitions

Tags can be set at registration time (`RegisterTaskDefinition.tags`) and modified
afterward via `TagResource`/`UntagResource`. The driver sets tags at registration
and reconciles them separately if they drift.

---

## Checklist

- [ ] `schemas/aws/ecs/task_definition.cue`
- [ ] `internal/drivers/ecstaskdef/types.go`
- [ ] `internal/drivers/ecstaskdef/aws.go`
- [ ] `internal/drivers/ecstaskdef/drift.go`
- [ ] `internal/drivers/ecstaskdef/driver.go`
- [ ] `internal/drivers/ecstaskdef/driver_test.go`
- [ ] `internal/drivers/ecstaskdef/aws_test.go`
- [ ] `internal/drivers/ecstaskdef/drift_test.go`
- [ ] `internal/core/provider/ecstaskdef_adapter.go`
- [ ] `internal/core/provider/ecstaskdef_adapter_test.go`
- [ ] `tests/integration/ecstaskdef_driver_test.go`
- [ ] `cmd/praxis-compute/main.go` — Bind ECSTaskDefinition driver
- [ ] `internal/core/provider/registry.go` — Register adapter
- [ ] `justfile` — Add task definition test targets
