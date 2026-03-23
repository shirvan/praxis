package rdsinstance

import "github.com/praxiscloud/praxis/pkg/types"

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