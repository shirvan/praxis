# Aurora Cluster Driver — Implementation Plan

> **Implementation note:** This plan references a `praxis-database` driver pack.
> The actual implementation places the Aurora Cluster driver in **`praxis-storage`**
> (`cmd/praxis-storage/main.go`).
>
> Target: A Restate Virtual Object driver that manages Amazon Aurora DB Clusters,
> providing full lifecycle management including creation, configuration, import,
> deletion, drift detection, and drift correction for cluster properties, engine
> settings, networking, and tags.
>
> Key scope: `KeyScopeRegion` — key format is `region~clusterIdentifier`, permanent
> and immutable for the lifetime of the Virtual Object. The AWS-assigned
> DbClusterResourceId lives only in state/outputs.

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
12. [Step 9 — Database Driver Pack Entry Point](#step-9--database-driver-pack-entry-point)
13. [Step 10 — Docker Compose & Justfile](#step-10--docker-compose--justfile)
14. [Step 11 — Unit Tests](#step-11--unit-tests)
15. [Step 12 — Integration Tests](#step-12--integration-tests)
16. [Aurora-Cluster-Specific Design Decisions](#aurora-cluster-specific-design-decisions)
17. [Design Decisions (Resolved)](#design-decisions-resolved)
18. [Checklist](#checklist)

---

## 1. Overview & Scope

The Aurora Cluster driver manages the lifecycle of Aurora **DB clusters** only.
Aurora cluster instances are managed by the RDS DB Instance driver with
`dbClusterIdentifier` set. Global clusters, cross-region replicas, Serverless v2
configurations, and cluster snapshots are out of scope for this driver.

Aurora clusters are the control plane for Aurora databases. They manage storage
replication, endpoint routing, and failover. Individual compute instances
(readers/writers) are added via the RDS DB Instance driver referencing the cluster.

The driver follows the established Virtual Object contract:

| Handler | Context | Purpose |
|---|---|---|
| `Provision` | `ObjectContext` (exclusive) | Create or converge an Aurora cluster |
| `Import` | `ObjectContext` (exclusive) | Adopt an existing Aurora cluster |
| `Delete` | `ObjectContext` (exclusive) | Delete an Aurora cluster (after instances removed) |
| `Reconcile` | `ObjectContext` (exclusive) | Detect/correct drift (report-only for Observed mode) |
| `GetStatus` | `ObjectSharedContext` (shared) | Return current status |
| `GetOutputs` | `ObjectSharedContext` (shared) | Return cluster outputs |

### Mutable vs Immutable Attributes

| Attribute | Mutability | Notes |
|---|---|---|
| `clusterIdentifier` | Immutable | Part of the Virtual Object key; cannot change after creation |
| `region` | Immutable | Aurora clusters cannot be moved between regions |
| `engine` | Immutable | Cannot change engine (aurora-postgresql ↔ aurora-mysql) |
| `masterUsername` | Immutable | Set at creation, cannot be changed |
| `databaseName` | Immutable | Initial database name, cannot be changed after creation |
| `engineVersion` | Mutable | Upgraded via `ModifyDBCluster`; supports minor/major upgrades |
| `masterUserPassword` | Mutable | Changed via `ModifyDBCluster`; write-only (never read back) |
| `backupRetentionPeriod` | Mutable | 1–35 days |
| `preferredBackupWindow` | Mutable | Daily backup window (UTC) |
| `preferredMaintenanceWindow` | Mutable | Weekly maintenance window (UTC) |
| `dbClusterParameterGroupName` | Mutable | Switch cluster parameter groups; may require reboot of instances |
| `vpcSecurityGroupIds` | Mutable | Modified via `ModifyDBCluster` |
| `deletionProtection` | Mutable | Toggle via `ModifyDBCluster`; must disable before delete |
| `port` | Mutable | Database port; change requires cluster modification |
| `enabledCloudwatchLogsExports` | Mutable | Log types to export ("audit", "error", "general", "slowquery", "postgresql") |
| `tags` | Mutable | Full replace via `AddTagsToResource` / `RemoveTagsFromResource` |

### Downstream Consumers

```text
${resources.my-cluster.outputs.clusterIdentifier}    → RDS Instance dbClusterIdentifier
${resources.my-cluster.outputs.endpoint}              → Application config (writer endpoint)
${resources.my-cluster.outputs.readerEndpoint}        → Application config (reader endpoint)
${resources.my-cluster.outputs.arn}                   → IAM policies, monitoring
${resources.my-cluster.outputs.port}                  → Application config
```

---

## 2. Key Strategy

### Key Scope: `KeyScopeRegion`

Aurora cluster identifiers are unique per region per AWS account. The key is
`region~clusterIdentifier`, matching the RDS DB Instance pattern.

```text
region~clusterIdentifier
```

### BuildKey vs BuildImportKey

- **`BuildKey(resourceDoc)`**: Extracts `spec.region` and `metadata.name` from the
  resource document. Returns `region~metadata.name`. The `metadata.name` maps to
  the cluster identifier.

- **`BuildImportKey(region, resourceID)`**: Returns `region~resourceID` where
  `resourceID` is the cluster identifier.

### BuildImportKey Produces the Same Key as BuildKey

Aurora cluster identifiers are user-chosen and unique within a region. Import and
template management converge on the same Virtual Object when the same identifier
is used.

### Identifier Uniqueness

Aurora enforces identifier uniqueness per region per account. `CreateDBCluster`
returns `DBClusterAlreadyExistsFault` if the identifier is taken. This natural
conflict signal eliminates the need for `praxis:managed-key` ownership tags.

---

## 3. File Inventory

```text
✦ schemas/aws/rds/aurora_cluster.cue                      — CUE schema for AuroraCluster resource
✦ internal/drivers/auroracluster/types.go                  — Spec, Outputs, ObservedState, State structs
✦ internal/drivers/auroracluster/aws.go                    — AuroraClusterAPI interface + realAuroraClusterAPI
✦ internal/drivers/auroracluster/drift.go                  — HasDrift(), ComputeFieldDiffs()
✦ internal/drivers/auroracluster/driver.go                 — AuroraClusterDriver Virtual Object
✦ internal/drivers/auroracluster/driver_test.go            — Unit tests for driver (mocked AWS)
✦ internal/drivers/auroracluster/aws_test.go               — Unit tests for error classification
✦ internal/drivers/auroracluster/drift_test.go             — Unit tests for drift detection
✦ internal/core/provider/auroracluster_adapter.go          — AuroraClusterAdapter implementing provider.Adapter
✦ internal/core/provider/auroracluster_adapter_test.go     — Unit tests for adapter
✦ tests/integration/auroracluster_driver_test.go           — Integration tests
✎ internal/core/provider/registry.go                       — Add NewAuroraClusterAdapter to NewRegistry()
✎ cmd/praxis-database/main.go                              — Bind AuroraClusterDriver
```

---

## Step 1 — CUE Schema

**File**: `schemas/aws/rds/aurora_cluster.cue`

```cue
package rds

#AuroraCluster: {
    apiVersion: "praxis.io/v1"
    kind:       "AuroraCluster"

    metadata: {
        // name maps to the Aurora cluster identifier.
        // Must match RDS naming rules: 1-63 chars, alphanumeric + hyphens,
        // first char must be a letter, cannot end with hyphen or contain
        // two consecutive hyphens.
        name: string & =~"^[a-zA-Z][a-zA-Z0-9-]{0,61}[a-zA-Z0-9]$"
        labels: [string]: string
    }

    spec: {
        // region is the AWS region to create the cluster in.
        region: string

        // engine is the Aurora engine ("aurora-postgresql" or "aurora-mysql").
        engine: "aurora-postgresql" | "aurora-mysql"

        // engineVersion is the Aurora engine version (e.g., "16.4", "3.07.1").
        engineVersion: string

        // masterUsername is the master user name.
        masterUsername: string

        // masterUserPassword is the master user password.
        // Supports SSM references (e.g., "ssm:///myapp/aurora-password").
        // Write-only: never read back from AWS API.
        masterUserPassword: string

        // databaseName is the name of the initial database.
        // Optional — omit to create a cluster with no initial database.
        databaseName?: string

        // port is the database port. Defaults depend on engine.
        port?: int & >=1150 & <=65535

        // dbSubnetGroupName is the name of the DB subnet group.
        dbSubnetGroupName?: string

        // dbClusterParameterGroupName is the cluster parameter group name.
        dbClusterParameterGroupName?: string

        // vpcSecurityGroupIds is a list of VPC security group IDs.
        vpcSecurityGroupIds: [...string] | *[]

        // storageEncrypted enables encryption at rest.
        storageEncrypted: bool | *true

        // kmsKeyId is the KMS key ARN for encryption. Uses AWS-managed key if omitted.
        kmsKeyId?: string

        // backupRetentionPeriod is the number of days to retain automated backups (1–35).
        backupRetentionPeriod: int & >=1 & <=35 | *7

        // preferredBackupWindow is the daily time range for automated backups (UTC).
        // Format: "hh24:mi-hh24:mi" (e.g., "03:00-04:00").
        preferredBackupWindow?: string

        // preferredMaintenanceWindow is the weekly time range for maintenance (UTC).
        // Format: "ddd:hh24:mi-ddd:hh24:mi" (e.g., "sun:05:00-sun:06:00").
        preferredMaintenanceWindow?: string

        // deletionProtection prevents accidental deletion.
        deletionProtection: bool | *false

        // enabledCloudwatchLogsExports lists the log types to export.
        // PostgreSQL: ["postgresql"]
        // MySQL: ["audit", "error", "general", "slowquery"]
        enabledCloudwatchLogsExports: [...string] | *[]

        // tags applied to the Aurora cluster.
        tags: [string]: string
    }

    outputs?: {
        clusterIdentifier:  string
        clusterResourceId:  string
        arn:                string
        endpoint:           string
        readerEndpoint:     string
        port:               int
        engine:             string
        engineVersion:      string
        status:             string
    }
}
```

### Key Design Decisions

- **`engine` restricted to Aurora engines**: Only `"aurora-postgresql"` and
  `"aurora-mysql"` are valid. Non-Aurora engines use the RDS DB Instance driver.

- **`masterUserPassword` is write-only**: Same pattern as RDS DB Instance — AWS
  never returns the password via API.

- **`backupRetentionPeriod` minimum is 1**: Unlike RDS DB instances (where 0
  disables backups), Aurora clusters always maintain automated backups with a
  minimum retention of 1 day.

- **`deletionProtection` defaults to `false`**: Simplifies development/testing.
  Production templates should explicitly enable it.

- **`storageEncrypted` defaults to `true`**: Opinionated best practice. Aurora
  storage is replicated across AZs; encryption adds protection at rest.

---

## Step 2 — AWS Client Factory

**File**: `internal/infra/awsclient/client.go` — Uses same `NewRDSClient` as
the RDS Instance driver. Aurora clusters are managed via the RDS API.

---

## Step 3 — Driver Types

**File**: `internal/drivers/auroracluster/types.go`

```go
package auroracluster

import "github.com/shirvan/praxis/pkg/types"

const ServiceName = "AuroraCluster"

type AuroraClusterSpec struct {
    Account                         string            `json:"account,omitempty"`
    Region                          string            `json:"region"`
    ClusterIdentifier               string            `json:"clusterIdentifier"`
    Engine                          string            `json:"engine"`
    EngineVersion                   string            `json:"engineVersion"`
    MasterUsername                  string            `json:"masterUsername"`
    MasterUserPassword              string            `json:"masterUserPassword,omitempty"`
    DatabaseName                    string            `json:"databaseName,omitempty"`
    Port                            int32             `json:"port,omitempty"`
    DBSubnetGroupName               string            `json:"dbSubnetGroupName,omitempty"`
    DBClusterParameterGroupName     string            `json:"dbClusterParameterGroupName,omitempty"`
    VpcSecurityGroupIds             []string          `json:"vpcSecurityGroupIds,omitempty"`
    StorageEncrypted                bool              `json:"storageEncrypted"`
    KMSKeyId                        string            `json:"kmsKeyId,omitempty"`
    BackupRetentionPeriod           int32             `json:"backupRetentionPeriod"`
    PreferredBackupWindow           string            `json:"preferredBackupWindow,omitempty"`
    PreferredMaintenanceWindow      string            `json:"preferredMaintenanceWindow,omitempty"`
    DeletionProtection              bool              `json:"deletionProtection"`
    EnabledCloudwatchLogsExports    []string          `json:"enabledCloudwatchLogsExports,omitempty"`
    Tags                            map[string]string `json:"tags,omitempty"`
}

type AuroraClusterOutputs struct {
    ClusterIdentifier string `json:"clusterIdentifier"`
    ClusterResourceId string `json:"clusterResourceId"`
    ARN               string `json:"arn"`
    Endpoint          string `json:"endpoint"`
    ReaderEndpoint    string `json:"readerEndpoint"`
    Port              int32  `json:"port"`
    Engine            string `json:"engine"`
    EngineVersion     string `json:"engineVersion"`
    Status            string `json:"status"`
}

type ObservedState struct {
    ClusterIdentifier               string            `json:"clusterIdentifier"`
    ClusterResourceId               string            `json:"clusterResourceId"`
    ARN                             string            `json:"arn"`
    Engine                          string            `json:"engine"`
    EngineVersion                   string            `json:"engineVersion"`
    MasterUsername                  string            `json:"masterUsername"`
    DatabaseName                    string            `json:"databaseName"`
    Port                            int32             `json:"port"`
    DBSubnetGroupName               string            `json:"dbSubnetGroupName"`
    DBClusterParameterGroupName     string            `json:"dbClusterParameterGroupName"`
    VpcSecurityGroupIds             []string          `json:"vpcSecurityGroupIds"`
    StorageEncrypted                bool              `json:"storageEncrypted"`
    KMSKeyId                        string            `json:"kmsKeyId"`
    BackupRetentionPeriod           int32             `json:"backupRetentionPeriod"`
    PreferredBackupWindow           string            `json:"preferredBackupWindow"`
    PreferredMaintenanceWindow      string            `json:"preferredMaintenanceWindow"`
    DeletionProtection              bool              `json:"deletionProtection"`
    EnabledCloudwatchLogsExports    []string          `json:"enabledCloudwatchLogsExports"`
    Endpoint                        string            `json:"endpoint"`
    ReaderEndpoint                  string            `json:"readerEndpoint"`
    Status                          string            `json:"status"`
    Members                         []string          `json:"members"`
    Tags                            map[string]string `json:"tags"`
}

type AuroraClusterState struct {
    Desired            AuroraClusterSpec    `json:"desired"`
    Observed           ObservedState        `json:"observed"`
    Outputs            AuroraClusterOutputs `json:"outputs"`
    Status             types.ResourceStatus `json:"status"`
    Mode               types.Mode           `json:"mode"`
    Error              string               `json:"error,omitempty"`
    Generation         int64                `json:"generation"`
    LastReconcile      string               `json:"lastReconcile,omitempty"`
    ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
```

### Key Type Decisions

- **`MasterUserPassword` in spec only**: Same write-only pattern as RDS Instance.
- **`Members` in ObservedState**: List of cluster member DB instance identifiers.
  Useful for pre-deletion validation (cluster must have no members before delete).
- **`VpcSecurityGroupIds` sorted**: Same pattern as RDS Instance.

---

## Step 4 — AWS API Abstraction Layer

**File**: `internal/drivers/auroracluster/aws.go`

### AuroraClusterAPI Interface

```go
type AuroraClusterAPI interface {
    // CreateDBCluster creates a new Aurora cluster.
    CreateDBCluster(ctx context.Context, spec AuroraClusterSpec) (string, error)

    // DescribeDBCluster returns the observed state of an Aurora cluster.
    DescribeDBCluster(ctx context.Context, clusterIdentifier string) (ObservedState, error)

    // ModifyDBCluster modifies mutable attributes of an Aurora cluster.
    ModifyDBCluster(ctx context.Context, spec AuroraClusterSpec, applyImmediately bool) error

    // DeleteDBCluster deletes an Aurora cluster.
    // skipFinalSnapshot controls whether a final snapshot is created.
    DeleteDBCluster(ctx context.Context, clusterIdentifier string, skipFinalSnapshot bool) error

    // WaitUntilAvailable blocks until the cluster reaches "available" status.
    WaitUntilAvailable(ctx context.Context, clusterIdentifier string) error

    // WaitUntilDeleted blocks until the cluster is fully deleted.
    WaitUntilDeleted(ctx context.Context, clusterIdentifier string) error

    // UpdateTags replaces all tags on the cluster (by ARN).
    UpdateTags(ctx context.Context, arn string, tags map[string]string) error

    // ListTags returns all tags on the cluster (by ARN).
    ListTags(ctx context.Context, arn string) (map[string]string, error)
}
```

### realAuroraClusterAPI Implementation

```go
type realAuroraClusterAPI struct {
    client  *rds.Client
    limiter *ratelimit.Limiter
}

func NewAuroraClusterAPI(client *rds.Client) AuroraClusterAPI {
    return &realAuroraClusterAPI{
        client:  client,
        limiter: ratelimit.New("rds", 15, 8),
    }
}
```

### Key Implementation Details

#### `CreateDBCluster`

```go
func (r *realAuroraClusterAPI) CreateDBCluster(ctx context.Context, spec AuroraClusterSpec) (string, error) {
    input := &rds.CreateDBClusterInput{
        DBClusterIdentifier: aws.String(spec.ClusterIdentifier),
        Engine:              aws.String(spec.Engine),
        EngineVersion:       aws.String(spec.EngineVersion),
        MasterUsername:      aws.String(spec.MasterUsername),
        MasterUserPassword:  aws.String(spec.MasterUserPassword),
        StorageEncrypted:    aws.Bool(spec.StorageEncrypted),
        DeletionProtection:  aws.Bool(spec.DeletionProtection),
        BackupRetentionPeriod: aws.Int32(spec.BackupRetentionPeriod),
    }

    if spec.DatabaseName != "" {
        input.DatabaseName = aws.String(spec.DatabaseName)
    }
    if spec.Port > 0 {
        input.Port = aws.Int32(spec.Port)
    }
    if spec.DBSubnetGroupName != "" {
        input.DBSubnetGroupName = aws.String(spec.DBSubnetGroupName)
    }
    if spec.DBClusterParameterGroupName != "" {
        input.DBClusterParameterGroupName = aws.String(spec.DBClusterParameterGroupName)
    }
    if len(spec.VpcSecurityGroupIds) > 0 {
        input.VpcSecurityGroupIds = spec.VpcSecurityGroupIds
    }
    if spec.KMSKeyId != "" {
        input.KmsKeyId = aws.String(spec.KMSKeyId)
    }
    if spec.PreferredBackupWindow != "" {
        input.PreferredBackupWindow = aws.String(spec.PreferredBackupWindow)
    }
    if spec.PreferredMaintenanceWindow != "" {
        input.PreferredMaintenanceWindow = aws.String(spec.PreferredMaintenanceWindow)
    }
    if len(spec.EnabledCloudwatchLogsExports) > 0 {
        input.EnableCloudwatchLogsExports = spec.EnabledCloudwatchLogsExports
    }
    if len(spec.Tags) > 0 {
        input.Tags = toRDSTags(spec.Tags)
    }

    out, err := r.client.CreateDBCluster(ctx, input)
    if err != nil {
        return "", err
    }
    return aws.ToString(out.DBCluster.DbClusterResourceId), nil
}
```

#### `DescribeDBCluster`

```go
func (r *realAuroraClusterAPI) DescribeDBCluster(ctx context.Context, clusterIdentifier string) (ObservedState, error) {
    out, err := r.client.DescribeDBClusters(ctx, &rds.DescribeDBClustersInput{
        DBClusterIdentifier: aws.String(clusterIdentifier),
    })
    if err != nil {
        return ObservedState{}, err
    }
    if len(out.DBClusters) == 0 {
        return ObservedState{}, fmt.Errorf("Aurora cluster %s not found", clusterIdentifier)
    }
    cluster := out.DBClusters[0]

    obs := ObservedState{
        ClusterIdentifier:           aws.ToString(cluster.DBClusterIdentifier),
        ClusterResourceId:           aws.ToString(cluster.DbClusterResourceId),
        ARN:                         aws.ToString(cluster.DBClusterArn),
        Engine:                      aws.ToString(cluster.Engine),
        EngineVersion:               aws.ToString(cluster.EngineVersion),
        MasterUsername:              aws.ToString(cluster.MasterUsername),
        DatabaseName:                aws.ToString(cluster.DatabaseName),
        Port:                        aws.ToInt32(cluster.Port),
        StorageEncrypted:            aws.ToBool(cluster.StorageEncrypted),
        KMSKeyId:                    aws.ToString(cluster.KmsKeyId),
        BackupRetentionPeriod:       aws.ToInt32(cluster.BackupRetentionPeriod),
        PreferredBackupWindow:       aws.ToString(cluster.PreferredBackupWindow),
        PreferredMaintenanceWindow:  aws.ToString(cluster.PreferredMaintenanceWindow),
        DeletionProtection:          aws.ToBool(cluster.DeletionProtection),
        EnabledCloudwatchLogsExports: cluster.EnabledCloudwatchLogsExports,
        Endpoint:                    aws.ToString(cluster.Endpoint),
        ReaderEndpoint:              aws.ToString(cluster.ReaderEndpoint),
        Status:                      aws.ToString(cluster.Status),
    }

    // Subnet group
    if cluster.DBSubnetGroup != nil {
        obs.DBSubnetGroupName = aws.ToString(cluster.DBSubnetGroup)
    }

    // Cluster parameter group
    if len(cluster.DBClusterParameterGroup) > 0 {
        obs.DBClusterParameterGroupName = aws.ToString(cluster.DBClusterParameterGroup)
    }

    // Security groups
    for _, sg := range cluster.VpcSecurityGroups {
        obs.VpcSecurityGroupIds = append(obs.VpcSecurityGroupIds, aws.ToString(sg.VpcSecurityGroupId))
    }

    // Members
    for _, member := range cluster.DBClusterMembers {
        obs.Members = append(obs.Members, aws.ToString(member.DBInstanceIdentifier))
    }

    return obs, nil
}
```

### Error Classification Helpers

```go
func IsNotFound(err error) bool {
    if err == nil {
        return false
    }
    var apiErr smithy.APIError
    if errors.As(err, &apiErr) {
        return apiErr.ErrorCode() == "DBClusterNotFoundFault"
    }
    return strings.Contains(err.Error(), "DBClusterNotFoundFault")
}

func IsAlreadyExists(err error) bool {
    if err == nil {
        return false
    }
    var apiErr smithy.APIError
    if errors.As(err, &apiErr) {
        return apiErr.ErrorCode() == "DBClusterAlreadyExistsFault"
    }
    return strings.Contains(err.Error(), "DBClusterAlreadyExistsFault")
}

func IsInvalidState(err error) bool {
    if err == nil {
        return false
    }
    var apiErr smithy.APIError
    if errors.As(err, &apiErr) {
        return apiErr.ErrorCode() == "InvalidDBClusterStateFault"
    }
    return strings.Contains(err.Error(), "InvalidDBClusterStateFault")
}

func IsHasMembers(err error) bool {
    if err == nil {
        return false
    }
    // Cannot delete cluster with active members
    return strings.Contains(err.Error(), "member") ||
           strings.Contains(err.Error(), "DBClusterMember")
}
```

### Helper Functions

Same `toRDSTags` / `fromRDSTags` pattern as RDS Instance (uses same SDK types).

---

## Step 5 — Drift Detection

**File**: `internal/drivers/auroracluster/drift.go`

### Core Functions

**`HasDrift(desired AuroraClusterSpec, observed ObservedState) bool`**

```go
func HasDrift(desired AuroraClusterSpec, observed ObservedState) bool {
    if desired.EngineVersion != observed.EngineVersion {
        return true
    }
    if desired.BackupRetentionPeriod != observed.BackupRetentionPeriod {
        return true
    }
    if desired.DeletionProtection != observed.DeletionProtection {
        return true
    }
    if desired.Port > 0 && desired.Port != observed.Port {
        return true
    }
    if desired.DBClusterParameterGroupName != "" &&
       desired.DBClusterParameterGroupName != observed.DBClusterParameterGroupName {
        return true
    }
    if !securityGroupIdsEqual(desired.VpcSecurityGroupIds, observed.VpcSecurityGroupIds) {
        return true
    }
    if !cloudwatchLogsEqual(desired.EnabledCloudwatchLogsExports, observed.EnabledCloudwatchLogsExports) {
        return true
    }
    // masterUserPassword is write-only — cannot detect drift
    return !tagsMatch(desired.Tags, observed.Tags)
}
```

**`ComputeFieldDiffs(desired AuroraClusterSpec, observed ObservedState) []FieldDiffEntry`**

Produces human-readable diffs:

- Immutable fields: `engine`, `masterUsername`, `databaseName`, `storageEncrypted`,
  `kmsKeyId` — reported with "(immutable, ignored)" suffix.
- Mutable fields: `engineVersion`, `backupRetentionPeriod`, `deletionProtection`,
  `port`, `dbClusterParameterGroupName`, `vpcSecurityGroupIds`,
  `enabledCloudwatchLogsExports`, `tags`.
- Write-only fields: `masterUserPassword` — reported as "(write-only, drift not
  detectable)".

### CloudWatch Logs Export Comparison

```go
func cloudwatchLogsEqual(desired, observed []string) bool {
    if len(desired) != len(observed) {
        return false
    }
    dSet := make(map[string]bool, len(desired))
    for _, log := range desired {
        dSet[log] = true
    }
    for _, log := range observed {
        if !dSet[log] {
            return false
        }
    }
    return true
}
```

---

## Step 6 — Driver Implementation

**File**: `internal/drivers/auroracluster/driver.go`

### Service Registration

```go
const ServiceName = "AuroraCluster"
```

### Constructor Pattern

```go
func NewAuroraClusterDriver(accounts *auth.Registry) *AuroraClusterDriver
func NewAuroraClusterDriverWithFactory(accounts *auth.Registry, factory func(aws.Config) AuroraClusterAPI) *AuroraClusterDriver
```

### Provision Handler

1. **Input validation**: `clusterIdentifier`, `engine`, `engineVersion`,
   `masterUsername`, `masterUserPassword` must be non-empty. `engine` must be
   `"aurora-postgresql"` or `"aurora-mysql"`. Returns `TerminalError(400)`.

2. **Load current state**: Reads `AuroraClusterState` from Restate K/V. Sets status
   to `Provisioning`, increments generation.

3. **Re-provision check**: If `state.Outputs.ClusterIdentifier` is non-empty,
   describes the cluster. If deleted externally (404), clears outputs and falls
   through to creation.

4. **Create cluster**: Calls `api.CreateDBCluster`. Classifies errors:
   - `IsAlreadyExists` → `TerminalError(409)`

5. **Wait for available**: Calls `api.WaitUntilAvailable` wrapped in `restate.Run`.

6. **Converge mutable attributes** (re-provision path):
   - Engine version: `ModifyDBCluster` with `ApplyImmediately=true`.
   - Backup retention, maintenance windows: `ModifyDBCluster`.
   - Security groups: `ModifyDBCluster`.
   - Deletion protection: `ModifyDBCluster`.
   - CloudWatch logs exports: `ModifyDBCluster`.
   - Tags: `UpdateTags` with ARN.

7. **Describe final state**: Calls `api.DescribeDBCluster`.

8. **Commit state**: Sets status to `Ready`, saves atomically, schedules reconcile.

### Import Handler

1. Describes the cluster by `ref.ResourceID` (the cluster identifier).
2. Synthesizes an `AuroraClusterSpec` from the observed state.
3. Fetches tags via `api.ListTags(arn)`.
4. Sets mode to `ModeObserved`.
5. Schedules reconciliation.

### Delete Handler

1. Sets status to `Deleting`.
2. **Check for cluster members**: If `observed.Members` is non-empty, return
   `TerminalError(409)` instructing the user to delete cluster instances first.
   The DAG should handle this ordering, but defensive checking prevents data loss.
3. **Disable deletion protection**: If enabled, modifies to disable. Waits for
   available.
4. **Delete cluster**: Calls `api.DeleteDBCluster` with `skipFinalSnapshot=true`.
5. **Wait for deletion**: Calls `api.WaitUntilDeleted`.
6. **Error classification**:
   - `IsNotFound` → silent success (already gone).
   - `IsInvalidState` → `TerminalError(409)`.
7. Sets status to `StatusDeleted`.

### Reconcile Handler

Standard reconcile on 5-minute timer. Corrects mutable attributes for Managed mode,
reports-only for Observed mode.

---

## Step 7 — Provider Adapter

**File**: `internal/core/provider/auroracluster_adapter.go`

### Methods

**`Scope() KeyScope`** → `KeyScopeRegion`

**`Kind() string`** → `"AuroraCluster"`

**`ServiceName() string`** → `"AuroraCluster"`

**`BuildKey(resourceDoc json.RawMessage) (string, error)`**:
Returns `region~metadata.name`.

**`BuildImportKey(region, resourceID string) (string, error)`**:
Returns `region~resourceID`.

**`Plan(ctx, key, account, desiredSpec) (DiffOperation, []FieldDiff, error)`**:
Calls `api.DescribeDBCluster`. Not found → `OpCreate`. Found → diff. No diffs
→ `OpNoOp`. Diffs → `OpUpdate`.

---

## Step 8 — Registry Integration

Add `NewAuroraClusterAdapterWithRegistry(accounts)` to `NewRegistry()`.

---

## Step 9 — Database Driver Pack Entry Point

Bind `AuroraClusterDriver` in `cmd/praxis-database/main.go` alongside other RDS
drivers.

---

## Step 10 — Docker Compose & Justfile

Uses the same `praxis-database` service on port 9086 as the RDS Instance driver.

### Justfile Targets

| Target | Command |
|---|---|
| `test-auroracluster` | `go test ./internal/drivers/auroracluster/... -v -count=1 -race` |
| `test-auroracluster-integration` | `go test ./tests/integration/ -run TestAuroraCluster -v -count=1 -tags=integration -timeout=15m` |

---

## Step 11 — Unit Tests

### `internal/drivers/auroracluster/drift_test.go`

| Test | Purpose |
|---|---|
| `TestHasDrift_NoDrift` | Matching state → no drift |
| `TestHasDrift_EngineVersionDrift` | Engine version change → drift |
| `TestHasDrift_BackupRetentionDrift` | Backup retention change → drift |
| `TestHasDrift_DeletionProtectionDrift` | Deletion protection toggle → drift |
| `TestHasDrift_PortDrift` | Port change → drift |
| `TestHasDrift_ParameterGroupDrift` | Cluster parameter group change → drift |
| `TestHasDrift_SecurityGroupDrift` | SG ID list change → drift |
| `TestHasDrift_SecurityGroupOrderIndependent` | Same SG IDs, different order → no drift |
| `TestHasDrift_CloudwatchLogsDrift` | Log export list change → drift |
| `TestHasDrift_TagDrift` | Tag change → drift |
| `TestHasDrift_PasswordNotDetectable` | Password change → no drift (write-only) |
| `TestComputeFieldDiffs_ImmutableEngine` | Reports engine change as "(immutable, ignored)" |
| `TestComputeFieldDiffs_ImmutableDatabaseName` | Reports database name as "(immutable, ignored)" |

### `internal/drivers/auroracluster/aws_test.go`

| Test | Purpose |
|---|---|
| `TestIsNotFound_DBClusterNotFound` | Cluster not-found error → true |
| `TestIsNotFound_OtherError` | Other error → false |
| `TestIsAlreadyExists_DBClusterAlreadyExists` | Duplicate identifier → true |
| `TestIsInvalidState_True` | Invalid state error → true |
| `TestIsNotFound_WrappedRestateError` | String fallback for Restate-wrapped errors |

### `internal/drivers/auroracluster/driver_test.go`

| Test | Purpose |
|---|---|
| `TestSpecFromObserved_RoundTrip` | Observed → spec preserves all fields except password |
| `TestServiceName` | Returns "AuroraCluster" |
| `TestOutputsFromObserved` | Correct output mapping |

### `internal/core/provider/auroracluster_adapter_test.go`

| Test | Purpose |
|---|---|
| `TestAuroraClusterAdapter_BuildKey` | Returns `region~clusterIdentifier` |
| `TestAuroraClusterAdapter_BuildImportKey` | Returns `region~clusterIdentifier` |
| `TestAuroraClusterAdapter_Kind` | Returns "AuroraCluster" |
| `TestAuroraClusterAdapter_Scope` | Returns `KeyScopeRegion` |

---

## Step 12 — Integration Tests

**File**: `tests/integration/auroracluster_driver_test.go`

### Test Cases

| Test | Description |
|---|---|
| `TestAuroraClusterProvision_CreatesCluster` | Creates an Aurora PostgreSQL cluster with subnet group and SG. Verifies cluster exists in LocalStack. |
| `TestAuroraClusterProvision_Idempotent` | Provisions same spec twice. Verifies same ClusterResourceId. |
| `TestAuroraClusterProvision_UpdateEngineVersion` | Re-provisions with changed version. Verifies upgrade applied. |
| `TestAuroraClusterProvision_ToggleDeletionProtection` | Re-provisions toggling deletion protection. |
| `TestAuroraClusterImport_ExistingCluster` | Creates cluster via RDS API, imports via driver. Verifies Observed mode. |
| `TestAuroraClusterDelete_RemovesCluster` | Provisions, then deletes. Verifies cluster removal. |
| `TestAuroraClusterDelete_DeletionProtection` | Provisions with protection, deletes. Verifies auto-disable. |
| `TestAuroraClusterDelete_WithMembers` | Provisions cluster + instance, attempts delete cluster. Verifies 409. |
| `TestAuroraClusterReconcile_DetectsDrift` | Provisions, modifies via API, reconciles. Verifies correction. |
| `TestAuroraClusterGetStatus_ReturnsReady` | Provisions, checks `GetStatus` returns Ready. |

---

## Aurora-Cluster-Specific Design Decisions

### 1. Cluster vs Instance Separation

Aurora separates the cluster (storage + endpoints) from instances (compute). The
cluster driver manages only the cluster resource. Instances are managed via the
RDS DB Instance driver with `dbClusterIdentifier` set. This matches AWS's own
resource model and allows independent scaling of compute.

### 2. Member Validation Before Delete

The Delete handler checks `observed.Members` before attempting deletion. If the
cluster has active instances, deletion returns `TerminalError(409)` instructing
the user to delete instances first. In practice, the DAG should order deletions
correctly (instances before cluster), but defensive checking prevents accidental
data loss.

### 3. Shared RDS API Client

Aurora clusters use the same `rds.Client` as RDS instances. The RDS API provides
both `CreateDBInstance` and `CreateDBCluster` operations. This means the database
driver pack only needs one AWS SDK dependency (`aws-sdk-go-v2/service/rds`).

### 4. Shared Rate Limiter

All RDS/Aurora drivers share the `"rds"` rate limiter namespace. This is correct
because Aurora and RDS operations share the same AWS API rate limits.

### 5. No Storage Management

Aurora's storage layer is fully managed by AWS (auto-scaling, replication). The
driver has no storage-related fields unlike RDS instances. Storage encryption is
set at creation time and is immutable.

### 6. Engine Version Upgrades

Aurora engine version upgrades are applied via `ModifyDBCluster` with
`ApplyImmediately=true`. Major version upgrades require
`AllowMajorVersionUpgrade=true`. The driver detects major vs minor by comparing
the major version component and sets the flag accordingly.

### 7. Global Clusters Out of Scope

Aurora Global Clusters (cross-region) add significant complexity (secondary
clusters, failover promotion). They are out of scope for v1 and would be a
separate driver if needed.

### 8. Serverless v2 Out of Scope

Aurora Serverless v2 requires `ServerlessV2ScalingConfiguration` on the cluster and
`db.serverless` instance class on instances. This is a future enhancement that
extends both the cluster and instance drivers.

---

## Design Decisions (Resolved)

1. **Should Aurora instances be part of this driver?**
   No. Aurora instances have their own lifecycle (promotion, failover priority) and
   are standard RDS instances with `dbClusterIdentifier` set. The RDS Instance driver
   handles them. This mirrors AWS's resource model.

2. **Should the driver manage cluster endpoints (custom)?**
   No. Custom endpoints are a separate resource (`CreateDBClusterEndpoint`) and would
   be their own driver. The built-in writer and reader endpoints are managed implicitly.

3. **Should Serverless v2 scaling be supported?**
   Not in v1. Serverless v2 requires additional spec fields
   (`minCapacity`, `maxCapacity`) and changes to how instances are provisioned.
   A future enhancement can add `serverlessV2ScalingConfig` to the spec.

4. **Should the driver support restore from snapshot?**
   Not in v1. `RestoreDBClusterFromSnapshot` is a separate creation path with
   snapshot-specific parameters. A future enhancement could add a `restoreFrom`
   spec field.

---

## Checklist

- [x] **Schema**: `schemas/aws/rds/aurora_cluster.cue` created
- [x] **Types**: `internal/drivers/auroracluster/types.go` created
- [x] **AWS API**: `internal/drivers/auroracluster/aws.go` created
- [x] **Drift**: `internal/drivers/auroracluster/drift.go` created
- [x] **Driver**: `internal/drivers/auroracluster/driver.go` created with all 6 handlers
- [x] **Adapter**: `internal/core/provider/auroracluster_adapter.go` created
- [x] **Registry**: `internal/core/provider/registry.go` updated
- [x] **Entry point**: `cmd/praxis-database/main.go` updated with Aurora binding
- [x] **Unit tests (drift)**: `internal/drivers/auroracluster/drift_test.go`
- [x] **Unit tests (aws helpers)**: `internal/drivers/auroracluster/aws_test.go`
- [x] **Unit tests (driver)**: `internal/drivers/auroracluster/driver_test.go`
- [x] **Unit tests (adapter)**: `internal/core/provider/auroracluster_adapter_test.go`
- [x] **Integration tests**: `tests/integration/auroracluster_driver_test.go`
- [x] **Password write-only**: Skipped in drift detection
- [x] **Member validation**: Delete blocks if cluster has active instances
- [x] **Deletion protection auto-disable**: Tested in integration
- [x] **Import default mode**: `ModeObserved` (high-value resource)
- [x] **Delete mode guard**: Delete handler blocks for ModeObserved (409)
- [x] **Engine version upgrade**: Major vs minor detection
- [x] **Build passes**: `go build ./...` succeeds
- [x] **Unit tests pass**: `go test ./internal/drivers/auroracluster/... -race`
- [x] **Integration tests pass**: `go test ./tests/integration/ -run TestAuroraCluster -tags=integration`
