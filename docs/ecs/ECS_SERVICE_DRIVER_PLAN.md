# ECS Service Driver — Implementation Plan

> NYI
> Target: A Restate Virtual Object driver that manages AWS ECS Services, following
> the exact patterns established by the S3, Security Group, EC2, VPC, EBS, Elastic IP,
> Key Pair, AMI, IAM, ECS Cluster, and ECS Task Definition drivers.
>
> Key scope: `KeyScopeRegion` — key format is `region~clusterName~serviceName`,
> permanent and immutable for the lifetime of the Virtual Object. The AWS-assigned
> service ARN lives only in state/outputs.

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
16. [ECS-Service-Specific Design Decisions](#ecs-service-specific-design-decisions)
17. [Checklist](#checklist)

---

## 1. Overview & Scope

The ECS Service driver manages the lifecycle of **ECS services** — the long-running
controller that maintains a desired count of tasks from a task definition within a
cluster. This is the most complex driver in the ECS family due to rolling deployments,
circuit breaker configuration, load balancer integration, and network configuration.

The driver follows the established Virtual Object contract:

| Handler | Context | Purpose |
|---|---|---|
| `Provision` | `ObjectContext` (exclusive) | Create or converge an ECS service |
| `Import` | `ObjectContext` (exclusive) | Adopt an existing service |
| `Delete` | `ObjectContext` (exclusive) | Delete a service (scale to 0, then delete) |
| `Reconcile` | `ObjectContext` (exclusive) | Detect/correct drift (report-only for Observed mode) |
| `GetStatus` | `ObjectSharedContext` (shared) | Return current status |
| `GetOutputs` | `ObjectSharedContext` (shared) | Return service outputs |

### Mutable vs Immutable Attributes

| Attribute | Mutability | API | Notes |
|---|---|---|---|
| Service name | Immutable | — | Set at creation; cannot be changed |
| Service ARN | Immutable | — | AWS-assigned |
| Cluster | Immutable | — | Set at creation; cannot be changed |
| Launch type | Immutable | — | Set at creation; changing requires delete + recreate |
| Load balancers | **Immutable** | — | **Cannot be changed after creation**; must delete + recreate |
| Service registries | **Immutable** | — | **Cannot be changed after creation**; must delete + recreate |
| Task definition | Mutable | `UpdateService` | Updated to deploy new revisions |
| Desired count | Mutable | `UpdateService` | Scale up/down |
| Deployment configuration | Mutable | `UpdateService` | Max percent, min healthy, circuit breaker |
| Network configuration | Mutable | `UpdateService` | Subnets, security groups, public IP (awsvpc only) |
| Capacity provider strategy | Mutable | `UpdateService` | Weight/base allocation |
| Platform version | Mutable | `UpdateService` | Fargate platform version (e.g., `LATEST`, `1.4.0`) |
| Health check grace period | Mutable | `UpdateService` | Seconds to wait before ELB health checks count |
| Enable execute command | Mutable | `UpdateService` | ECS Exec toggle |
| Force new deployment | Mutable | `UpdateService` | Forces task replacement even without spec changes |
| Placement constraints | Mutable | `UpdateService` | EC2 placement expressions |
| Placement strategy | Mutable | `UpdateService` | EC2 task spread/binpack strategy |
| Service connect config | Mutable | `UpdateService` | Service Connect namespace and services |
| Tags | Mutable | `TagResource` / `UntagResource` | Key-value pairs |

### Immutable Fields That Require Recreation

The following fields **cannot** be updated after creation. If the desired spec
changes any of these, the driver must signal `OpRecreate` during planning, and the
orchestrator handles delete + create sequencing:

- **`loadBalancers`** — Target group associations are permanently bound at creation.
- **`serviceRegistries`** — Cloud Map registrations are permanent.
- **`launchType`** — Cannot switch between `FARGATE` and `EC2` on an existing service.
- **`cluster`** — Services cannot be moved between clusters.
- **`schedulingStrategy`** — `REPLICA` vs `DAEMON` is permanent.

### What Is NOT In Scope

- **Tasks**: Individual task instances are managed by the ECS scheduler. The service
  driver controls the desired count; ECS handles placement, restarts, and drain.
- **Auto Scaling**: Application Auto Scaling policies for ECS services are a separate
  resource type (future driver). The service driver manages the static `desiredCount`.
- **Service Discovery registration**: The driver passes `serviceRegistries` at
  creation but does not manage Cloud Map namespaces or services themselves.
- **Task sets**: Used for external deployment controllers (CODE_DEPLOY). Task sets
  are a separate advanced concept not covered by this driver.
- **Deployment controller**: The driver uses the default `ECS` rolling deployment
  controller. `CODE_DEPLOY` and `EXTERNAL` controllers require different lifecycle
  management and are out of scope.

### Downstream Consumers

```
${resources.my-service.outputs.serviceArn}          → Monitoring, Auto Scaling, CI/CD
${resources.my-service.outputs.serviceName}          → CLI references, logging
${resources.my-service.outputs.clusterArn}           → Cross-references
${resources.my-service.outputs.desiredCount}         → Scaling verification
${resources.my-service.outputs.runningCount}         → Health checks
${resources.my-service.outputs.deploymentId}         → Deployment tracking
```

---

## 2. Key Strategy

### Key Format: `region~clusterName~serviceName`

ECS service names are unique within a cluster but not across clusters. The key
includes the cluster name to avoid collisions. The CUE schema maps
`spec.cluster` (cluster name) and `metadata.name` (service name) along with
`spec.region`.

1. **BuildKey** (adapter, plan-time): returns `region~clusterName~metadata.name`.
   The adapter extracts the cluster name from `spec.cluster` — if the user
   provides a cluster ARN, the adapter parses the cluster name from it.
2. **Provision / Delete**: dispatched to the same VO key.
3. **Plan**: reads VO state via `GetOutputs`. If outputs contain a service ARN,
   describes the service. Compares the desired spec with the current state —
   if immutable fields changed, `OpRecreate`; if mutable fields changed, `OpUpdate`.
4. **Import**: `BuildImportKey(region, resourceID)` returns
   `region~clusterName~serviceName` where `resourceID` is `clusterName/serviceName`
   or `clusterName~serviceName`.

### Ownership Tags

ECS service names are unique within a cluster. The driver adds
`praxis:managed-key=<region~clusterName~serviceName>` as a service tag for
consistency with the established pattern. This enables cross-Praxis-installation
conflict detection when multiple Praxis installations target the same account.

**FindByManagedKey** is NOT needed because service names are enforced unique within
a cluster by AWS.

### Import Semantics

Import and template-based management produce the **same Virtual Object key**:

- `praxis import --kind ECSService --region us-east-1 --resource-id myapp/web-svc`:
  Creates VO key `us-east-1~myapp~web-svc`.
- Template with `metadata.name: web-svc` and `spec.cluster: myapp` in `us-east-1`:
  Creates VO key `us-east-1~myapp~web-svc`.

Both target the same Virtual Object.

---

## 3. File Inventory

Create or modify these files (✦ = new file, ✎ = modify existing):

```text
✦ internal/drivers/ecsservice/types.go               — Spec, Outputs, ObservedState, State structs
✦ internal/drivers/ecsservice/aws.go                  — ECSServiceAPI interface + realECSServiceAPI impl
✦ internal/drivers/ecsservice/drift.go                — HasDrift(), ComputeFieldDiffs(), NeedsRecreate()
✦ internal/drivers/ecsservice/driver.go               — ECSServiceDriver Virtual Object
✦ internal/drivers/ecsservice/driver_test.go          — Unit tests for driver (mocked AWS)
✦ internal/drivers/ecsservice/aws_test.go             — Unit tests for error classification
✦ internal/drivers/ecsservice/drift_test.go           — Unit tests for drift detection
✦ internal/core/provider/ecsservice_adapter.go        — ECSServiceAdapter implementing provider.Adapter
✦ internal/core/provider/ecsservice_adapter_test.go   — Unit tests for adapter
✦ schemas/aws/ecs/service.cue                         — CUE schema for ECSService resource
✦ tests/integration/ecsservice_driver_test.go         — Integration tests (Testcontainers + LocalStack)
✎ cmd/praxis-compute/main.go                          — Bind ECSService driver
✎ internal/core/provider/registry.go                  — Add adapter to NewRegistry()
✎ justfile                                            — Add ECS service test targets
```

---

## Step 1 — CUE Schema

**File**: `schemas/aws/ecs/service.cue`

```cue
package ecs

import "list"

#ECSService: {
    apiVersion: "praxis.io/v1"
    kind:       "ECSService"

    metadata: {
        // name is the ECS service name within its cluster.
        // Must be 1-255 characters: letters, numbers, hyphens, and underscores.
        name: string & =~"^[a-zA-Z][a-zA-Z0-9_-]{0,254}$"
        labels: [string]: string
    }

    spec: {
        // region is the AWS region where the service runs.
        region: string

        // cluster is the ECS cluster name or ARN to run the service in.
        cluster: string

        // taskDefinition is the task definition family, family:revision, or full ARN.
        taskDefinition: string

        // desiredCount is the number of task instances to maintain.
        desiredCount: int & >=0

        // launchType determines how tasks are hosted.
        // Immutable after creation.
        launchType?: "FARGATE" | "EC2" | "EXTERNAL"

        // capacityProviderStrategy defines the capacity provider allocation.
        // Mutually exclusive with launchType.
        capacityProviderStrategy?: [...#CapacityProviderStrategyItem]

        // platformVersion is the Fargate platform version (e.g., "LATEST", "1.4.0").
        platformVersion?: string | *"LATEST"

        // schedulingStrategy controls replica vs daemon scheduling.
        // Immutable after creation.
        schedulingStrategy?: "REPLICA" | "DAEMON" | *"REPLICA"

        // deploymentConfiguration controls rolling update behavior.
        deploymentConfiguration?: #DeploymentConfiguration

        // networkConfiguration is required for awsvpc network mode.
        networkConfiguration?: #NetworkConfiguration

        // loadBalancers attaches the service to target groups.
        // Immutable after creation.
        loadBalancers?: [...#LoadBalancer]

        // serviceRegistries for Cloud Map service discovery integration.
        // Immutable after creation.
        serviceRegistries?: [...#ServiceRegistry]

        // healthCheckGracePeriodSeconds is the time to ignore ELB health
        // check failures after a task starts. Only valid with loadBalancers.
        healthCheckGracePeriodSeconds?: int & >=0

        // enableExecuteCommand enables ECS Exec for the service's tasks.
        enableExecuteCommand?: bool

        // placementConstraints limits EC2 task placement.
        placementConstraints?: [...#PlacementConstraint]

        // placementStrategy controls how tasks are spread across instances.
        placementStrategy?: [...#PlacementStrategy]

        // serviceConnectConfiguration for Service Connect.
        serviceConnectConfiguration?: #ServiceConnectConfiguration

        // propagateTags controls whether to propagate tags to tasks.
        propagateTags?: "TASK_DEFINITION" | "SERVICE" | "NONE"

        // enableECSManagedTags enables ECS-managed tags on tasks.
        enableECSManagedTags?: bool

        // forceNewDeployment forces a new deployment even if the task
        // definition and other settings haven't changed.
        forceNewDeployment?: bool

        // tags on the service resource.
        tags: [string]: string
    }

    outputs?: {
        serviceArn:   string
        serviceName:  string
        clusterArn:   string
        status:       string
        desiredCount: int
        runningCount: int
        deploymentId: string
    }
}

#DeploymentConfiguration: {
    // maximumPercent is the upper limit (% of desiredCount) of tasks during deployment.
    maximumPercent?: int & >=100 & <=200 | *200

    // minimumHealthyPercent is the lower bound (% of desiredCount) of tasks
    // that must remain running during deployment.
    minimumHealthyPercent?: int & >=0 & <=100 | *100

    // deploymentCircuitBreaker enables the ECS deployment circuit breaker.
    deploymentCircuitBreaker?: {
        enable:   bool
        rollback: bool
    }

    // alarms configures CloudWatch-alarm-based deployment failure detection.
    alarms?: {
        alarmNames: [...string] & list.MinItems(1)
        enable:   bool
        rollback: bool
    }
}

#NetworkConfiguration: {
    awsvpcConfiguration: {
        // subnets is the list of subnet IDs for task ENIs.
        subnets: [...string] & list.MinItems(1)

        // securityGroups is the list of security group IDs.
        securityGroups?: [...string]

        // assignPublicIp controls whether tasks get public IPs.
        assignPublicIp?: "ENABLED" | "DISABLED" | *"DISABLED"
    }
}

#LoadBalancer: {
    // targetGroupArn is the ALB/NLB target group ARN.
    targetGroupArn: string

    // containerName is the name of the container to register with the target group.
    containerName: string

    // containerPort is the port on the container to register.
    containerPort: int
}

#ServiceRegistry: {
    // registryArn is the Cloud Map service registry ARN.
    registryArn: string

    // port for SRV records (optional).
    port?: int

    // containerName to associate with the service registry.
    containerName?: string

    // containerPort to associate with the service registry.
    containerPort?: int
}

#PlacementConstraint: {
    type:        "distinctInstance" | "memberOf"
    expression?: string
}

#PlacementStrategy: {
    type:  "random" | "spread" | "binpack"
    field?: string
}

#ServiceConnectConfiguration: {
    enabled:    bool
    namespace?: string
    services?: [...{
        portName:      string
        discoveryName?: string
        clientAliases?: [...{
            port:    int
            dnsName?: string
        }]
        ingressPortOverride?: int
    }]
    logConfiguration?: {
        logDriver: string
        options?: [string]: string
        secretOptions?: [...{
            name:      string
            valueFrom: string
        }]
    }
}

// Shared with cluster schema — imported from the same ecs package.
// #CapacityProviderStrategyItem is defined in cluster.cue.
```

### Schema Design Notes

- **`loadBalancers` is immutable**: The schema allows specifying load balancers at
  creation. If the user changes them, the adapter detects an immutable field change
  and signals `OpRecreate`.
- **`launchType` vs `capacityProviderStrategy`**: These are mutually exclusive in
  AWS. The schema allows both but the driver validates mutual exclusivity at
  provision time.
- **`schedulingStrategy`**: `DAEMON` services ignore `desiredCount` (one task per
  container instance). The driver handles this distinction in scaling logic.
- **`forceNewDeployment`**: This is a one-shot flag, not persisted state. When set
  to `true`, the driver forces a new deployment on the next `Provision` call and
  then resets it. Useful for cycling tasks to pick up updated secrets or images
  with the `latest` tag.

---

## Step 2 — AWS Client Factory

**File**: `internal/infra/awsclient/client.go` — shared with ECS Cluster driver.

The `NewECSClient()` factory is added as part of the ECS Cluster driver (Phase 1).
No additional client factory is needed — all ECS drivers share the same `ecs.Client`.

---

## Step 3 — Driver Types

**File**: `internal/drivers/ecsservice/types.go`

```go
package ecsservice

import "github.com/praxiscloud/praxis/pkg/types"

// ServiceName is the Restate Virtual Object name for ECS Services.
const ServiceName = "ECSService"

// ECSServiceSpec is the desired state for an ECS service.
type ECSServiceSpec struct {
    Account                        string                          `json:"account,omitempty"`
    Region                         string                          `json:"region"`
    Cluster                        string                          `json:"cluster"`
    ServiceName                    string                          `json:"serviceName"`
    TaskDefinition                 string                          `json:"taskDefinition"`
    DesiredCount                   int32                           `json:"desiredCount"`
    LaunchType                     string                          `json:"launchType,omitempty"`
    CapacityProviderStrategy       []CapacityProviderStrategyItem  `json:"capacityProviderStrategy,omitempty"`
    PlatformVersion                string                          `json:"platformVersion,omitempty"`
    SchedulingStrategy             string                          `json:"schedulingStrategy,omitempty"`
    DeploymentConfiguration        *DeploymentConfiguration        `json:"deploymentConfiguration,omitempty"`
    NetworkConfiguration           *NetworkConfiguration           `json:"networkConfiguration,omitempty"`
    LoadBalancers                  []LoadBalancer                  `json:"loadBalancers,omitempty"`
    ServiceRegistries              []ServiceRegistry               `json:"serviceRegistries,omitempty"`
    HealthCheckGracePeriodSeconds  *int32                          `json:"healthCheckGracePeriodSeconds,omitempty"`
    EnableExecuteCommand           *bool                           `json:"enableExecuteCommand,omitempty"`
    PlacementConstraints           []PlacementConstraint           `json:"placementConstraints,omitempty"`
    PlacementStrategy              []PlacementStrategy             `json:"placementStrategy,omitempty"`
    ServiceConnectConfiguration    *ServiceConnectConfiguration    `json:"serviceConnectConfiguration,omitempty"`
    PropagateTags                  string                          `json:"propagateTags,omitempty"`
    EnableECSManagedTags           *bool                           `json:"enableECSManagedTags,omitempty"`
    ForceNewDeployment             bool                            `json:"forceNewDeployment,omitempty"`
    Tags                           map[string]string               `json:"tags,omitempty"`
    ManagedKey                     string                          `json:"managedKey,omitempty"`
}

// DeploymentConfiguration controls rolling deployment behavior.
type DeploymentConfiguration struct {
    MaximumPercent            int32                      `json:"maximumPercent,omitempty"`
    MinimumHealthyPercent     int32                      `json:"minimumHealthyPercent,omitempty"`
    DeploymentCircuitBreaker  *DeploymentCircuitBreaker  `json:"deploymentCircuitBreaker,omitempty"`
    Alarms                    *DeploymentAlarms          `json:"alarms,omitempty"`
}

// DeploymentCircuitBreaker controls automatic rollback on deployment failure.
type DeploymentCircuitBreaker struct {
    Enable   bool `json:"enable"`
    Rollback bool `json:"rollback"`
}

// DeploymentAlarms configures CloudWatch alarm-based failure detection.
type DeploymentAlarms struct {
    AlarmNames []string `json:"alarmNames"`
    Enable     bool     `json:"enable"`
    Rollback   bool     `json:"rollback"`
}

// NetworkConfiguration for awsvpc network mode.
type NetworkConfiguration struct {
    AwsvpcConfiguration AwsvpcConfiguration `json:"awsvpcConfiguration"`
}

// AwsvpcConfiguration defines subnets, security groups, and public IP assignment.
type AwsvpcConfiguration struct {
    Subnets        []string `json:"subnets"`
    SecurityGroups []string `json:"securityGroups,omitempty"`
    AssignPublicIp string   `json:"assignPublicIp,omitempty"`
}

// LoadBalancer defines a target group attachment.
type LoadBalancer struct {
    TargetGroupArn string `json:"targetGroupArn"`
    ContainerName  string `json:"containerName"`
    ContainerPort  int32  `json:"containerPort"`
}

// ServiceRegistry defines a Cloud Map service discovery registration.
type ServiceRegistry struct {
    RegistryArn   string `json:"registryArn"`
    Port          int32  `json:"port,omitempty"`
    ContainerName string `json:"containerName,omitempty"`
    ContainerPort int32  `json:"containerPort,omitempty"`
}

// CapacityProviderStrategyItem defines a capacity provider strategy entry.
type CapacityProviderStrategyItem struct {
    CapacityProvider string `json:"capacityProvider"`
    Weight           int32  `json:"weight,omitempty"`
    Base             int32  `json:"base,omitempty"`
}

// PlacementConstraint limits EC2 task placement.
type PlacementConstraint struct {
    Type       string `json:"type"`
    Expression string `json:"expression,omitempty"`
}

// PlacementStrategy controls EC2 task spread/binpack.
type PlacementStrategy struct {
    Type  string `json:"type"`
    Field string `json:"field,omitempty"`
}

// ServiceConnectConfiguration for ECS Service Connect.
type ServiceConnectConfiguration struct {
    Enabled          bool                        `json:"enabled"`
    Namespace        string                      `json:"namespace,omitempty"`
    Services         []ServiceConnectService     `json:"services,omitempty"`
    LogConfiguration *ServiceConnectLogConfig    `json:"logConfiguration,omitempty"`
}

// ServiceConnectService defines a Service Connect service port mapping.
type ServiceConnectService struct {
    PortName            string                    `json:"portName"`
    DiscoveryName       string                    `json:"discoveryName,omitempty"`
    ClientAliases       []ServiceConnectAlias     `json:"clientAliases,omitempty"`
    IngressPortOverride *int32                    `json:"ingressPortOverride,omitempty"`
}

// ServiceConnectAlias defines a DNS alias for a Service Connect service.
type ServiceConnectAlias struct {
    Port    int32  `json:"port"`
    DnsName string `json:"dnsName,omitempty"`
}

// ServiceConnectLogConfig defines log routing for Service Connect proxy.
type ServiceConnectLogConfig struct {
    LogDriver     string            `json:"logDriver"`
    Options       map[string]string `json:"options,omitempty"`
    SecretOptions []Secret          `json:"secretOptions,omitempty"`
}

// Secret defines a secret injected from SSM or Secrets Manager.
type Secret struct {
    Name      string `json:"name"`
    ValueFrom string `json:"valueFrom"`
}

// Deployment tracks an in-flight or completed ECS deployment.
type Deployment struct {
    Id                 string `json:"id"`
    Status             string `json:"status"`
    TaskDefinition     string `json:"taskDefinition"`
    DesiredCount       int32  `json:"desiredCount"`
    RunningCount       int32  `json:"runningCount"`
    PendingCount       int32  `json:"pendingCount"`
    RolloutState       string `json:"rolloutState"`
    FailedTasks        int32  `json:"failedTasks"`
}

// ECSServiceOutputs is produced after provisioning and stored in Restate K/V.
type ECSServiceOutputs struct {
    ServiceArn   string `json:"serviceArn"`
    ServiceName  string `json:"serviceName"`
    ClusterArn   string `json:"clusterArn"`
    Status       string `json:"status"`
    DesiredCount int32  `json:"desiredCount"`
    RunningCount int32  `json:"runningCount"`
    DeploymentId string `json:"deploymentId"`
}

// ObservedState captures the actual configuration of a service from AWS.
type ObservedState struct {
    ServiceArn                      string                          `json:"serviceArn"`
    ServiceName                     string                          `json:"serviceName"`
    ClusterArn                      string                          `json:"clusterArn"`
    Status                          string                          `json:"status"`
    TaskDefinition                  string                          `json:"taskDefinition"`
    DesiredCount                    int32                           `json:"desiredCount"`
    RunningCount                    int32                           `json:"runningCount"`
    PendingCount                    int32                           `json:"pendingCount"`
    LaunchType                      string                          `json:"launchType,omitempty"`
    CapacityProviderStrategy        []CapacityProviderStrategyItem  `json:"capacityProviderStrategy,omitempty"`
    PlatformVersion                 string                          `json:"platformVersion,omitempty"`
    SchedulingStrategy              string                          `json:"schedulingStrategy,omitempty"`
    DeploymentConfiguration         *DeploymentConfiguration        `json:"deploymentConfiguration,omitempty"`
    NetworkConfiguration            *NetworkConfiguration           `json:"networkConfiguration,omitempty"`
    LoadBalancers                   []LoadBalancer                  `json:"loadBalancers,omitempty"`
    ServiceRegistries               []ServiceRegistry               `json:"serviceRegistries,omitempty"`
    HealthCheckGracePeriodSeconds   *int32                          `json:"healthCheckGracePeriodSeconds,omitempty"`
    EnableExecuteCommand            bool                            `json:"enableExecuteCommand"`
    PlacementConstraints            []PlacementConstraint           `json:"placementConstraints,omitempty"`
    PlacementStrategy               []PlacementStrategy             `json:"placementStrategy,omitempty"`
    ServiceConnectConfiguration     *ServiceConnectConfiguration    `json:"serviceConnectConfiguration,omitempty"`
    PropagateTags                   string                          `json:"propagateTags,omitempty"`
    EnableECSManagedTags            bool                            `json:"enableECSManagedTags"`
    Tags                            map[string]string               `json:"tags,omitempty"`
    Deployments                     []Deployment                    `json:"deployments,omitempty"`
    CreatedAt                       string                          `json:"createdAt,omitempty"`
}

// ECSServiceState is the single atomic state object stored under drivers.StateKey.
type ECSServiceState struct {
    Desired            ECSServiceSpec        `json:"desired"`
    Observed           ObservedState         `json:"observed"`
    Outputs            ECSServiceOutputs     `json:"outputs"`
    Status             types.ResourceStatus  `json:"status"`
    Mode               types.Mode            `json:"mode"`
    Error              string                `json:"error,omitempty"`
    Generation         int64                 `json:"generation"`
    LastReconcile      string                `json:"lastReconcile,omitempty"`
    ReconcileScheduled bool                  `json:"reconcileScheduled"`
}
```

### Types Design Notes

- **`ForceNewDeployment` is spec-level, not state-level**: It's a one-shot flag.
  The driver reads it during `Provision`, passes it to `UpdateService`, and does
  NOT persist it in state. The next reconcile or provision call sees it as `false`
  unless the user sets it again.
- **`HealthCheckGracePeriodSeconds` and `EnableExecuteCommand` are pointers**: Go's
  zero values (`0` and `false`) are valid desired states. Pointers distinguish
  "not set" from "explicitly zero/false".
- **`ObservedState` includes runtime fields**: `runningCount`, `pendingCount`, and
  `deployments` are informational. They are not drift-detected but are used for
  status reporting and steady-state checks.
- **`Deployment` structs**: Track in-flight and historical deployments. The primary
  deployment is always `deployments[0]`. The driver uses `rolloutState` to
  determine if a deployment has completed (`COMPLETED`), is in progress
  (`IN_PROGRESS`), or has failed (`FAILED`).

---

## Step 4 — AWS API Abstraction Layer

**File**: `internal/drivers/ecsservice/aws.go`

### ECSServiceAPI Interface

```go
type ECSServiceAPI interface {
    // CreateService creates a new ECS service.
    CreateService(ctx context.Context, spec ECSServiceSpec) (ECSServiceOutputs, error)

    // DescribeService returns the current state of a service.
    DescribeService(ctx context.Context, cluster, serviceName string) (ObservedState, error)

    // UpdateService updates a service's mutable attributes.
    UpdateService(ctx context.Context, cluster, serviceName string, spec ECSServiceSpec) (ECSServiceOutputs, error)

    // DeleteService deletes a service. The service must have desiredCount=0.
    DeleteService(ctx context.Context, cluster, serviceName string, force bool) error

    // UpdateDesiredCount sets the desired task count (scale to 0 for deletion).
    UpdateDesiredCount(ctx context.Context, cluster, serviceName string, count int32) error

    // TagService replaces all tags on the service.
    TagService(ctx context.Context, serviceArn string, tags map[string]string) error

    // UntagService removes specific tag keys from the service.
    UntagService(ctx context.Context, serviceArn string, keys []string) error
}
```

### Implementation: realECSServiceAPI

```go
type realECSServiceAPI struct {
    client  *ecs.Client
    limiter ratelimit.Limiter
}

func newRealECSServiceAPI(client *ecs.Client, limiter ratelimit.Limiter) ECSServiceAPI {
    return &realECSServiceAPI{client: client, limiter: limiter}
}
```

### Error Classification

```go
func isNotFound(err error) bool {
    var snf *ecstypes.ServiceNotFoundException
    return errors.As(err, &snf)
}

func isClusterNotFound(err error) bool {
    var cnf *ecstypes.ClusterNotFoundException
    return errors.As(err, &cnf)
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

func isAccessDenied(err error) bool {
    var ad *ecstypes.AccessDeniedException
    return errors.As(err, &ad)
}
```

**Important**: `ServiceNotFoundException` is only returned by `DescribeServices`
when the service does not exist AND the `include` parameter is not used.
`DescribeServices` with a non-existent service name returns an empty result in the
`services` field and populates the `failures` field instead. The driver must check
both paths.

### Key Implementation Details

#### CreateService

```go
func (r *realECSServiceAPI) CreateService(ctx context.Context, spec ECSServiceSpec) (ECSServiceOutputs, error) {
    r.limiter.Wait(ctx)

    input := &ecs.CreateServiceInput{
        ServiceName:    &spec.ServiceName,
        Cluster:        &spec.Cluster,
        TaskDefinition: &spec.TaskDefinition,
        DesiredCount:   &spec.DesiredCount,
        Tags:           toECSTags(spec.Tags),
    }

    if spec.LaunchType != "" {
        input.LaunchType = ecstypes.LaunchType(spec.LaunchType)
    }
    if len(spec.CapacityProviderStrategy) > 0 {
        input.CapacityProviderStrategy = toECSCapProviderStrategy(spec.CapacityProviderStrategy)
    }
    if spec.PlatformVersion != "" {
        input.PlatformVersion = &spec.PlatformVersion
    }
    if spec.SchedulingStrategy != "" {
        input.SchedulingStrategy = ecstypes.SchedulingStrategy(spec.SchedulingStrategy)
    }
    if spec.DeploymentConfiguration != nil {
        input.DeploymentConfiguration = toECSDeploymentConfig(spec.DeploymentConfiguration)
    }
    if spec.NetworkConfiguration != nil {
        input.NetworkConfiguration = toECSNetworkConfig(spec.NetworkConfiguration)
    }
    if len(spec.LoadBalancers) > 0 {
        input.LoadBalancers = toECSLoadBalancers(spec.LoadBalancers)
    }
    if len(spec.ServiceRegistries) > 0 {
        input.ServiceRegistries = toECSServiceRegistries(spec.ServiceRegistries)
    }
    if spec.HealthCheckGracePeriodSeconds != nil {
        input.HealthCheckGracePeriodSeconds = spec.HealthCheckGracePeriodSeconds
    }
    if spec.EnableExecuteCommand != nil {
        input.EnableExecuteCommand = *spec.EnableExecuteCommand
    }
    if len(spec.PlacementConstraints) > 0 {
        input.PlacementConstraints = toECSPlacementConstraints(spec.PlacementConstraints)
    }
    if len(spec.PlacementStrategy) > 0 {
        input.PlacementStrategy = toECSPlacementStrategies(spec.PlacementStrategy)
    }
    if spec.ServiceConnectConfiguration != nil {
        input.ServiceConnectConfiguration = toECSServiceConnectConfig(spec.ServiceConnectConfiguration)
    }
    if spec.PropagateTags != "" {
        input.PropagateTags = ecstypes.PropagateTags(spec.PropagateTags)
    }
    if spec.EnableECSManagedTags != nil {
        input.EnableECSManagedTags = *spec.EnableECSManagedTags
    }

    out, err := r.client.CreateService(ctx, input)
    if err != nil {
        return ECSServiceOutputs{}, err
    }

    svc := out.Service
    return outputsFromService(svc), nil
}
```

#### DescribeService

```go
func (r *realECSServiceAPI) DescribeService(ctx context.Context, cluster, serviceName string) (ObservedState, error) {
    r.limiter.Wait(ctx)

    out, err := r.client.DescribeServices(ctx, &ecs.DescribeServicesInput{
        Cluster:  &cluster,
        Services: []string{serviceName},
        Include:  []ecstypes.ServiceField{ecstypes.ServiceFieldTags},
    })
    if err != nil {
        return ObservedState{}, err
    }

    // Check for failures (service not found returns here, not as an error)
    if len(out.Failures) > 0 {
        reason := aws.ToString(out.Failures[0].Reason)
        if reason == "MISSING" {
            return ObservedState{}, &ecstypes.ServiceNotFoundException{
                Message: aws.String("service not found"),
            }
        }
        return ObservedState{}, fmt.Errorf("DescribeServices failure: %s", reason)
    }

    if len(out.Services) == 0 {
        return ObservedState{}, &ecstypes.ServiceNotFoundException{
            Message: aws.String("service not found"),
        }
    }

    svc := out.Services[0]

    // DRAINING or INACTIVE services are effectively deleted.
    if string(svc.Status) == "DRAINING" || string(svc.Status) == "INACTIVE" {
        return ObservedState{}, &ecstypes.ServiceNotFoundException{
            Message: aws.String(fmt.Sprintf("service is %s", svc.Status)),
        }
    }

    return observedFromService(&svc), nil
}
```

**Important**: `DescribeServices` behaves differently from most AWS Describe calls:
1. Non-existent services return in the `failures` array, not as an error.
2. Deleted services may still appear with `DRAINING` or `INACTIVE` status.
3. The `include` parameter must specify `TAGS` to get tag data.
The driver handles all three cases.

#### UpdateService

```go
func (r *realECSServiceAPI) UpdateService(ctx context.Context, cluster, serviceName string, spec ECSServiceSpec) (ECSServiceOutputs, error) {
    r.limiter.Wait(ctx)

    input := &ecs.UpdateServiceInput{
        Service:            &serviceName,
        Cluster:            &cluster,
        TaskDefinition:     &spec.TaskDefinition,
        DesiredCount:       &spec.DesiredCount,
        ForceNewDeployment: spec.ForceNewDeployment,
    }

    if spec.DeploymentConfiguration != nil {
        input.DeploymentConfiguration = toECSDeploymentConfig(spec.DeploymentConfiguration)
    }
    if spec.NetworkConfiguration != nil {
        input.NetworkConfiguration = toECSNetworkConfig(spec.NetworkConfiguration)
    }
    if len(spec.CapacityProviderStrategy) > 0 {
        input.CapacityProviderStrategy = toECSCapProviderStrategy(spec.CapacityProviderStrategy)
    }
    if spec.PlatformVersion != "" {
        input.PlatformVersion = &spec.PlatformVersion
    }
    if spec.HealthCheckGracePeriodSeconds != nil {
        input.HealthCheckGracePeriodSeconds = spec.HealthCheckGracePeriodSeconds
    }
    if spec.EnableExecuteCommand != nil {
        input.EnableExecuteCommand = spec.EnableExecuteCommand
    }
    if len(spec.PlacementConstraints) > 0 {
        input.PlacementConstraints = toECSPlacementConstraints(spec.PlacementConstraints)
    }
    if len(spec.PlacementStrategy) > 0 {
        input.PlacementStrategy = toECSPlacementStrategies(spec.PlacementStrategy)
    }
    if spec.ServiceConnectConfiguration != nil {
        input.ServiceConnectConfiguration = toECSServiceConnectConfig(spec.ServiceConnectConfiguration)
    }
    if spec.PropagateTags != "" {
        input.PropagateTags = ecstypes.PropagateTags(spec.PropagateTags)
    }
    if spec.EnableECSManagedTags != nil {
        input.EnableECSManagedTags = spec.EnableECSManagedTags
    }

    out, err := r.client.UpdateService(ctx, input)
    if err != nil {
        return ECSServiceOutputs{}, err
    }

    return outputsFromService(out.Service), nil
}
```

**Note**: `UpdateService` does NOT support changing `loadBalancers`,
`serviceRegistries`, `launchType`, `schedulingStrategy`, or `cluster`. Attempting
to set these fields results in an `InvalidParameterException`. The driver detects
these changes in the plan phase and signals `OpRecreate`.

#### DeleteService

```go
func (r *realECSServiceAPI) DeleteService(ctx context.Context, cluster, serviceName string, force bool) error {
    r.limiter.Wait(ctx)

    input := &ecs.DeleteServiceInput{
        Service: &serviceName,
        Cluster: &cluster,
    }
    if force {
        input.Force = &force
    }

    _, err := r.client.DeleteService(ctx, input)
    return err
}
```

**Force delete**: When `force=true`, ECS deletes the service even if `desiredCount`
is not zero, and stops all running tasks. The driver uses a two-phase approach:
scale to 0, wait briefly, then delete. Force is only used as a fallback.

#### UpdateDesiredCount

```go
func (r *realECSServiceAPI) UpdateDesiredCount(ctx context.Context, cluster, serviceName string, count int32) error {
    r.limiter.Wait(ctx)

    _, err := r.client.UpdateService(ctx, &ecs.UpdateServiceInput{
        Service:      &serviceName,
        Cluster:      &cluster,
        DesiredCount: &count,
    })
    return err
}
```

### Helper Functions

```go
func outputsFromService(svc *ecstypes.Service) ECSServiceOutputs {
    out := ECSServiceOutputs{
        ServiceArn:   aws.ToString(svc.ServiceArn),
        ServiceName:  aws.ToString(svc.ServiceName),
        ClusterArn:   aws.ToString(svc.ClusterArn),
        Status:       string(svc.Status),
        DesiredCount: svc.DesiredCount,
        RunningCount: svc.RunningCount,
    }
    if len(svc.Deployments) > 0 {
        out.DeploymentId = aws.ToString(svc.Deployments[0].Id)
    }
    return out
}

func observedFromService(svc *ecstypes.Service) ObservedState {
    observed := ObservedState{
        ServiceArn:                    aws.ToString(svc.ServiceArn),
        ServiceName:                   aws.ToString(svc.ServiceName),
        ClusterArn:                    aws.ToString(svc.ClusterArn),
        Status:                        string(svc.Status),
        TaskDefinition:                aws.ToString(svc.TaskDefinition),
        DesiredCount:                  svc.DesiredCount,
        RunningCount:                  svc.RunningCount,
        PendingCount:                  svc.PendingCount,
        LaunchType:                    string(svc.LaunchType),
        PlatformVersion:               aws.ToString(svc.PlatformVersion),
        SchedulingStrategy:            string(svc.SchedulingStrategy),
        EnableExecuteCommand:          svc.EnableExecuteCommand,
        EnableECSManagedTags:          svc.EnableECSManagedTags,
        PropagateTags:                 string(svc.PropagateTags),
        Tags:                          fromECSTags(svc.Tags),
        Deployments:                   fromECSDeployments(svc.Deployments),
    }

    if svc.DeploymentConfiguration != nil {
        observed.DeploymentConfiguration = fromECSDeploymentConfig(svc.DeploymentConfiguration)
    }
    if svc.NetworkConfiguration != nil {
        observed.NetworkConfiguration = fromECSNetworkConfig(svc.NetworkConfiguration)
    }
    if len(svc.LoadBalancers) > 0 {
        observed.LoadBalancers = fromECSLoadBalancers(svc.LoadBalancers)
    }
    if len(svc.ServiceRegistries) > 0 {
        observed.ServiceRegistries = fromECSServiceRegistries(svc.ServiceRegistries)
    }
    if svc.HealthCheckGracePeriodSeconds != nil {
        observed.HealthCheckGracePeriodSeconds = svc.HealthCheckGracePeriodSeconds
    }
    if len(svc.CapacityProviderStrategy) > 0 {
        observed.CapacityProviderStrategy = fromECSCapProviderStrategy(svc.CapacityProviderStrategy)
    }
    if len(svc.PlacementConstraints) > 0 {
        observed.PlacementConstraints = fromECSPlacementConstraints(svc.PlacementConstraints)
    }
    if len(svc.PlacementStrategy) > 0 {
        observed.PlacementStrategy = fromECSPlacementStrategies(svc.PlacementStrategy)
    }
    if svc.ServiceConnectConfiguration != nil {
        observed.ServiceConnectConfiguration = fromECSServiceConnectConfig(svc.ServiceConnectConfiguration)
    }
    if svc.CreatedAt != nil {
        observed.CreatedAt = svc.CreatedAt.Format(time.RFC3339)
    }

    return observed
}
```

---

## Step 5 — Drift Detection

**File**: `internal/drivers/ecsservice/drift.go`

### Drift Detection Strategy

ECS services have both mutable and immutable attributes. The drift detection module
must distinguish between:

1. **Mutable drift**: Fields that can be corrected via `UpdateService` (e.g.,
   desired count, task definition, network config, deployment config).
2. **Immutable drift**: Fields that require recreation (e.g., load balancers,
   launch type, scheduling strategy). These should never drift from external
   changes — they can only differ due to a template spec change.

### NeedsRecreate

```go
// NeedsRecreate returns true if any immutable field has changed, requiring
// the service to be deleted and recreated.
func NeedsRecreate(desired ECSServiceSpec, observed ObservedState) bool {
    if !loadBalancersMatch(desired.LoadBalancers, observed.LoadBalancers) { return true }
    if !serviceRegistriesMatch(desired.ServiceRegistries, observed.ServiceRegistries) { return true }
    if desired.LaunchType != "" && desired.LaunchType != observed.LaunchType { return true }
    if desired.SchedulingStrategy != "" && desired.SchedulingStrategy != observed.SchedulingStrategy { return true }
    return false
}
```

### Drift-Detectable Fields (Mutable)

| Field | Drift Source | Notes |
|---|---|---|
| Task definition | Console, CLI, CI/CD | Service updated to a different task def |
| Desired count | Console, CLI, Auto Scaling | Scaled externally |
| Deployment configuration | Console, CLI | Max/min percent, circuit breaker changed |
| Network configuration | Console, CLI | Subnets or security groups changed |
| Capacity provider strategy | Console, CLI | Weights adjusted |
| Platform version | Console, CLI | Fargate platform version changed |
| Health check grace period | Console, CLI | Grace period adjusted |
| Enable execute command | Console, CLI | ECS Exec toggled |
| Placement constraints | Console, CLI | Placement rules changed |
| Placement strategy | Console, CLI | Spread/binpack strategy changed |
| Service Connect config | Console, CLI | Service Connect toggled or modified |
| Tags | Console, CLI, other tools | Tags added/removed/changed |

### Fields NOT Drift-Detected

- **Status**: Read-only runtime state (`ACTIVE`, `DRAINING`, `INACTIVE`).
- **Running count / Pending count**: Runtime counters, not configured attributes.
- **Deployments**: Transient deployment state managed by ECS.
- **Created at**: Timestamp, not a driftable attribute.
- **Load balancers**: Immutable post-creation — drift is impossible for these fields
  (AWS rejects modifications).
- **Service registries**: Immutable post-creation.
- **Launch type**: Immutable post-creation.
- **Scheduling strategy**: Immutable post-creation.

### HasDrift

```go
func HasDrift(desired ECSServiceSpec, observed ObservedState) bool {
    if desired.TaskDefinition != observed.TaskDefinition { return true }
    if desired.DesiredCount != observed.DesiredCount { return true }
    if !deploymentConfigMatch(desired.DeploymentConfiguration, observed.DeploymentConfiguration) { return true }
    if !networkConfigMatch(desired.NetworkConfiguration, observed.NetworkConfiguration) { return true }
    if !capProviderStrategyMatch(desired.CapacityProviderStrategy, observed.CapacityProviderStrategy) { return true }
    if desired.PlatformVersion != "" && desired.PlatformVersion != observed.PlatformVersion { return true }
    if desired.HealthCheckGracePeriodSeconds != nil && observed.HealthCheckGracePeriodSeconds != nil &&
        *desired.HealthCheckGracePeriodSeconds != *observed.HealthCheckGracePeriodSeconds { return true }
    if desired.EnableExecuteCommand != nil && *desired.EnableExecuteCommand != observed.EnableExecuteCommand { return true }
    if !placementConstraintsMatch(desired.PlacementConstraints, observed.PlacementConstraints) { return true }
    if !placementStrategiesMatch(desired.PlacementStrategy, observed.PlacementStrategy) { return true }
    if !serviceConnectMatch(desired.ServiceConnectConfiguration, observed.ServiceConnectConfiguration) { return true }
    if !tagsMatch(desired.Tags, observed.Tags) { return true }
    return false
}
```

### ComputeFieldDiffs

```go
func ComputeFieldDiffs(desired ECSServiceSpec, observed ObservedState) []types.FieldDiff {
    var diffs []types.FieldDiff

    // Immutable field changes → flagged as "requires recreation"
    if !loadBalancersMatch(desired.LoadBalancers, observed.LoadBalancers) {
        diffs = append(diffs, types.FieldDiff{
            Field:    "loadBalancers",
            Desired:  fmt.Sprintf("%v", desired.LoadBalancers),
            Observed: fmt.Sprintf("%v", observed.LoadBalancers),
            Note:     "immutable — requires recreation",
        })
    }

    // Mutable field changes
    if desired.TaskDefinition != observed.TaskDefinition {
        diffs = append(diffs, types.FieldDiff{
            Field:    "taskDefinition",
            Desired:  desired.TaskDefinition,
            Observed: observed.TaskDefinition,
        })
    }
    if desired.DesiredCount != observed.DesiredCount {
        diffs = append(diffs, types.FieldDiff{
            Field:    "desiredCount",
            Desired:  fmt.Sprintf("%d", desired.DesiredCount),
            Observed: fmt.Sprintf("%d", observed.DesiredCount),
        })
    }
    // ... similar for all drift-detectable fields
    return diffs
}
```

### Network Configuration Comparison

Network configuration comparison deserves special attention because AWS may
normalize the data:

```go
func networkConfigMatch(desired *NetworkConfiguration, observed *NetworkConfiguration) bool {
    if desired == nil && observed == nil { return true }
    if desired == nil || observed == nil { return false }

    d := desired.AwsvpcConfiguration
    o := observed.AwsvpcConfiguration

    // Subnets: order-independent comparison
    if !stringSlicesEqualUnordered(d.Subnets, o.Subnets) { return false }
    // Security groups: order-independent comparison
    if !stringSlicesEqualUnordered(d.SecurityGroups, o.SecurityGroups) { return false }
    // AssignPublicIp: normalize empty to "DISABLED"
    dPub := d.AssignPublicIp
    if dPub == "" { dPub = "DISABLED" }
    oPub := o.AssignPublicIp
    if oPub == "" { oPub = "DISABLED" }
    return dPub == oPub
}
```

---

## Step 6 — Driver Implementation

**File**: `internal/drivers/ecsservice/driver.go`

### Constructor

```go
type ECSServiceDriver struct {
    accounts *auth.Registry
}

func NewECSServiceDriver(accounts *auth.Registry) *ECSServiceDriver {
    return &ECSServiceDriver{accounts: accounts}
}

func (ECSServiceDriver) ServiceName() string { return ServiceName }
```

### Provision

```go
func (d *ECSServiceDriver) Provision(ctx restate.ObjectContext, spec ECSServiceSpec) (ECSServiceOutputs, error) {
    // 1. Load existing state
    state, _ := restate.Get[*ECSServiceState](ctx, drivers.StateKey)

    // 2. Build API client
    api := d.buildAPI(spec.Account, spec.Region)

    // 3. If no existing state → CreateService
    if state == nil || state.Outputs.ServiceArn == "" {
        return d.createService(ctx, api, spec)
    }

    // 4. Check for immutable field changes → signal recreation needed
    if NeedsRecreate(spec, state.Observed) {
        return ECSServiceOutputs{}, restate.TerminalError(
            fmt.Errorf("immutable fields changed (loadBalancers, launchType, schedulingStrategy, or serviceRegistries); delete and recreate the service"),
            409,
        )
    }

    // 5. Existing service → update mutable fields
    return d.updateService(ctx, api, spec, state)
}
```

#### Create Flow

```go
func (d *ECSServiceDriver) createService(ctx restate.ObjectContext, api ECSServiceAPI, spec ECSServiceSpec) (ECSServiceOutputs, error) {
    // Write pending state
    restate.Set(ctx, drivers.StateKey, &ECSServiceState{
        Desired: spec,
        Status:  types.StatusProvisioning,
        Mode:    drivers.DefaultMode(""),
    })

    // Create service (journaled via restate.Run)
    outputs, err := restate.Run(ctx, func(rc restate.RunContext) (ECSServiceOutputs, error) {
        return api.CreateService(rc, spec)
    })
    if err != nil {
        // If service already exists with our managed key tag, treat as convergence
        if isInvalidParam(err) {
            return ECSServiceOutputs{}, restate.TerminalError(
                fmt.Errorf("CreateService failed: %w", err), 400,
            )
        }
        return ECSServiceOutputs{}, err
    }

    // Describe to populate full observed state
    observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
        return api.DescribeService(rc, spec.Cluster, spec.ServiceName)
    })
    if err != nil {
        return ECSServiceOutputs{}, err
    }

    // Write final state
    restate.Set(ctx, drivers.StateKey, &ECSServiceState{
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
func (d *ECSServiceDriver) updateService(ctx restate.ObjectContext, api ECSServiceAPI, spec ECSServiceSpec, state *ECSServiceState) (ECSServiceOutputs, error) {
    // Check if any mutable fields actually changed
    mutableDrift := HasDrift(spec, state.Observed) || spec.ForceNewDeployment
    tagDrift := !tagsMatch(spec.Tags, state.Observed.Tags)

    if !mutableDrift && !tagDrift {
        // No changes needed
        state.Desired = spec
        restate.Set(ctx, drivers.StateKey, state)
        return state.Outputs, nil
    }

    state.Desired = spec
    state.Status = types.StatusProvisioning
    state.Generation++
    restate.Set(ctx, drivers.StateKey, state)

    // Phase 1: Update service (mutable fields)
    if mutableDrift {
        outputs, err := restate.Run(ctx, func(rc restate.RunContext) (ECSServiceOutputs, error) {
            return api.UpdateService(rc, spec.Cluster, spec.ServiceName, spec)
        })
        if err != nil {
            return ECSServiceOutputs{}, err
        }
        state.Outputs = outputs
    }

    // Phase 2: Update tags (separate API call)
    if tagDrift {
        if _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
            removedKeys := computeRemovedKeys(state.Observed.Tags, spec.Tags)
            if len(removedKeys) > 0 {
                if err := api.UntagService(rc, state.Outputs.ServiceArn, removedKeys); err != nil {
                    return restate.Void{}, err
                }
            }
            return restate.Void{}, api.TagService(rc, state.Outputs.ServiceArn, spec.Tags)
        }); err != nil {
            return ECSServiceOutputs{}, err
        }
    }

    // Describe final state
    observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
        return api.DescribeService(rc, spec.Cluster, spec.ServiceName)
    })
    if err != nil {
        return ECSServiceOutputs{}, err
    }

    restate.Set(ctx, drivers.StateKey, &ECSServiceState{
        Desired:    spec,
        Observed:   observed,
        Outputs:    outputsFromObserved(observed),
        Status:     types.StatusReady,
        Mode:       types.ModeManaged,
        Generation: state.Generation,
    })

    return outputsFromObserved(observed), nil
}
```

### Delete

ECS services require a two-phase deletion: scale to 0 tasks, then delete the service.

```go
func (d *ECSServiceDriver) Delete(ctx restate.ObjectContext) error {
    state, _ := restate.Get[*ECSServiceState](ctx, drivers.StateKey)
    if state == nil {
        return nil
    }
    if state.Mode == types.ModeObserved {
        restate.Clear(ctx, drivers.StateKey)
        return nil
    }

    api := d.buildAPI(state.Desired.Account, state.Desired.Region)
    cluster := state.Desired.Cluster
    svcName := state.Desired.ServiceName

    // Check if service still exists
    observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
        return api.DescribeService(rc, cluster, svcName)
    })
    if err != nil {
        if isNotFound(err) {
            restate.Clear(ctx, drivers.StateKey)
            return nil
        }
        return err
    }

    // Phase 1: Scale to 0 if not already
    if observed.DesiredCount > 0 {
        if _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
            return restate.Void{}, api.UpdateDesiredCount(rc, cluster, svcName, 0)
        }); err != nil {
            return err
        }

        // Brief pause to allow task drain to begin
        if err := restate.Sleep(ctx, 5*time.Second); err != nil {
            return err
        }
    }

    // Phase 2: Delete the service
    if _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
        return restate.Void{}, api.DeleteService(rc, cluster, svcName, false)
    }); err != nil {
        if isNotFound(err) {
            restate.Clear(ctx, drivers.StateKey)
            return nil
        }
        // If regular delete fails (tasks still draining), try force delete
        if isInvalidParam(err) {
            if _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
                return restate.Void{}, api.DeleteService(rc, cluster, svcName, true)
            }); err != nil {
                if isNotFound(err) {
                    restate.Clear(ctx, drivers.StateKey)
                    return nil
                }
                return err
            }
        } else {
            return err
        }
    }

    restate.Clear(ctx, drivers.StateKey)
    return nil
}
```

### Import

```go
func (d *ECSServiceDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (ECSServiceOutputs, error) {
    api := d.buildAPI(ref.Account, ref.Region)

    // Parse cluster and service name from resourceID.
    // Expected format: "clusterName/serviceName" or "clusterName~serviceName"
    cluster, svcName, err := parseServiceResourceID(ref.ResourceID)
    if err != nil {
        return ECSServiceOutputs{}, restate.TerminalError(
            fmt.Errorf("invalid resource ID %q: expected 'clusterName/serviceName': %w", ref.ResourceID, err),
            400,
        )
    }

    observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
        return api.DescribeService(rc, cluster, svcName)
    })
    if err != nil {
        return ECSServiceOutputs{}, err
    }

    outputs := outputsFromObserved(observed)

    mode := types.ModeObserved
    if ref.Mode != "" {
        mode = ref.Mode
    }

    restate.Set(ctx, drivers.StateKey, &ECSServiceState{
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

The reconcile loop for ECS services is the most complex in the driver family.
It must handle:

1. **Task definition drift**: Someone deployed a different revision via CLI/console.
2. **Desired count drift**: Auto Scaling or manual scaling changed the count.
3. **Network configuration drift**: Security groups or subnets modified.
4. **Deployment state monitoring**: Check if the current deployment has completed,
   failed, or is still rolling.

```go
func (d *ECSServiceDriver) Reconcile(ctx restate.ObjectContext) error {
    state, _ := restate.Get[*ECSServiceState](ctx, drivers.StateKey)
    if state == nil {
        return nil
    }

    api := d.buildAPI(state.Desired.Account, state.Desired.Region)

    // Describe current state
    observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
        return api.DescribeService(rc, state.Desired.Cluster, state.Desired.ServiceName)
    })
    if err != nil {
        if isNotFound(err) {
            state.Status = types.StatusDeleted
            state.Error = "service not found in AWS"
            restate.Set(ctx, drivers.StateKey, state)
            d.scheduleReconcile(ctx)
            return nil
        }
        return err
    }

    state.Observed = observed
    state.LastReconcile = time.Now().UTC().Format(time.RFC3339)

    // Check for drift
    hasDrift := HasDrift(state.Desired, observed)

    if hasDrift && state.Mode == types.ModeManaged {
        // Correct drift: update service to match desired state
        if _, err := restate.Run(ctx, func(rc restate.RunContext) (ECSServiceOutputs, error) {
            return api.UpdateService(rc, state.Desired.Cluster, state.Desired.ServiceName, state.Desired)
        }); err != nil {
            state.Error = fmt.Sprintf("reconcile update failed: %v", err)
            restate.Set(ctx, drivers.StateKey, state)
            d.scheduleReconcile(ctx)
            return nil
        }

        // Tag reconciliation
        if !tagsMatch(state.Desired.Tags, observed.Tags) {
            if _, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
                removedKeys := computeRemovedKeys(observed.Tags, state.Desired.Tags)
                if len(removedKeys) > 0 {
                    if err := api.UntagService(rc, observed.ServiceArn, removedKeys); err != nil {
                        return restate.Void{}, err
                    }
                }
                return restate.Void{}, api.TagService(rc, observed.ServiceArn, state.Desired.Tags)
            }); err != nil {
                state.Error = fmt.Sprintf("reconcile tag update failed: %v", err)
                restate.Set(ctx, drivers.StateKey, state)
                d.scheduleReconcile(ctx)
                return nil
            }
        }

        // Re-describe after correction
        observed, err = restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
            return api.DescribeService(rc, state.Desired.Cluster, state.Desired.ServiceName)
        })
        if err != nil {
            state.Error = fmt.Sprintf("post-reconcile describe failed: %v", err)
            restate.Set(ctx, drivers.StateKey, state)
            d.scheduleReconcile(ctx)
            return nil
        }
        state.Observed = observed
    }

    state.Outputs = outputsFromObserved(observed)
    state.Status = types.StatusReady
    state.Error = ""
    restate.Set(ctx, drivers.StateKey, state)

    // Schedule next reconcile
    d.scheduleReconcile(ctx)
    return nil
}
```

### GetStatus and GetOutputs

```go
func (d *ECSServiceDriver) GetStatus(ctx restate.ObjectSharedContext) (types.ResourceStatus, error) {
    state, _ := restate.Get[*ECSServiceState](ctx, drivers.StateKey)
    if state == nil {
        return types.StatusNotFound, nil
    }
    return state.Status, nil
}

func (d *ECSServiceDriver) GetOutputs(ctx restate.ObjectSharedContext) (*ECSServiceOutputs, error) {
    state, _ := restate.Get[*ECSServiceState](ctx, drivers.StateKey)
    if state == nil {
        return nil, nil
    }
    return &state.Outputs, nil
}
```

### scheduleReconcile

```go
func (d *ECSServiceDriver) scheduleReconcile(ctx restate.ObjectContext) {
    restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").
        Send(nil, restate.WithDelay(drivers.ReconcileInterval))
}
```

### buildAPI

```go
func (d *ECSServiceDriver) buildAPI(account, region string) ECSServiceAPI {
    cfg := d.accounts.GetConfig(account, region)
    client := awsclient.NewECSClient(cfg)
    limiter := ratelimit.NewLimiter("ecs-service", 15, 8)
    return newRealECSServiceAPI(client, limiter)
}
```

---

## Step 7 — Provider Adapter

**File**: `internal/core/provider/ecsservice_adapter.go`

```go
type ECSServiceAdapter struct {
    accounts *auth.Registry
}

func NewECSServiceAdapterWithRegistry(accounts *auth.Registry) *ECSServiceAdapter {
    return &ECSServiceAdapter{accounts: accounts}
}

func (a *ECSServiceAdapter) Kind() string { return "ECSService" }

func (a *ECSServiceAdapter) ServiceName() string { return ecsservice.ServiceName }

func (a *ECSServiceAdapter) Scope() KeyScope { return KeyScopeRegion }

func (a *ECSServiceAdapter) BuildKey(doc json.RawMessage) (string, error) {
    region, _ := jsonpath.String(doc, "spec.region")
    cluster, _ := jsonpath.String(doc, "spec.cluster")
    name, _ := jsonpath.String(doc, "metadata.name")
    if region == "" || cluster == "" || name == "" {
        return "", fmt.Errorf("ECSService requires spec.region, spec.cluster, and metadata.name")
    }
    // If cluster is an ARN, extract the cluster name
    clusterName := extractClusterName(cluster)
    return region + "~" + clusterName + "~" + name, nil
}

func (a *ECSServiceAdapter) BuildImportKey(region, resourceID string) (string, error) {
    // resourceID is "clusterName/serviceName" or "clusterName~serviceName"
    cluster, svcName, err := parseServiceResourceID(resourceID)
    if err != nil {
        return "", fmt.Errorf("invalid ECSService resource ID %q: %w", resourceID, err)
    }
    return region + "~" + cluster + "~" + svcName, nil
}

func (a *ECSServiceAdapter) DecodeSpec(doc json.RawMessage) (any, error) {
    // Extract fields from evaluated CUE doc, build ECSServiceSpec
    // ...
}

func (a *ECSServiceAdapter) Plan(ctx context.Context, key string, doc map[string]any) (types.PlanOp, []types.FieldDiff, error) {
    // Describe service, compare with desired spec.
    // If immutable fields changed → OpRecreate.
    // If mutable fields changed → OpUpdate with diffs.
    // If service doesn't exist → OpCreate.
    // If no changes → OpNoop.
    // ...
}
```

### Helper: extractClusterName

```go
// extractClusterName extracts the cluster name from a cluster ARN or returns
// the input as-is if it's already a name.
func extractClusterName(clusterRef string) string {
    // ARN format: arn:aws:ecs:region:account:cluster/clusterName
    if strings.HasPrefix(clusterRef, "arn:") {
        parts := strings.Split(clusterRef, "/")
        if len(parts) >= 2 {
            return parts[len(parts)-1]
        }
    }
    return clusterRef
}
```

### Helper: parseServiceResourceID

```go
// parseServiceResourceID parses "clusterName/serviceName" or "clusterName~serviceName".
func parseServiceResourceID(resourceID string) (cluster, serviceName string, err error) {
    for _, sep := range []string{"/", "~"} {
        parts := strings.SplitN(resourceID, sep, 2)
        if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
            return parts[0], parts[1], nil
        }
    }
    return "", "", fmt.Errorf("expected 'clusterName/serviceName' or 'clusterName~serviceName'")
}
```

### Plan Logic: Immutable Field Detection

The adapter's `Plan` method is unique among drivers because it detects immutable
field changes and returns `OpRecreate`:

```go
func (a *ECSServiceAdapter) Plan(ctx context.Context, key string, doc map[string]any) (types.PlanOp, []types.FieldDiff, error) {
    spec, err := a.DecodeSpec(doc)
    if err != nil {
        return types.OpNoop, nil, err
    }
    svcSpec := spec.(ECSServiceSpec)

    // Get current outputs via Restate RPC
    outputs, err := getOutputsRPC(ctx, ServiceName, key)
    if err != nil || outputs == nil {
        return types.OpCreate, nil, nil
    }

    // Describe actual service
    api := a.buildAPI(svcSpec.Account, svcSpec.Region)
    observed, err := api.DescribeService(ctx, svcSpec.Cluster, svcSpec.ServiceName)
    if err != nil {
        if isNotFound(err) {
            return types.OpCreate, nil, nil
        }
        return types.OpNoop, nil, err
    }

    // Check immutable fields first
    if NeedsRecreate(svcSpec, observed) {
        diffs := ComputeFieldDiffs(svcSpec, observed)
        return types.OpRecreate, diffs, nil
    }

    // Check mutable fields
    if HasDrift(svcSpec, observed) {
        diffs := ComputeFieldDiffs(svcSpec, observed)
        return types.OpUpdate, diffs, nil
    }

    return types.OpNoop, nil, nil
}
```

---

## Step 8 — Registry Integration

**File**: `internal/core/provider/registry.go` — add to `NewRegistry()`:

```go
// Added to NewRegistryWithAdapters() call:
NewECSServiceAdapterWithRegistry(accounts),
```

---

## Step 9 — Compute Driver Pack Entry Point

**File**: `cmd/praxis-compute/main.go` — add Bind call:

```go
Bind(restate.Reflect(ecsservice.NewECSServiceDriver(cfg.Auth())))
```

---

## Step 10 — Docker Compose & Justfile

### Docker Compose

No changes needed — ECS Service driver is hosted in the existing `praxis-compute`
service on port 9084.

### Justfile

```just
test-ecs-service:
    go test ./internal/drivers/ecsservice/... -v -count=1 -race

test-ecs-service-integration:
    go test ./tests/integration/ -run TestECSService -v -count=1 -tags=integration -timeout=10m
```

---

## Step 11 — Unit Tests

**File**: `internal/drivers/ecsservice/driver_test.go`

### Test Cases

| Test | Description |
|---|---|
| `TestProvision_NewService_Fargate` | Create a Fargate service with ALB, awsvpc networking |
| `TestProvision_NewService_EC2` | Create an EC2 service with bridge networking |
| `TestProvision_UpdateTaskDefinition` | Deploy a new task definition revision |
| `TestProvision_UpdateDesiredCount` | Scale service up/down |
| `TestProvision_UpdateNetworkConfig` | Change subnets or security groups |
| `TestProvision_UpdateDeploymentConfig` | Enable circuit breaker, change max/min percent |
| `TestProvision_ForceNewDeployment` | Force task cycling without spec changes |
| `TestProvision_ImmutableFieldChange_LoadBalancers` | Changed load balancers → terminal 409 |
| `TestProvision_ImmutableFieldChange_LaunchType` | Changed launch type → terminal 409 |
| `TestProvision_NoChanges` | Idempotent provision with no drift |
| `TestDelete_ScaleAndDelete` | Scale to 0, then delete |
| `TestDelete_ForceDelete` | Force delete when tasks won't drain |
| `TestDelete_AlreadyGone` | Service already deleted → no error |
| `TestDelete_ObservedMode` | Delete in observed mode → clear state only |
| `TestImport_FargateService` | Import a Fargate service with load balancers |
| `TestImport_EC2Service` | Import an EC2 service |
| `TestImport_NotFound` | Import non-existent service → error |
| `TestImport_InvalidResourceID` | Malformed resource ID → terminal error |
| `TestReconcile_NoDrift` | Reconcile with no changes |
| `TestReconcile_TaskDefDrift` | External deployment changed task definition |
| `TestReconcile_DesiredCountDrift` | Auto Scaling changed desired count |
| `TestReconcile_NetworkDrift` | Security group modified externally |
| `TestReconcile_DriftObservedMode` | Drift detected, report only (observed mode) |
| `TestReconcile_ServiceDeleted` | Service disappeared from AWS |
| `TestGetStatus` | Returns current status from state |
| `TestGetOutputs` | Returns current outputs from state |
| `TestNeedsRecreate_LoadBalancerChange` | Detects immutable load balancer change |
| `TestNeedsRecreate_NoChange` | Same immutable fields → no recreation |

**File**: `internal/drivers/ecsservice/drift_test.go`

### Drift Test Cases

| Test | Description |
|---|---|
| `TestHasDrift_TaskDefinitionChanged` | Different task definition ARN |
| `TestHasDrift_DesiredCountChanged` | Desired count mismatch |
| `TestHasDrift_NetworkSubnetsChanged` | Different subnets in awsvpc config |
| `TestHasDrift_SecurityGroupsChanged` | Different security groups |
| `TestHasDrift_DeploymentConfigChanged` | Circuit breaker toggled |
| `TestHasDrift_TagsChanged` | Tags added/removed |
| `TestHasDrift_NoDrift` | All fields match |
| `TestNeedsRecreate_LoadBalancersChanged` | Load balancer target group changed |
| `TestNeedsRecreate_LaunchTypeChanged` | FARGATE → EC2 |
| `TestNeedsRecreate_SchedulingStrategyChanged` | REPLICA → DAEMON |
| `TestNeedsRecreate_ServiceRegistriesChanged` | Cloud Map registration changed |
| `TestComputeFieldDiffs_MultipleChanges` | Returns all changed fields |
| `TestNetworkConfigMatch_SubnetOrder` | Order-independent subnet comparison |
| `TestNetworkConfigMatch_AssignPublicIpDefault` | Empty normalized to DISABLED |

**File**: `internal/drivers/ecsservice/aws_test.go`

### AWS Error Classification Tests

| Test | Description |
|---|---|
| `TestIsNotFound_ServiceNotFoundException` | ServiceNotFoundException classified correctly |
| `TestIsNotFound_DescribeFailures` | MISSING failure in DescribeServices response |
| `TestIsClusterNotFound` | ClusterNotFoundException classified correctly |
| `TestIsInvalidParam` | InvalidParameterException classified correctly |
| `TestIsServerError` | ServerException classified correctly |
| `TestIsUpdateInProgress` | UpdateInProgressException classified correctly |
| `TestIsAccessDenied` | AccessDeniedException classified correctly |

---

## Step 12 — Integration Tests

**File**: `tests/integration/ecsservice_driver_test.go`

Integration tests use Testcontainers (LocalStack) to exercise real AWS API calls.

### Test Scenarios

1. **Create Fargate service → Describe → Delete**: Full lifecycle with awsvpc
   networking, Fargate launch type. Create cluster and task definition as
   prerequisites.
2. **Create with load balancer**: Service attached to a target group (requires ELB
   and VPC resources as prerequisites).
3. **Update task definition**: Deploy a new revision and verify the service picks it up.
4. **Scale up/down**: Change desired count and verify.
5. **Update network configuration**: Change security groups.
6. **Force new deployment**: Trigger task cycling without spec changes.
7. **Import → Reconcile**: Import an existing service and verify observation.
8. **Delete with running tasks**: Scale to 0, wait, then delete.

### Test Fixture: ECS Stack

Integration tests create a minimal ECS stack:

```go
func setupECSStack(t *testing.T, client *ecs.Client) ecsStack {
    // 1. Create cluster
    cluster, _ := client.CreateCluster(ctx, &ecs.CreateClusterInput{
        ClusterName: aws.String("test-cluster"),
    })

    // 2. Register task definition
    taskDef, _ := client.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
        Family: aws.String("test-task"),
        ContainerDefinitions: []ecstypes.ContainerDefinition{{
            Name:      aws.String("app"),
            Image:     aws.String("nginx:latest"),
            Essential: aws.Bool(true),
            PortMappings: []ecstypes.PortMapping{{
                ContainerPort: aws.Int32(80),
                Protocol:      ecstypes.TransportProtocolTcp,
            }},
        }},
        NetworkMode:            ecstypes.NetworkModeAwsvpc,
        RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
        Cpu:                    aws.String("256"),
        Memory:                 aws.String("512"),
    })

    return ecsStack{
        clusterArn:        aws.ToString(cluster.Cluster.ClusterArn),
        clusterName:       "test-cluster",
        taskDefinitionArn: aws.ToString(taskDef.TaskDefinition.TaskDefinitionArn),
        family:            "test-task",
    }
}
```

**LocalStack limitation**: ECS in LocalStack has limited fidelity. Services will
be created but tasks will not actually run. Integration tests validate API shape
(correct parameters, error codes, response parsing) and state transitions rather
than full runtime behavior. The `runningCount` and `pendingCount` fields may not
reflect a production-like state in LocalStack.

### Cross-Driver Integration Test

**File**: `tests/integration/ecs_stack_test.go`

A comprehensive test that exercises the full ECS stack:

```
Cluster (create) → Task Definition (register) → Service (create with LB) →
  → Service (update task def) → Service (scale) → Service (delete) →
  → Task Definition (delete) → Cluster (delete)
```

This test validates the dependency chain and the orchestrator's DAG-based ordering.

---

## ECS-Service-Specific Design Decisions

### Load Balancers Are Immutable Post-Creation

ECS does not support modifying `loadBalancers` on an existing service. This is a
hard AWS constraint, not a driver limitation. If the user changes `loadBalancers`
in their template, the adapter's `Plan` method returns `OpRecreate`, and the
orchestrator sequences a delete + create.

The driver does NOT attempt to work around this by deleting and recreating the
service silently — recreation must flow through the orchestrator's DAG to handle
dependent resources correctly (e.g., draining connections via the target group).

### Two-Phase Delete (Scale to 0, Then Delete)

AWS recommends scaling to 0 before deleting a service to allow graceful task
shutdown. The driver:

1. Calls `UpdateService` with `desiredCount=0`.
2. Sleeps briefly (5 seconds) via `restate.Sleep` (durable timer).
3. Calls `DeleteService`.

If the normal delete fails (tasks still draining), the driver retries with
`force=true`. Force delete stops all tasks immediately.

The durable sleep between phases survives process restarts — Restate journals the
timer and resumes after the configured delay.

### DescribeServices Returns Failures, Not Errors

Unlike most AWS Describe APIs that throw exceptions for non-existent resources,
`DescribeServices` returns successfully with a `failures` array containing entries
with `reason: "MISSING"`. The driver must check the `failures` array before
accessing the `services` array.

Additionally, deleted ECS services transition through `DRAINING` → `INACTIVE`
status before disappearing. The driver treats both `DRAINING` and `INACTIVE`
services as "not found" to maintain consistent lifecycle semantics.

### ForceNewDeployment Is a One-Shot Flag

`forceNewDeployment` tells ECS to cycle all tasks even if the task definition
hasn't changed. This is useful when:

- Container images use the `latest` tag and the underlying image has been updated.
- Secrets or environment variables (from SSM/Secrets Manager) have changed.
- The user wants to restart all tasks for debugging.

The driver reads `ForceNewDeployment` from the spec, passes it to `UpdateService`,
and does NOT persist it in the desired state. The next reconcile or provision call
does not force a new deployment unless the user explicitly sets the flag again.

### Task Definition References

The `taskDefinition` field accepts three formats:
- **Family name**: `myapp-web` → ECS uses the latest ACTIVE revision.
- **Family:revision**: `myapp-web:3` → ECS uses the specific revision.
- **Full ARN**: `arn:aws:ecs:us-east-1:123456789012:task-definition/myapp-web:3`

The driver passes the value through to the ECS API as-is. For drift detection,
the driver compares the desired value against the observed `taskDefinition` field,
which AWS always returns as a full ARN. The drift comparator resolves family names
to ARNs before comparing:

```go
func taskDefinitionMatch(desired string, observedArn string) bool {
    // If desired is a full ARN, compare directly
    if strings.HasPrefix(desired, "arn:") {
        return desired == observedArn
    }
    // If desired is family or family:revision, extract from observed ARN
    // arn:aws:ecs:region:account:task-definition/family:revision
    arnParts := strings.Split(observedArn, "/")
    if len(arnParts) < 2 {
        return false
    }
    familyRevision := arnParts[len(arnParts)-1]
    // If desired has no revision, compare family only
    if !strings.Contains(desired, ":") {
        parts := strings.SplitN(familyRevision, ":", 2)
        return parts[0] == desired
    }
    // desired has family:revision, compare directly
    return desired == familyRevision
}
```

### Deployment Monitoring (Future Enhancement)

The current design does not block `Provision` until the deployment reaches steady
state. `UpdateService` returns immediately, and the new deployment rolls out
asynchronously. The driver records the deployment ID in outputs for external
monitoring.

A future enhancement could add a `WaitForSteadyState` option that polls
`DescribeServices` until:
- `deployments[0].rolloutState == "COMPLETED"` (success)
- `deployments[0].rolloutState == "FAILED"` (circuit breaker triggered)
- Timeout is reached

This would use `restate.Sleep` for polling intervals, making the wait durable
across restarts.

### Capacity Provider Strategy vs Launch Type

`launchType` and `capacityProviderStrategy` are mutually exclusive. If the user
specifies both, the driver returns a terminal error at provision time. In practice:

- **Fargate workloads**: Use `capacityProviderStrategy` with `FARGATE` and
  optionally `FARGATE_SPOT` for cost optimization.
- **EC2 workloads**: Use `launchType: "EC2"` for simple cases, or
  `capacityProviderStrategy` with custom capacity providers for advanced scenarios.

The schema allows both but the driver validates mutual exclusivity.

### Service Connect

Service Connect enables service-to-service communication with automatic service
discovery and traffic management. The driver passes `serviceConnectConfiguration`
through to the ECS API. Key considerations:

- The cluster must have Service Connect defaults configured (namespace).
- Service Connect injects a sidecar proxy container into tasks.
- The `services` array maps port names from the task definition to discoverable
  service endpoints.
- Service Connect configuration is mutable via `UpdateService`.

---

## Checklist

- [ ] `schemas/aws/ecs/service.cue`
- [ ] `internal/drivers/ecsservice/types.go`
- [ ] `internal/drivers/ecsservice/aws.go`
- [ ] `internal/drivers/ecsservice/drift.go`
- [ ] `internal/drivers/ecsservice/driver.go`
- [ ] `internal/drivers/ecsservice/driver_test.go`
- [ ] `internal/drivers/ecsservice/aws_test.go`
- [ ] `internal/drivers/ecsservice/drift_test.go`
- [ ] `internal/core/provider/ecsservice_adapter.go`
- [ ] `internal/core/provider/ecsservice_adapter_test.go`
- [ ] `tests/integration/ecsservice_driver_test.go`
- [ ] `tests/integration/ecs_stack_test.go` (cross-driver integration)
- [ ] `cmd/praxis-compute/main.go` — Bind ECSService driver
- [ ] `internal/core/provider/registry.go` — Register adapter
- [ ] `justfile` — Add ECS service test targets
