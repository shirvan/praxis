// Package auroracluster implements the Praxis Aurora cluster driver as a Restate Virtual Object.
// It manages the full lifecycle of Amazon Aurora DB clusters: provisioning, importing,
// reconcile (drift detection and correction), deletion, and status/output queries.
//
// Aurora clusters are the storage layer for Aurora — they own the distributed storage volume,
// replication, backups, and encryption. Individual RDS instances (managed by the rdsinstance
// driver) connect to the cluster as readers/writers.
package auroracluster

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate Virtual Object name for Aurora clusters.
const ServiceName = "AuroraCluster"

// AuroraClusterSpec is the desired state for an Aurora cluster.
// Fields map to the #AuroraCluster CUE schema in schemas/aws/rds/aurora.cue.
type AuroraClusterSpec struct {
	// Account is the operator-defined AWS account name.
	Account string `json:"account,omitempty"`

	// Region is the AWS region for the cluster.
	Region string `json:"region"`

	// ClusterIdentifier is the unique Aurora cluster identifier. Immutable.
	ClusterIdentifier string `json:"clusterIdentifier"`

	// Engine is the Aurora engine ("aurora-mysql" or "aurora-postgresql"). Immutable.
	Engine string `json:"engine"`

	// EngineVersion is the engine version. Can be upgraded in-place.
	EngineVersion string `json:"engineVersion"`

	// MasterUsername is the admin username. Immutable after creation.
	MasterUsername string `json:"masterUsername"`

	// MasterUserPassword is the admin password. Can be rotated via Provision.
	MasterUserPassword string `json:"masterUserPassword"`

	// DatabaseName is the initial database name. Immutable after creation.
	DatabaseName string `json:"databaseName,omitempty"`

	// Port is the cluster port (default: 3306 for MySQL, 5432 for PostgreSQL).
	Port int32 `json:"port,omitempty"`

	// DBSubnetGroupName is the DB subnet group for VPC placement.
	DBSubnetGroupName string `json:"dbSubnetGroupName,omitempty"`

	// DBClusterParameterGroupName is the cluster parameter group.
	DBClusterParameterGroupName string `json:"dbClusterParameterGroupName,omitempty"`

	// VpcSecurityGroupIds are the VPC security group IDs.
	VpcSecurityGroupIds []string `json:"vpcSecurityGroupIds,omitempty"`

	// StorageEncrypted controls at-rest encryption. Immutable.
	StorageEncrypted bool `json:"storageEncrypted"`

	// KMSKeyId is the KMS key for encryption. Immutable.
	KMSKeyId string `json:"kmsKeyId,omitempty"`

	// BackupRetentionPeriod is the backup retention in days (1-35). Defaults to 7.
	BackupRetentionPeriod int32 `json:"backupRetentionPeriod"`

	// PreferredBackupWindow is the daily UTC backup window.
	PreferredBackupWindow string `json:"preferredBackupWindow,omitempty"`

	// PreferredMaintenanceWindow is the weekly UTC maintenance window.
	PreferredMaintenanceWindow string `json:"preferredMaintenanceWindow,omitempty"`

	// DeletionProtection prevents accidental deletion.
	DeletionProtection bool `json:"deletionProtection"`

	// EnabledCloudwatchLogsExports lists log types to export ("audit", "error", etc.).
	EnabledCloudwatchLogsExports []string `json:"enabledCloudwatchLogsExports,omitempty"`

	// Tags are key-value pairs for cost allocation and tracking.
	Tags map[string]string `json:"tags,omitempty"`
}

// AuroraClusterOutputs are produced after provisioning and stored in Restate's K/V store.
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

// ObservedState captures the actual AWS-side configuration of an Aurora cluster
// as returned by rds:DescribeDBClusters. Used for drift comparison.
type ObservedState struct {
	ClusterIdentifier            string            `json:"clusterIdentifier"`
	ClusterResourceId            string            `json:"clusterResourceId"`
	ARN                          string            `json:"arn"`
	Engine                       string            `json:"engine"`
	EngineVersion                string            `json:"engineVersion"`
	MasterUsername               string            `json:"masterUsername"`
	DatabaseName                 string            `json:"databaseName"`
	Port                         int32             `json:"port"`
	DBSubnetGroupName            string            `json:"dbSubnetGroupName"`
	DBClusterParameterGroupName  string            `json:"dbClusterParameterGroupName"`
	VpcSecurityGroupIds          []string          `json:"vpcSecurityGroupIds"`
	StorageEncrypted             bool              `json:"storageEncrypted"`
	KMSKeyId                     string            `json:"kmsKeyId"`
	BackupRetentionPeriod        int32             `json:"backupRetentionPeriod"`
	PreferredBackupWindow        string            `json:"preferredBackupWindow"`
	PreferredMaintenanceWindow   string            `json:"preferredMaintenanceWindow"`
	DeletionProtection           bool              `json:"deletionProtection"`
	EnabledCloudwatchLogsExports []string          `json:"enabledCloudwatchLogsExports"`
	Endpoint                     string            `json:"endpoint"`
	ReaderEndpoint               string            `json:"readerEndpoint"`
	Status                       string            `json:"status"`
	Tags                         map[string]string `json:"tags"`
}

// AuroraClusterState is the single atomic state object stored under drivers.StateKey.
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
