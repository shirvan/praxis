// Package rdsinstance implements the Praxis RDS instance driver as a Restate Virtual Object.
// It manages the full lifecycle of Amazon RDS database instances: provisioning, importing,
// reconcile (drift detection and correction), deletion, and status/output queries.
//
// RDS instances can be standalone (with their own storage) or members of an Aurora cluster
// (where storage is managed by the cluster). The driver handles both modes, adapting
// the CreateDBInstance and ModifyDBInstance calls based on DBClusterIdentifier.
package rdsinstance

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate Virtual Object name for RDS instances.
// This is the user-facing API surface (e.g., curl .../RDSInstance/key/Provision).
const ServiceName = "RDSInstance"

// RDSInstanceSpec is the desired state for an RDS instance.
// Fields map to the #RDSInstance CUE schema in schemas/aws/rds/instance.cue.
// When DBClusterIdentifier is set, storage/backup/master fields are managed by
// the Aurora cluster and should be omitted from the instance spec.
type RDSInstanceSpec struct {
	// Account is the operator-defined AWS account name.
	Account string `json:"account,omitempty"`

	// Region is the AWS region for the instance.
	Region string `json:"region"`

	// DBIdentifier is the unique RDS instance identifier. Immutable after creation.
	DBIdentifier string `json:"dbIdentifier"`

	// Engine is the database engine ("mysql", "postgres", "aurora-mysql", etc.). Immutable.
	Engine string `json:"engine"`

	// EngineVersion is the engine version (e.g., "8.0.35"). Can be upgraded in-place.
	EngineVersion string `json:"engineVersion"`

	// InstanceClass is the compute instance type (e.g., "db.t3.medium").
	InstanceClass string `json:"instanceClass"`

	// AllocatedStorage is the storage size in GiB (standalone instances only).
	// Can only be increased, never decreased.
	AllocatedStorage int32 `json:"allocatedStorage,omitempty"`

	// StorageType is the storage type: "gp2", "gp3", "io1", "io2". Defaults to "gp3".
	StorageType string `json:"storageType"`

	// IOPS is the provisioned IOPS for io1/io2/gp3 storage types.
	IOPS int32 `json:"iops,omitempty"`

	// StorageThroughput is the provisioned throughput in MiB/s for gp3 storage.
	StorageThroughput int32 `json:"storageThroughput,omitempty"`

	// StorageEncrypted controls at-rest encryption. Immutable after creation.
	StorageEncrypted bool `json:"storageEncrypted"`

	// KMSKeyId is the KMS key for encryption. Immutable after creation.
	KMSKeyId string `json:"kmsKeyId,omitempty"`

	// MasterUsername is the admin username. Required for standalone instances. Immutable.
	MasterUsername string `json:"masterUsername,omitempty"`

	// MasterUserPassword is the admin password. Can be rotated via Provision.
	MasterUserPassword string `json:"masterUserPassword,omitempty"`

	// DBSubnetGroupName is the DB subnet group for VPC placement.
	DBSubnetGroupName string `json:"dbSubnetGroupName,omitempty"`

	// ParameterGroupName is the DB parameter group to apply.
	ParameterGroupName string `json:"parameterGroupName,omitempty"`

	// VpcSecurityGroupIds are the VPC security group IDs to attach.
	VpcSecurityGroupIds []string `json:"vpcSecurityGroupIds,omitempty"`

	// DBClusterIdentifier links this instance to an Aurora cluster. Immutable.
	DBClusterIdentifier string `json:"dbClusterIdentifier,omitempty"`

	// MultiAZ enables Multi-AZ deployment for high availability.
	MultiAZ bool `json:"multiAZ"`

	// PubliclyAccessible controls whether the instance has a public IP.
	PubliclyAccessible bool `json:"publiclyAccessible"`

	// BackupRetentionPeriod is the number of days to retain automated backups (0-35).
	// Defaults to 7 for standalone instances.
	BackupRetentionPeriod int32 `json:"backupRetentionPeriod"`

	// PreferredBackupWindow is the daily UTC time window for automated backups.
	PreferredBackupWindow string `json:"preferredBackupWindow,omitempty"`

	// PreferredMaintenanceWindow is the weekly UTC time window for maintenance.
	PreferredMaintenanceWindow string `json:"preferredMaintenanceWindow,omitempty"`

	// DeletionProtection prevents accidental deletion when true.
	// The Delete handler auto-disables this before deleting.
	DeletionProtection bool `json:"deletionProtection"`

	// AutoMinorVersionUpgrade allows AWS to auto-apply minor engine patches.
	AutoMinorVersionUpgrade bool `json:"autoMinorVersionUpgrade"`

	// MonitoringInterval is the Enhanced Monitoring interval in seconds (0, 1, 5, 10, 15, 30, 60).
	// Requires MonitoringRoleArn when > 0.
	MonitoringInterval int32 `json:"monitoringInterval"`

	// MonitoringRoleArn is the IAM role ARN for Enhanced Monitoring.
	MonitoringRoleArn string `json:"monitoringRoleArn,omitempty"`

	// PerformanceInsightsEnabled enables Performance Insights.
	PerformanceInsightsEnabled bool `json:"performanceInsightsEnabled"`

	// Tags are key-value pairs for cost allocation and tracking.
	Tags map[string]string `json:"tags,omitempty"`
}

// RDSInstanceOutputs are produced after provisioning and stored in Restate's K/V store.
// Dependent resources reference these via output expressions.
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

// ObservedState captures the actual AWS-side configuration of an RDS instance
// as returned by rds:DescribeDBInstances. Used for drift comparison.
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

// RDSInstanceState is the single atomic state object stored under drivers.StateKey.
// All fields are written together in one restate.Set() call.
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
