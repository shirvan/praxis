package auroracluster

import "github.com/shirvan/praxis/pkg/types"

const ServiceName = "AuroraCluster"

type AuroraClusterSpec struct {
	Account                      string            `json:"account,omitempty"`
	Region                       string            `json:"region"`
	ClusterIdentifier            string            `json:"clusterIdentifier"`
	Engine                       string            `json:"engine"`
	EngineVersion                string            `json:"engineVersion"`
	MasterUsername               string            `json:"masterUsername"`
	MasterUserPassword           string            `json:"masterUserPassword"`
	DatabaseName                 string            `json:"databaseName,omitempty"`
	Port                         int32             `json:"port,omitempty"`
	DBSubnetGroupName            string            `json:"dbSubnetGroupName,omitempty"`
	DBClusterParameterGroupName  string            `json:"dbClusterParameterGroupName,omitempty"`
	VpcSecurityGroupIds          []string          `json:"vpcSecurityGroupIds,omitempty"`
	StorageEncrypted             bool              `json:"storageEncrypted"`
	KMSKeyId                     string            `json:"kmsKeyId,omitempty"`
	BackupRetentionPeriod        int32             `json:"backupRetentionPeriod"`
	PreferredBackupWindow        string            `json:"preferredBackupWindow,omitempty"`
	PreferredMaintenanceWindow   string            `json:"preferredMaintenanceWindow,omitempty"`
	DeletionProtection           bool              `json:"deletionProtection"`
	EnabledCloudwatchLogsExports []string          `json:"enabledCloudwatchLogsExports,omitempty"`
	Tags                         map[string]string `json:"tags,omitempty"`
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
