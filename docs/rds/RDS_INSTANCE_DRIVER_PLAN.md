# RDS DB Instance Driver — Implementation Spec

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
12. [Step 9 — Storage Driver Pack Entry Point](#step-9--storage-driver-pack-entry-point)
13. [Step 10 — Docker Compose & Justfile](#step-10--docker-compose--justfile)
14. [Step 11 — Unit Tests](#step-11--unit-tests)
15. [Step 12 — Integration Tests](#step-12--integration-tests)
16. [RDS-Instance-Specific Design Decisions](#rds-instance-specific-design-decisions)
17. [Design Decisions (Resolved)](#design-decisions-resolved)
18. [Checklist](#checklist)

---

## 1. Overview & Scope

The RDS DB Instance driver manages the lifecycle of RDS **DB instances** only.
Read replicas, snapshots, automated backups, event subscriptions, and proxy
connections are out of scope for this driver. Aurora cluster instances are created
via this driver with `dbClusterIdentifier` set—the Aurora Cluster driver manages
the cluster resource itself.

The driver follows the established Virtual Object contract:

| Handler | Context | Purpose |
|---|---|---|
| `Provision` | `ObjectContext` (exclusive) | Create or converge a DB instance |
| `Import` | `ObjectContext` (exclusive) | Adopt an existing DB instance |
| `Delete` | `ObjectContext` (exclusive) | Delete a DB instance (skip final snapshot by default) |
| `Reconcile` | `ObjectContext` (exclusive) | Detect/correct drift (report-only for Observed mode) |
| `GetStatus` | `ObjectSharedContext` (shared) | Return current status |
| `GetOutputs` | `ObjectSharedContext` (shared) | Return DB instance outputs |

### Mutable vs Immutable Attributes

| Attribute | Mutability | Notes |
|---|---|---|
| `dbIdentifier` | Immutable | The Virtual Object key component; renaming requires delete + recreate |
| `region` | Immutable | RDS instances cannot be moved between regions |
| `engine` | Immutable | Cannot change engine after creation (e.g., postgres → mysql) |
| `masterUsername` | Immutable | Set at creation, cannot be changed |
| `instanceClass` | Mutable | Modified via `ModifyDBInstance`; may require reboot or failover |
| `engineVersion` | Mutable | Upgraded via `ModifyDBInstance`; supports minor/major version upgrades |
| `allocatedStorage` | Mutable | Grow-only; cannot shrink. Modified via `ModifyDBInstance` |
| `storageType` | Mutable | gp2 → gp3, io1 → io2, etc. via `ModifyDBInstance` |
| `iops` | Mutable | Only for io1/io2/gp3. Modified via `ModifyDBInstance` |
| `storageThroughput` | Mutable | Only for gp3. Modified via `ModifyDBInstance` |
| `multiAZ` | Mutable | Toggle via `ModifyDBInstance`; may cause brief failover |
| `publiclyAccessible` | Mutable | Toggle via `ModifyDBInstance` |
| `backupRetentionPeriod` | Mutable | 0 disables automated backups; 1–35 days |
| `preferredBackupWindow` | Mutable | Daily backup window (UTC) |
| `preferredMaintenanceWindow` | Mutable | Weekly maintenance window (UTC) |
| `parameterGroupName` | Mutable | Switch parameter groups; may require reboot |
| `masterUserPassword` | Mutable | Changed via `ModifyDBInstance`; write-only (never read back) |
| `vpcSecurityGroupIds` | Mutable | Modified via `ModifyDBInstance` |
| `deletionProtection` | Mutable | Toggle via `ModifyDBInstance`; must disable before delete |
| `tags` | Mutable | Full replace via `AddTagsToResource` / `RemoveTagsFromResource` |

### Downstream Consumers

```text
${resources.mydb.outputs.endpoint}         → Application config, connection strings
${resources.mydb.outputs.port}              → Application config
${resources.mydb.outputs.arn}               → IAM policies, monitoring
${resources.mydb.outputs.dbIdentifier}      → CloudWatch metrics, log group names
${resources.mydb.outputs.dbiResourceId}     → IAM auth tokens
```

---

## 2. Key Strategy

### Key Scope: `KeyScopeRegion`

RDS DB instance identifiers are unique per region per AWS account. The key is
`region~dbIdentifier`, matching the EC2 Instance pattern.

```text
region~dbIdentifier
```

### BuildKey vs BuildImportKey

- **`BuildKey(resourceDoc)`**: Extracts `spec.region` and `metadata.name` from the
  resource document. Returns `region~metadata.name`. The `metadata.name` maps to
  the RDS DB instance identifier.

- **`BuildImportKey(region, resourceID)`**: Returns `region~resourceID` where
  `resourceID` is the DB instance identifier. This produces the **same key** as
  `BuildKey` when the user's `metadata.name` matches the DB identifier — same
  pattern as EC2.

### BuildImportKey Produces the Same Key as BuildKey

Unlike SG (where `BuildImportKey` uses the group ID, different from
`vpcId~groupName`), RDS identifiers are user-chosen and unique within a region.
Import and template management converge on the same Virtual Object when the same
identifier is used. This matches the S3 and IAM Role patterns where the resource
name IS the identity.

### Identifier Uniqueness

RDS enforces identifier uniqueness per region per account. `CreateDBInstance` returns
`DBInstanceAlreadyExists` if the identifier is taken. This natural conflict signal
eliminates the need for `praxis:managed-key` ownership tags. The duplicate error
maps to a terminal 409 in the Provision handler.

### Plan-Time Instance Resolution

The adapter's `Plan()` method calls `DescribeDBInstances` with the DB identifier
directly — unlike EC2 (where Name tags are mutable and non-unique), RDS identifiers
are stable, unique, and the primary AWS lookup key.

---

## 3. File Inventory

```text
✦ schemas/aws/rds/instance.cue                        — CUE schema for RDSInstance resource
✦ internal/drivers/rdsinstance/types.go                — Spec, Outputs, ObservedState, State structs
✦ internal/drivers/rdsinstance/aws.go                  — RDSInstanceAPI interface + realRDSInstanceAPI
✦ internal/drivers/rdsinstance/drift.go                — HasDrift(), ComputeFieldDiffs()
✦ internal/drivers/rdsinstance/driver.go               — RDSInstanceDriver Virtual Object
✦ internal/drivers/rdsinstance/driver_test.go          — Unit tests for driver (mocked AWS)
✦ internal/drivers/rdsinstance/aws_test.go             — Unit tests for error classification
✦ internal/drivers/rdsinstance/drift_test.go           — Unit tests for drift detection
✦ internal/core/provider/rdsinstance_adapter.go        — RDSInstanceAdapter implementing provider.Adapter
✦ internal/core/provider/rdsinstance_adapter_test.go   — Unit tests for adapter
✦ tests/integration/rdsinstance_driver_test.go         — Integration tests
✔ cmd/praxis-storage/main.go                          — Storage driver pack entry point
✔ cmd/praxis-storage/Dockerfile                       — Multi-stage Docker build
✎ internal/infra/awsclient/client.go                   — Add NewRDSClient()
✎ internal/core/provider/registry.go                   — Add NewRDSInstanceAdapter to NewRegistry()
✔ docker-compose.yaml                                  — praxis-storage service includes RDS drivers
✎ justfile                                             — Add database build/test/register targets
```

---

## Step 1 — CUE Schema

**File**: `schemas/aws/rds/instance.cue`

```cue
package rds

#RDSInstance: {
    apiVersion: "praxis.io/v1"
    kind:       "RDSInstance"

    metadata: {
        // name maps to the DB instance identifier in AWS.
        // Must match RDS naming rules: 1-63 chars, alphanumeric + hyphens,
        // first char must be a letter, cannot end with a hyphen or contain
        // two consecutive hyphens.
        name: string & =~"^[a-zA-Z][a-zA-Z0-9-]{0,61}[a-zA-Z0-9]$"
        labels: [string]: string
    }

    spec: {
        // region is the AWS region to create the instance in.
        region: string

        // engine is the database engine (e.g., "postgres", "mysql", "mariadb",
        // "oracle-ee", "sqlserver-ee", "aurora-postgresql", "aurora-mysql").
        engine: string

        // engineVersion is the database engine version (e.g., "16.4", "8.0.35").
        engineVersion: string

        // instanceClass is the DB instance class (e.g., "db.t3.micro", "db.r6g.large").
        instanceClass: string

        // allocatedStorage is the initial storage in GiB.
        // Not required for Aurora cluster instances.
        allocatedStorage?: int & >=20 & <=65536

        // storageType is the storage type ("gp2", "gp3", "io1", "io2").
        storageType: "gp2" | "gp3" | "io1" | "io2" | *"gp3"

        // iops is the provisioned IOPS. Required for io1/io2, optional for gp3.
        iops?: int & >=1000 & <=256000

        // storageThroughput is the provisioned throughput in MiBps. Only for gp3.
        storageThroughput?: int & >=125 & <=4000

        // storageEncrypted enables encryption at rest.
        storageEncrypted: bool | *true

        // kmsKeyId is the KMS key ARN for encryption. Uses AWS-managed key if omitted.
        kmsKeyId?: string

        // masterUsername is the master user name.
        // Not required for Aurora cluster instances.
        masterUsername?: string

        // masterUserPassword is the master user password.
        // Supports SSM references (e.g., "ssm:///myapp/db-password").
        // Write-only: never read back from AWS API.
        // Not required for Aurora cluster instances.
        masterUserPassword?: string

        // dbSubnetGroupName is the name of the DB subnet group.
        dbSubnetGroupName?: string

        // parameterGroupName is the name of the DB parameter group.
        parameterGroupName?: string

        // vpcSecurityGroupIds is a list of VPC security group IDs.
        vpcSecurityGroupIds: [...string] | *[]

        // dbClusterIdentifier associates this instance with an Aurora cluster.
        // When set, several fields (allocatedStorage, masterUsername,
        // masterUserPassword, dbSubnetGroupName) are inherited from the cluster.
        dbClusterIdentifier?: string

        // multiAZ enables Multi-AZ deployment for high availability.
        // Not applicable for Aurora cluster instances.
        multiAZ: bool | *false

        // publiclyAccessible determines whether the instance has a public IP.
        publiclyAccessible: bool | *false

        // backupRetentionPeriod is the number of days to retain automated backups (0–35).
        // 0 disables automated backups.
        backupRetentionPeriod: int & >=0 & <=35 | *7

        // preferredBackupWindow is the daily time range for automated backups (UTC).
        // Format: "hh24:mi-hh24:mi" (e.g., "03:00-04:00").
        preferredBackupWindow?: string

        // preferredMaintenanceWindow is the weekly time range for maintenance (UTC).
        // Format: "ddd:hh24:mi-ddd:hh24:mi" (e.g., "sun:05:00-sun:06:00").
        preferredMaintenanceWindow?: string

        // deletionProtection prevents accidental deletion.
        deletionProtection: bool | *false

        // autoMinorVersionUpgrade enables automatic minor version upgrades.
        autoMinorVersionUpgrade: bool | *true

        // monitoringInterval is the Enhanced Monitoring interval in seconds.
        // 0 disables enhanced monitoring. Valid: 0, 1, 5, 10, 15, 30, 60.
        monitoringInterval: 0 | 1 | 5 | 10 | 15 | 30 | 60 | *0

        // monitoringRoleArn is the IAM role ARN for Enhanced Monitoring.
        // Required when monitoringInterval > 0.
        monitoringRoleArn?: string

        // performanceInsightsEnabled enables Performance Insights.
        performanceInsightsEnabled: bool | *false

        // tags applied to the DB instance.
        tags: [string]: string
    }

    outputs?: {
        dbIdentifier:  string
        dbiResourceId: string
        arn:           string
        endpoint:      string
        port:          int
        engine:        string
        engineVersion: string
        status:        string
    }
}
```

### Key Design Decisions

- **`metadata.name` IS the DB identifier**: The `metadata.name` field maps directly
  to the RDS DB instance identifier. RDS identifiers are user-chosen, unique per
  region, and serve as both the logical identity and the AWS lookup key.

- **`masterUserPassword` is write-only**: AWS never returns the password via
  `DescribeDBInstances`. The driver stores it in desired state for re-provision
  convergence but drift detection skips this field entirely.

- **`allocatedStorage` is optional**: Aurora cluster instances inherit storage from
  the cluster. For non-Aurora instances it is required — CUE validation enforces
  this via a conditional constraint.

- **`storageType` defaults to `gp3`**: General Purpose SSD (gp3) is the modern
  default, offering better baseline performance than gp2 at similar cost.

- **`storageEncrypted` defaults to `true`**: Encryption at rest is an opinionated
  best practice default, matching the S3 driver's encryption-by-default pattern.

- **`deletionProtection` defaults to `false`**: Unlike production recommendations,
  this simplifies development and testing. Users should enable it explicitly for
  production instances in their templates.

---

## Step 2 — AWS Client Factory

**File**: `internal/infra/awsclient/client.go` — **NEEDS NEW RDS CLIENT FACTORY**

```go
func NewRDSClient(cfg aws.Config) *rds.Client {
    return rds.NewFromConfig(cfg)
}
```

This requires adding `github.com/aws/aws-sdk-go-v2/service/rds` to `go.mod`.

---

## Step 3 — Driver Types

**File**: `internal/drivers/rdsinstance/types.go`

```go
package rdsinstance

import "github.com/shirvan/praxis/pkg/types"

const ServiceName = "RDSInstance"

type RDSInstanceSpec struct {
    Account                    string            `json:"account,omitempty"`
    Region                     string            `json:"region"`
    DBIdentifier               string            `json:"dbIdentifier"`
    Engine                     string            `json:"engine"`
    EngineVersion              string            `json:"engineVersion"`
    InstanceClass              string            `json:"instanceClass"`
    AllocatedStorage           int32             `json:"allocatedStorage,omitempty"`
    StorageType                string            `json:"storageType"`
    IOPS                       int32             `json:"iops,omitempty"`
    StorageThroughput          int32             `json:"storageThroughput,omitempty"`
    StorageEncrypted           bool              `json:"storageEncrypted"`
    KMSKeyId                   string            `json:"kmsKeyId,omitempty"`
    MasterUsername             string            `json:"masterUsername,omitempty"`
    MasterUserPassword         string            `json:"masterUserPassword,omitempty"`
    DBSubnetGroupName          string            `json:"dbSubnetGroupName,omitempty"`
    ParameterGroupName         string            `json:"parameterGroupName,omitempty"`
    VpcSecurityGroupIds        []string          `json:"vpcSecurityGroupIds,omitempty"`
    DBClusterIdentifier        string            `json:"dbClusterIdentifier,omitempty"`
    MultiAZ                    bool              `json:"multiAZ"`
    PubliclyAccessible         bool              `json:"publiclyAccessible"`
    BackupRetentionPeriod      int32             `json:"backupRetentionPeriod"`
    PreferredBackupWindow      string            `json:"preferredBackupWindow,omitempty"`
    PreferredMaintenanceWindow string            `json:"preferredMaintenanceWindow,omitempty"`
    DeletionProtection         bool              `json:"deletionProtection"`
    AutoMinorVersionUpgrade    bool              `json:"autoMinorVersionUpgrade"`
    MonitoringInterval         int32             `json:"monitoringInterval"`
    MonitoringRoleArn          string            `json:"monitoringRoleArn,omitempty"`
    PerformanceInsightsEnabled bool              `json:"performanceInsightsEnabled"`
    Tags                       map[string]string `json:"tags,omitempty"`
}

type RDSInstanceOutputs struct {
    DBIdentifier  string `json:"dbIdentifier"`
    DbiResourceId string `json:"dbiResourceId"`
    ARN           string `json:"arn"`
    Endpoint      string `json:"endpoint"`
    Port          int32  `json:"port"`
    Engine        string `json:"engine"`
    EngineVersion string `json:"engineVersion"`
    Status        string `json:"status"`
}

type ObservedState struct {
    DBIdentifier               string            `json:"dbIdentifier"`
    DbiResourceId              string            `json:"dbiResourceId"`
    ARN                        string            `json:"arn"`
    Engine                     string            `json:"engine"`
    EngineVersion              string            `json:"engineVersion"`
    InstanceClass              string            `json:"instanceClass"`
    AllocatedStorage           int32             `json:"allocatedStorage"`
    StorageType                string            `json:"storageType"`
    IOPS                       int32             `json:"iops"`
    StorageThroughput          int32             `json:"storageThroughput"`
    StorageEncrypted           bool              `json:"storageEncrypted"`
    KMSKeyId                   string            `json:"kmsKeyId"`
    MasterUsername             string            `json:"masterUsername"`
    DBSubnetGroupName          string            `json:"dbSubnetGroupName"`
    ParameterGroupName         string            `json:"parameterGroupName"`
    VpcSecurityGroupIds        []string          `json:"vpcSecurityGroupIds"`
    DBClusterIdentifier        string            `json:"dbClusterIdentifier"`
    MultiAZ                    bool              `json:"multiAZ"`
    PubliclyAccessible         bool              `json:"publiclyAccessible"`
    BackupRetentionPeriod      int32             `json:"backupRetentionPeriod"`
    PreferredBackupWindow      string            `json:"preferredBackupWindow"`
    PreferredMaintenanceWindow string            `json:"preferredMaintenanceWindow"`
    DeletionProtection         bool              `json:"deletionProtection"`
    AutoMinorVersionUpgrade    bool              `json:"autoMinorVersionUpgrade"`
    MonitoringInterval         int32             `json:"monitoringInterval"`
    MonitoringRoleArn          string            `json:"monitoringRoleArn"`
    PerformanceInsightsEnabled bool              `json:"performanceInsightsEnabled"`
    Endpoint                   string            `json:"endpoint"`
    Port                       int32             `json:"port"`
    Status                     string            `json:"status"`
    Tags                       map[string]string `json:"tags"`
}

type RDSInstanceState struct {
    Desired            RDSInstanceSpec      `json:"desired"`
    Observed           ObservedState        `json:"observed"`
    Outputs            RDSInstanceOutputs   `json:"outputs"`
    Status             types.ResourceStatus `json:"status"`
    Mode               types.Mode           `json:"mode"`
    Error              string               `json:"error,omitempty"`
    Generation         int64                `json:"generation"`
    LastReconcile      string               `json:"lastReconcile,omitempty"`
    ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
```

### Key Type Decisions

- **`MasterUserPassword` in spec only**: It is stored in the desired spec for
  re-provision convergence but is never present in observed state (AWS does not
  return it). Drift detection skips this field.
- **`AllocatedStorage` as `int32`**: Matches the AWS SDK type. Grow-only constraint
  is enforced in the driver (not in the type).
- **`VpcSecurityGroupIds` as sorted slice**: Sorted before comparison in drift
  detection. AWS returns SG IDs in arbitrary order.

---

## Step 4 — AWS API Abstraction Layer

**File**: `internal/drivers/rdsinstance/aws.go`

### RDSInstanceAPI Interface

```go
type RDSInstanceAPI interface {
    // CreateDBInstance creates a new RDS DB instance.
    CreateDBInstance(ctx context.Context, spec RDSInstanceSpec) (string, error)

    // DescribeDBInstance returns the observed state of a DB instance.
    DescribeDBInstance(ctx context.Context, dbIdentifier string) (ObservedState, error)

    // ModifyDBInstance modifies mutable attributes of a DB instance.
    // Returns the pending modifications summary.
    ModifyDBInstance(ctx context.Context, spec RDSInstanceSpec, applyImmediately bool) error

    // DeleteDBInstance deletes a DB instance.
    // skipFinalSnapshot controls whether a final snapshot is created.
    DeleteDBInstance(ctx context.Context, dbIdentifier string, skipFinalSnapshot bool) error

    // RebootDBInstance triggers an immediate reboot (e.g., to apply pending parameter changes).
    RebootDBInstance(ctx context.Context, dbIdentifier string) error

    // WaitUntilAvailable blocks until the DB instance reaches "available" status.
    WaitUntilAvailable(ctx context.Context, dbIdentifier string) error

    // WaitUntilDeleted blocks until the DB instance is fully deleted.
    WaitUntilDeleted(ctx context.Context, dbIdentifier string) error

    // UpdateTags replaces all tags on the DB instance (by ARN).
    UpdateTags(ctx context.Context, arn string, tags map[string]string) error

    // ListTags returns all tags on the DB instance (by ARN).
    ListTags(ctx context.Context, arn string) (map[string]string, error)
}
```

### realRDSInstanceAPI Implementation

```go
type realRDSInstanceAPI struct {
    client  *rds.Client
    limiter *ratelimit.Limiter
}

func NewRDSInstanceAPI(client *rds.Client) RDSInstanceAPI {
    return &realRDSInstanceAPI{
        client:  client,
        limiter: ratelimit.New("rds", 15, 8),
    }
}
```

### Key Implementation Details

#### `CreateDBInstance`

```go
func (r *realRDSInstanceAPI) CreateDBInstance(ctx context.Context, spec RDSInstanceSpec) (string, error) {
    input := &rds.CreateDBInstanceInput{
        DBInstanceIdentifier: aws.String(spec.DBIdentifier),
        DBInstanceClass:      aws.String(spec.InstanceClass),
        Engine:               aws.String(spec.Engine),
        EngineVersion:        aws.String(spec.EngineVersion),
    }

    // Non-Aurora fields
    if spec.DBClusterIdentifier == "" {
        input.AllocatedStorage      = aws.Int32(spec.AllocatedStorage)
        input.MasterUsername        = aws.String(spec.MasterUsername)
        input.MasterUserPassword    = aws.String(spec.MasterUserPassword)
        input.BackupRetentionPeriod = aws.Int32(spec.BackupRetentionPeriod)
        input.MultiAZ               = aws.Bool(spec.MultiAZ)
        input.StorageEncrypted      = aws.Bool(spec.StorageEncrypted)

        if spec.StorageType != "" {
            input.StorageType = aws.String(spec.StorageType)
        }
        if spec.IOPS > 0 {
            input.Iops = aws.Int32(spec.IOPS)
        }
        if spec.StorageThroughput > 0 {
            input.StorageThroughput = aws.Int32(spec.StorageThroughput)
        }
        if spec.KMSKeyId != "" {
            input.KmsKeyId = aws.String(spec.KMSKeyId)
        }
        if spec.DBSubnetGroupName != "" {
            input.DBSubnetGroupName = aws.String(spec.DBSubnetGroupName)
        }
    } else {
        // Aurora cluster instance — most fields come from the cluster
        input.DBClusterIdentifier = aws.String(spec.DBClusterIdentifier)
    }

    if spec.ParameterGroupName != "" {
        input.DBParameterGroupName = aws.String(spec.ParameterGroupName)
    }
    if len(spec.VpcSecurityGroupIds) > 0 {
        input.VpcSecurityGroupIds = spec.VpcSecurityGroupIds
    }

    input.PubliclyAccessible        = aws.Bool(spec.PubliclyAccessible)
    input.DeletionProtection        = aws.Bool(spec.DeletionProtection)
    input.AutoMinorVersionUpgrade   = aws.Bool(spec.AutoMinorVersionUpgrade)
    input.MonitoringInterval        = aws.Int32(spec.MonitoringInterval)
    input.EnablePerformanceInsights  = aws.Bool(spec.PerformanceInsightsEnabled)

    if spec.MonitoringRoleArn != "" {
        input.MonitoringRoleArn = aws.String(spec.MonitoringRoleArn)
    }
    if spec.PreferredBackupWindow != "" {
        input.PreferredBackupWindow = aws.String(spec.PreferredBackupWindow)
    }
    if spec.PreferredMaintenanceWindow != "" {
        input.PreferredMaintenanceWindow = aws.String(spec.PreferredMaintenanceWindow)
    }

    // Tags at creation
    if len(spec.Tags) > 0 {
        input.Tags = toRDSTags(spec.Tags)
    }

    out, err := r.client.CreateDBInstance(ctx, input)
    if err != nil {
        return "", err
    }
    return aws.ToString(out.DBInstance.DbiResourceId), nil
}
```

#### `DescribeDBInstance`

```go
func (r *realRDSInstanceAPI) DescribeDBInstance(ctx context.Context, dbIdentifier string) (ObservedState, error) {
    out, err := r.client.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{
        DBInstanceIdentifier: aws.String(dbIdentifier),
    })
    if err != nil {
        return ObservedState{}, err
    }
    if len(out.DBInstances) == 0 {
        return ObservedState{}, fmt.Errorf("DB instance %s not found", dbIdentifier)
    }
    inst := out.DBInstances[0]

    obs := ObservedState{
        DBIdentifier:               aws.ToString(inst.DBInstanceIdentifier),
        DbiResourceId:              aws.ToString(inst.DbiResourceId),
        ARN:                        aws.ToString(inst.DBInstanceArn),
        Engine:                     aws.ToString(inst.Engine),
        EngineVersion:              aws.ToString(inst.EngineVersion),
        InstanceClass:              aws.ToString(inst.DBInstanceClass),
        AllocatedStorage:           aws.ToInt32(inst.AllocatedStorage),
        StorageType:                aws.ToString(inst.StorageType),
        IOPS:                       aws.ToInt32(inst.Iops),
        StorageThroughput:          aws.ToInt32(inst.StorageThroughput),
        StorageEncrypted:           aws.ToBool(inst.StorageEncrypted),
        KMSKeyId:                   aws.ToString(inst.KmsKeyId),
        MasterUsername:             aws.ToString(inst.MasterUsername),
        MultiAZ:                    aws.ToBool(inst.MultiAZ),
        PubliclyAccessible:         aws.ToBool(inst.PubliclyAccessible),
        BackupRetentionPeriod:      aws.ToInt32(inst.BackupRetentionPeriod),
        PreferredBackupWindow:      aws.ToString(inst.PreferredBackupWindow),
        PreferredMaintenanceWindow: aws.ToString(inst.PreferredMaintenanceWindow),
        DeletionProtection:         aws.ToBool(inst.DeletionProtection),
        AutoMinorVersionUpgrade:    aws.ToBool(inst.AutoMinorVersionUpgrade),
        MonitoringInterval:         aws.ToInt32(inst.MonitoringInterval),
        PerformanceInsightsEnabled: aws.ToBool(inst.PerformanceInsightsEnabled),
        Status:                     aws.ToString(inst.DBInstanceStatus),
    }

    // Subnet group
    if inst.DBSubnetGroup != nil {
        obs.DBSubnetGroupName = aws.ToString(inst.DBSubnetGroup.DBSubnetGroupName)
    }

    // Parameter group
    if len(inst.DBParameterGroups) > 0 {
        obs.ParameterGroupName = aws.ToString(inst.DBParameterGroups[0].DBParameterGroupName)
    }

    // Security groups
    for _, sg := range inst.VpcSecurityGroups {
        obs.VpcSecurityGroupIds = append(obs.VpcSecurityGroupIds, aws.ToString(sg.VpcSecurityGroupId))
    }

    // Cluster association
    if inst.DBClusterIdentifier != nil {
        obs.DBClusterIdentifier = aws.ToString(inst.DBClusterIdentifier)
    }

    // Monitoring role
    if inst.MonitoringRoleArn != nil {
        obs.MonitoringRoleArn = aws.ToString(inst.MonitoringRoleArn)
    }

    // Endpoint
    if inst.Endpoint != nil {
        obs.Endpoint = aws.ToString(inst.Endpoint.Address)
        obs.Port = aws.ToInt32(inst.Endpoint.Port)
    }

    return obs, nil
}
```

#### `UpdateTags`

RDS uses ARN-based tagging, not resource-ID-based:

```go
func (r *realRDSInstanceAPI) UpdateTags(ctx context.Context, arn string, tags map[string]string) error {
    // 1. List current tags
    currentTags, err := r.ListTags(ctx, arn)
    if err != nil {
        return err
    }

    // 2. Remove all existing tags
    if len(currentTags) > 0 {
        keys := make([]string, 0, len(currentTags))
        for k := range currentTags {
            keys = append(keys, k)
        }
        _, err = r.client.RemoveTagsFromResource(ctx, &rds.RemoveTagsFromResourceInput{
            ResourceName: aws.String(arn),
            TagKeys:      keys,
        })
        if err != nil {
            return err
        }
    }

    // 3. Apply new tags
    if len(tags) > 0 {
        _, err = r.client.AddTagsToResource(ctx, &rds.AddTagsToResourceInput{
            ResourceName: aws.String(arn),
            Tags:         toRDSTags(tags),
        })
        if err != nil {
            return err
        }
    }
    return nil
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
        return apiErr.ErrorCode() == "DBInstanceNotFound" ||
               apiErr.ErrorCode() == "DBInstanceNotFoundFault"
    }
    return strings.Contains(err.Error(), "DBInstanceNotFound")
}

func IsAlreadyExists(err error) bool {
    if err == nil {
        return false
    }
    var apiErr smithy.APIError
    if errors.As(err, &apiErr) {
        return apiErr.ErrorCode() == "DBInstanceAlreadyExists" ||
               apiErr.ErrorCode() == "DBInstanceAlreadyExistsFault"
    }
    return strings.Contains(err.Error(), "DBInstanceAlreadyExists")
}

func IsInvalidState(err error) bool {
    if err == nil {
        return false
    }
    var apiErr smithy.APIError
    if errors.As(err, &apiErr) {
        return apiErr.ErrorCode() == "InvalidDBInstanceState" ||
               apiErr.ErrorCode() == "InvalidDBInstanceStateFault"
    }
    return strings.Contains(err.Error(), "InvalidDBInstanceState")
}

func IsStorageQuotaExceeded(err error) bool {
    if err == nil {
        return false
    }
    var apiErr smithy.APIError
    if errors.As(err, &apiErr) {
        return apiErr.ErrorCode() == "StorageQuotaExceeded" ||
               apiErr.ErrorCode() == "StorageQuotaExceededFault"
    }
    return strings.Contains(err.Error(), "StorageQuotaExceeded")
}

func IsInsufficientDBInstanceCapacity(err error) bool {
    if err == nil {
        return false
    }
    var apiErr smithy.APIError
    if errors.As(err, &apiErr) {
        return apiErr.ErrorCode() == "InsufficientDBInstanceCapacity"
    }
    return strings.Contains(err.Error(), "InsufficientDBInstanceCapacity")
}
```

### Helper Functions

```go
func toRDSTags(tags map[string]string) []rdstypes.Tag {
    out := make([]rdstypes.Tag, 0, len(tags))
    for k, v := range tags {
        out = append(out, rdstypes.Tag{Key: aws.String(k), Value: aws.String(v)})
    }
    return out
}

func fromRDSTags(tags []rdstypes.Tag) map[string]string {
    out := make(map[string]string, len(tags))
    for _, t := range tags {
        out[aws.ToString(t.Key)] = aws.ToString(t.Value)
    }
    return out
}
```

---

## Step 5 — Drift Detection

**File**: `internal/drivers/rdsinstance/drift.go`

### Core Functions

**`HasDrift(desired RDSInstanceSpec, observed ObservedState) bool`**

```go
func HasDrift(desired RDSInstanceSpec, observed ObservedState) bool {
    if desired.InstanceClass != observed.InstanceClass {
        return true
    }
    if desired.EngineVersion != observed.EngineVersion {
        return true
    }
    // Storage can only grow — drift only if observed < desired
    if desired.AllocatedStorage > 0 && desired.AllocatedStorage > observed.AllocatedStorage {
        return true
    }
    if desired.StorageType != observed.StorageType {
        return true
    }
    if desired.IOPS > 0 && desired.IOPS != observed.IOPS {
        return true
    }
    if desired.StorageThroughput > 0 && desired.StorageThroughput != observed.StorageThroughput {
        return true
    }
    if desired.MultiAZ != observed.MultiAZ {
        return true
    }
    if desired.PubliclyAccessible != observed.PubliclyAccessible {
        return true
    }
    if desired.BackupRetentionPeriod != observed.BackupRetentionPeriod {
        return true
    }
    if desired.PreferredBackupWindow != "" &&
       desired.PreferredBackupWindow != observed.PreferredBackupWindow {
        return true
    }
    if desired.PreferredMaintenanceWindow != "" &&
       desired.PreferredMaintenanceWindow != observed.PreferredMaintenanceWindow {
        return true
    }
    if desired.DeletionProtection != observed.DeletionProtection {
        return true
    }
    if desired.AutoMinorVersionUpgrade != observed.AutoMinorVersionUpgrade {
        return true
    }
    if desired.MonitoringInterval != observed.MonitoringInterval {
        return true
    }
    if desired.PerformanceInsightsEnabled != observed.PerformanceInsightsEnabled {
        return true
    }
    if desired.ParameterGroupName != "" && desired.ParameterGroupName != observed.ParameterGroupName {
        return true
    }
    if !securityGroupIdsEqual(desired.VpcSecurityGroupIds, observed.VpcSecurityGroupIds) {
        return true
    }
    // masterUserPassword is write-only — cannot detect drift
    return !tagsMatch(desired.Tags, observed.Tags)
}
```

### Skip-When-Empty Pattern

Several fields where AWS assigns defaults are skipped during drift comparison
when the desired value is empty:

| Field | AWS Default | Guard |
|---|---|---|
| `preferredBackupWindow` | Auto-assigned (e.g. `"07:27-07:57"`) | Skip if desired is `""` |
| `preferredMaintenanceWindow` | Auto-assigned (e.g. `"fri:08:42-fri:09:12"`) | Skip if desired is `""` |
| `kmsKeyId` | AWS-managed key ARN | Skip if desired is `""` |
| `parameterGroupName` | `"default.aurora-postgresql*"` | Skip if desired is `""` |

### Immutable Fields

Immutable fields (`engine`, `masterUsername`) are not compared in `HasDrift`.
In `ComputeFieldDiffs`, they are reported with an "(immutable)" suffix for
informational purposes only, and only when the desired value is non-empty.

**`ComputeFieldDiffs(desired RDSInstanceSpec, observed ObservedState) []FieldDiffEntry`**

Produces human-readable diffs for the plan renderer:

- Immutable fields: `engine`, `masterUsername`, `region` — reported with
  "(immutable, ignored)" suffix, only when desired is non-empty.
- Mutable fields: `instanceClass`, `engineVersion`, `allocatedStorage`, `storageType`,
  `iops`, `multiAZ`, `publiclyAccessible`, `backupRetentionPeriod`,
  `deletionProtection`, `parameterGroupName`, `vpcSecurityGroupIds`, `tags`.
- Write-only fields: `masterUserPassword` — reported as "(write-only, drift not
  detectable)" if changed in desired spec.

### Security Group ID Comparison

```go
func securityGroupIdsEqual(desired, observed []string) bool {
    if len(desired) != len(observed) {
        return false
    }
    dSet := make(map[string]bool, len(desired))
    for _, id := range desired {
        dSet[id] = true
    }
    for _, id := range observed {
        if !dSet[id] {
            return false
        }
    }
    return true
}
```

### Storage Grow-Only Constraint

Drift detection for `allocatedStorage` only reports drift when the desired value is
**larger** than the observed value. AWS does not support shrinking RDS storage.
If the desired value is smaller than observed, it is silently ignored (no diff
reported). The `ComputeFieldDiffs` function notes this constraint:

```go
if desired.AllocatedStorage < observed.AllocatedStorage {
    diffs = append(diffs, FieldDiffEntry{
        Field:    "allocatedStorage",
        Desired:  fmt.Sprintf("%d GiB", desired.AllocatedStorage),
        Observed: fmt.Sprintf("%d GiB", observed.AllocatedStorage),
        Note:     "(grow-only, shrink ignored)",
    })
}
```

---

## Step 6 — Driver Implementation

**File**: `internal/drivers/rdsinstance/driver.go`

### Service Registration

```go
const ServiceName = "RDSInstance"
```

### Constructor Pattern

```go
func NewRDSInstanceDriver(auth authservice.AuthClient) *RDSInstanceDriver
func NewRDSInstanceDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) RDSInstanceAPI) *RDSInstanceDriver
```

### Provision Handler

1. **Input validation**: `dbIdentifier`, `engine`, `engineVersion`, `instanceClass`
   must be non-empty. For non-Aurora instances: `allocatedStorage`, `masterUsername`,
   `masterUserPassword` must be set. Returns `TerminalError(400)` on failure.

2. **Load current state**: Reads `RDSInstanceState` from Restate K/V. Sets status
   to `Provisioning`, increments generation.

3. **Re-provision check**: If `state.Outputs.DBIdentifier` is non-empty, describes
   the instance. If deleted externally (404), clears outputs and falls through
   to creation.

4. **Create instance**: Calls `api.CreateDBInstance`. Classifies errors inside
   `restate.Run()`:
   - `IsAlreadyExists` → `TerminalError(409)`
   - `IsStorageQuotaExceeded` → `TerminalError(413)`
   - `IsInsufficientDBInstanceCapacity` → `TerminalError(503)`

5. **Wait for available**: Calls `api.WaitUntilAvailable` wrapped in `restate.Run`.
   This durably journals the waiter result. On restart, the journaled result is
   replayed without re-waiting.

6. **Converge mutable attributes** (re-provision path):
   - Instance class change: `ModifyDBInstance` with `ApplyImmediately=true`.
   - Engine version upgrade: `ModifyDBInstance` with `ApplyImmediately` based on
     whether it's a minor or major version change.
   - Storage scaling: Only if desired > observed (grow-only).
   - Other attributes: `ModifyDBInstance` batch call.
   - Tags: `UpdateTags` with ARN.

7. **Describe final state**: Calls `api.DescribeDBInstance` to populate observed state.

8. **Commit state**: Sets status to `Ready`, saves state atomically, schedules
   reconciliation.

### Import Handler

1. Describes the instance by `ref.ResourceID` (the DB identifier).
2. Synthesizes an `RDSInstanceSpec` from the observed state via `specFromObserved()`.
3. Fetches tags via `api.ListTags(arn)` and includes them.
4. Sets mode to `ModeObserved` (RDS instances are high-value resources).
5. Schedules reconciliation.

### Delete Handler

1. Sets status to `Deleting`.
2. **Disable deletion protection**: If observed `deletionProtection` is true,
   modifies to disable it first. Waits for available.
3. **Delete instance**: Calls `api.DeleteDBInstance` with `skipFinalSnapshot=true`
   (Praxis doesn't manage snapshots).
4. **Wait for deletion**: Calls `api.WaitUntilDeleted`.
5. **Error classification**:
   - `IsNotFound` → silent success (already gone).
   - `IsInvalidState` → `TerminalError(409)` with message about instance state.
6. Sets status to `StatusDeleted`.

### Reconcile Handler

Standard reconcile pattern on a 5-minute timer:

1. Clears `ReconcileScheduled` flag.
2. Skips if status is not `Ready` or `Error`.
3. Describes current AWS state.
4. **Managed + drift**: Corrects mutable attributes via `ModifyDBInstance` and
   `UpdateTags`.
5. **Observed + drift**: Reports only.
6. Re-schedules.

### GetStatus / GetOutputs

Standard shared handlers — read state and return projections.

---

## Step 7 — Provider Adapter

**File**: `internal/core/provider/rdsinstance_adapter.go`

```go
type RDSInstanceAdapter struct {
    auth              authservice.AuthClient
    staticPlanningAPI rdsinstance.RDSInstanceAPI
    apiFactory        func(aws.Config) rdsinstance.RDSInstanceAPI
}
```

### Methods

**`Scope() KeyScope`** → `KeyScopeRegion`

**`Kind() string`** → `"RDSInstance"`

**`ServiceName() string`** → `"RDSInstance"`

**`BuildKey(resourceDoc json.RawMessage) (string, error)`**:
Decodes the resource document, extracts `spec.region` and `metadata.name`.
Returns `region~metadata.name`.

**`BuildImportKey(region, resourceID string) (string, error)`**:
Returns `region~resourceID` (DB identifier).

**`Plan(ctx, key, account, desiredSpec) (DiffOperation, []FieldDiff, error)`**:
Calls `api.DescribeDBInstance(dbIdentifier)`. If not found → `OpCreate`.
If found → `ComputeFieldDiffs()`. If no diffs → `OpNoOp`. If diffs → `OpUpdate`.

---

## Step 8 — Registry Integration

**File**: `internal/core/provider/registry.go` (modified)

Add `NewRDSInstanceAdapterWithRegistry(auth)` to `NewRegistry()`.

---

## Step 9 — Storage Driver Pack Entry Point

**File**: `cmd/praxis-storage/main.go`

The RDS Instance driver is registered in the `praxis-storage` pack alongside
other storage drivers (S3, EBS, SNS, SQS, etc.).

```go
auth := authservice.NewAuthClient()

srv := server.NewRestate().
    // ... other storage drivers ...
    Bind(restate.Reflect(rdsinstance.NewRDSInstanceDriver(auth)))
```

---

## Step 10 — Docker Compose & Justfile

Part of the `praxis-storage` service (port 9081). No additional configuration needed.

### Justfile Targets

| Target | Command |
|---|---|
| `test-rdsinstance` | `go test ./internal/drivers/rdsinstance/... -v -count=1 -race` |
| `test-rdsinstance-integration` | `go test ./tests/integration/ -run TestRDSInstance -v -count=1 -tags=integration -timeout=15m` |

---

## Step 11 — Unit Tests

### `internal/drivers/rdsinstance/drift_test.go`

| Test | Purpose |
|---|---|
| `TestHasDrift_NoDrift` | Matching state → no drift |
| `TestHasDrift_InstanceClassDrift` | Instance class change → drift |
| `TestHasDrift_EngineVersionDrift` | Engine version change → drift |
| `TestHasDrift_AllocatedStorageGrow` | Desired > observed → drift |
| `TestHasDrift_AllocatedStorageShrinkIgnored` | Desired < observed → no drift |
| `TestHasDrift_StorageTypeDrift` | Storage type change → drift |
| `TestHasDrift_IOPSDrift` | IOPS change → drift |
| `TestHasDrift_MultiAZDrift` | Multi-AZ toggle → drift |
| `TestHasDrift_PubliclyAccessibleDrift` | Public access toggle → drift |
| `TestHasDrift_BackupRetentionDrift` | Backup retention change → drift |
| `TestHasDrift_DeletionProtectionDrift` | Deletion protection toggle → drift |
| `TestHasDrift_ParameterGroupDrift` | Parameter group change → drift |
| `TestHasDrift_SecurityGroupDrift` | SG ID list change → drift |
| `TestHasDrift_SecurityGroupOrderIndependent` | Same SG IDs, different order → no drift |
| `TestHasDrift_TagDrift` | Tag change → drift |
| `TestHasDrift_PasswordNotDetectable` | Password change → no drift (write-only) |
| `TestComputeFieldDiffs_ImmutableEngine` | Reports engine change as "(immutable, ignored)" |
| `TestComputeFieldDiffs_GrowOnlyStorage` | Reports shrink attempt as "(grow-only, shrink ignored)" |

### `internal/drivers/rdsinstance/aws_test.go`

| Test | Purpose |
|---|---|
| `TestIsNotFound_DBInstanceNotFound` | RDS not-found error → true |
| `TestIsNotFound_OtherError` | Other error → false |
| `TestIsAlreadyExists_DBInstanceAlreadyExists` | Duplicate identifier → true |
| `TestIsInvalidState_True` | Invalid state error → true |
| `TestIsStorageQuotaExceeded_True` | Storage quota → true |
| `TestIsNotFound_WrappedRestateError` | String fallback for Restate-wrapped errors |

### `internal/drivers/rdsinstance/driver_test.go`

| Test | Purpose |
|---|---|
| `TestSpecFromObserved_RoundTrip` | Observed → spec preserves all fields except password |
| `TestSpecFromObserved_AuroraClusterInstance` | Aurora instance → spec has dbClusterIdentifier, no storage |
| `TestServiceName` | Returns "RDSInstance" |
| `TestOutputsFromObserved` | Correct output mapping |

### `internal/core/provider/rdsinstance_adapter_test.go`

| Test | Purpose |
|---|---|
| `TestRDSInstanceAdapter_BuildKey` | Returns `region~dbIdentifier` |
| `TestRDSInstanceAdapter_BuildImportKey` | Returns `region~dbIdentifier` |
| `TestRDSInstanceAdapter_Kind` | Returns "RDSInstance" |
| `TestRDSInstanceAdapter_Scope` | Returns `KeyScopeRegion` |

---

## Step 12 — Integration Tests

**File**: `tests/integration/rdsinstance_driver_test.go`

Integration tests run against Testcontainers (Restate) + Moto (RDS).

### Test Cases

| Test | Description |
|---|---|
| `TestRDSInstanceProvision_CreatesInstance` | Creates a PostgreSQL instance with subnet group, parameter group, SG, and tags. Verifies the instance exists in Moto via `DescribeDBInstances`. |
| `TestRDSInstanceProvision_Idempotent` | Provisions the same spec twice on the same key. Verifies same DbiResourceId (no duplicate). |
| `TestRDSInstanceProvision_UpdateInstanceClass` | Re-provisions with changed instance class. Verifies the modification is applied. |
| `TestRDSInstanceProvision_ScaleStorage` | Re-provisions with larger storage. Verifies grow-only semantics. |
| `TestRDSInstanceProvision_ToggleMultiAZ` | Re-provisions toggling multi-AZ. Verifies the change. |
| `TestRDSInstanceImport_ExistingInstance` | Creates an instance directly via RDS API, then imports via the driver. Verifies Observed mode. |
| `TestRDSInstanceDelete_RemovesInstance` | Provisions, then deletes. Verifies deletion (skips final snapshot). |
| `TestRDSInstanceDelete_DeletionProtection` | Provisions with deletion protection, then deletes. Verifies protection is disabled automatically before deletion. |
| `TestRDSInstanceReconcile_DetectsDrift` | Provisions, then modifies instance class directly via RDS API. Triggers reconcile, verifies drift detected and corrected. |
| `TestRDSInstanceGetStatus_ReturnsReady` | Provisions and checks `GetStatus` returns `Ready`, `Managed`, generation > 0. |
| `TestRDSInstanceProvision_AuroraClusterInstance` | Creates an Aurora cluster instance (with `dbClusterIdentifier`). Verifies storage fields are not set. |

---

## RDS-Instance-Specific Design Decisions

### 1. Long Provisioning Times and Durable Waiters

RDS DB instances take 5–15 minutes to create. The driver uses RDS SDK waiters
wrapped in `restate.Run()` for durable journaling. If the process restarts during
the wait, Restate replays the journaled result without re-polling AWS. This is
critical for RDS because a 15-minute create followed by a restart should not
trigger a second `CreateDBInstance` call.

### 2. Storage Grow-Only Constraint

AWS does not support shrinking RDS instance storage. The driver enforces this:

- During drift detection: only reports drift when desired > observed.
- During convergence: only calls `ModifyDBInstance` for storage when desired > observed.
- During plan: reports shrink attempts with "(grow-only, shrink ignored)" note.

### 3. Password is Write-Only

`masterUserPassword` is set at creation and can be changed via `ModifyDBInstance`,
but AWS never returns it via `DescribeDBInstances`. Drift detection cannot detect
password changes made outside Praxis. The driver stores the password in desired
state for re-provision convergence only. `ComputeFieldDiffs` reports password
fields as "(write-only, drift not detectable)".

### 4. Aurora Cluster Instance Mode

When `dbClusterIdentifier` is set, the RDS instance is a cluster member:

- Storage fields (`allocatedStorage`, `storageType`, `iops`) are inherited from
  the cluster and not set on the instance.
- `masterUsername` and `masterUserPassword` are inherited from the cluster.
- `multiAZ` is not applicable (Aurora handles replication at the cluster level).
- `dbSubnetGroupName` is inherited from the cluster.

The CUE schema and type validations handle this dual mode.

### 5. Deletion Protection Auto-Disable

When the Delete handler is called and the instance has `deletionProtection=true`,
the driver automatically disables it before deleting. This is a convenience:
the user's intent to delete is clear (they called Delete), so Praxis handles
the pre-requisite automatically rather than returning a 409.

### 6. Skip Final Snapshot by Default

Praxis does not manage RDS snapshots. The Delete handler always sets
`SkipFinalSnapshot=true`. Users who need final snapshots before deletion should
manage this out-of-band or via a future snapshot driver.

### 7. Apply Immediately for Modifications

The driver defaults to `ApplyImmediately=true` for `ModifyDBInstance` calls.
This ensures modifications take effect promptly rather than being deferred to the
next maintenance window. For production systems where maintenance-window-only
changes are desired, a future `applyImmediately: false` spec field could be added.

### 8. Import Defaults to ModeObserved

RDS instances are high-value, stateful resources. Importing defaults to
`ModeObserved` to prevent accidental modification. Users must explicitly switch
to `ModeManaged` if they want Praxis to converge the instance's state.

### 9. No Ownership Tags

RDS DB identifiers are unique per region per account. `CreateDBInstance` returns
`DBInstanceAlreadyExists` for duplicates. This natural conflict signal eliminates
the need for `praxis:managed-key` ownership tags. The plan adapter uses
`DescribeDBInstances` by identifier for plan-time resolution (unlike EC2 which
uses state-driven discovery due to mutable Name tags).

---

## Design Decisions (Resolved)

1. **Should read replicas be part of this driver?**
   No. Read replicas have their own lifecycle (promote, cross-region) and are better
   modeled as a separate driver or as a property of a higher-level "RDS Cluster"
   abstraction. This driver manages standalone instances and Aurora cluster instances.

2. **Should the driver support instance renaming?**
   No. RDS supports `ModifyDBInstance` with a new identifier, but this changes the
   AWS identity. Since the identifier is part of the Virtual Object key, renaming
   would require migrating to a new VO — equivalent to delete + recreate.

3. **Should storage autoscaling be supported?**
   Not included initially. `MaxAllocatedStorage` enables RDS storage autoscaling, but it adds
   complexity (observed storage may exceed desired). A future enhancement can add
   `maxAllocatedStorage` to the spec.

4. **Should the driver handle pending modifications?**
   Yes, but minimally. The driver sets `ApplyImmediately=true` by default. For
   operations that AWS applies asynchronously regardless (e.g., storage scaling),
   the driver polls via `WaitUntilAvailable`. The `PendingModifiedValues` field
   in `DescribeDBInstances` output is logged but not exposed in outputs.

5. **Should engine version upgrades be handled differently for major vs minor?**
   Currently, all version changes use `ModifyDBInstance` with `ApplyImmediately=true`.
   Major version upgrades require `AllowMajorVersionUpgrade=true` — the driver sets
   this automatically when the major version component changes. A future enhancement
   could add a `requireApprovalForMajorUpgrade` flag.

---

## Checklist

- [x] **Schema**: `schemas/aws/rds/instance.cue` created
- [x] **Types**: `internal/drivers/rdsinstance/types.go` created
- [x] **AWS client**: `internal/infra/awsclient/client.go` updated with `NewRDSClient`
- [x] **AWS API**: `internal/drivers/rdsinstance/aws.go` created
- [x] **Drift**: `internal/drivers/rdsinstance/drift.go` created
- [x] **Driver**: `internal/drivers/rdsinstance/driver.go` created with all 6 handlers
- [x] **Adapter**: `internal/core/provider/rdsinstance_adapter.go` created
- [x] **Registry**: `internal/core/provider/registry.go` updated
- [x] **Entry point**: `cmd/praxis-storage/main.go` — `.Bind()` call for RDS Instance driver
- [x] **Dockerfile**: `cmd/praxis-storage/Dockerfile` (existing)
- [x] **Docker Compose**: Part of `praxis-storage` service
- [x] **Justfile**: Updated with RDS instance targets
- [x] **Unit tests (drift)**: `internal/drivers/rdsinstance/drift_test.go`
- [x] **Unit tests (aws helpers)**: `internal/drivers/rdsinstance/aws_test.go`
- [x] **Unit tests (driver)**: `internal/drivers/rdsinstance/driver_test.go`
- [x] **Unit tests (adapter)**: `internal/core/provider/rdsinstance_adapter_test.go`
- [x] **Integration tests**: `tests/integration/rdsinstance_driver_test.go`
- [x] **Waiter pattern**: WaitUntilAvailable + WaitUntilDeleted in restate.Run
- [x] **Storage grow-only**: Enforced in drift detection and convergence
- [x] **Password write-only**: Skipped in drift detection
- [x] **Aurora dual mode**: Cluster instance path tested
- [x] **Deletion protection auto-disable**: Tested in integration
- [x] **Import default mode**: `ModeObserved` (high-value resource)
- [x] **Delete mode guard**: Delete handler blocks for ModeObserved (409)
- [x] **go.mod**: `github.com/aws/aws-sdk-go-v2/service/rds` added
- [x] **Build passes**: `go build ./...` succeeds
- [x] **Unit tests pass**: `go test ./internal/drivers/rdsinstance/... -race`
- [x] **Integration tests pass**: `go test ./tests/integration/ -run TestRDSInstance -tags=integration`
