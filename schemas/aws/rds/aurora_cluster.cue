package rds

#AuroraCluster: {
	apiVersion: "praxis.io/v1"
	kind:       "AuroraCluster"

	metadata: {
		name: string & =~"^[a-zA-Z][a-zA-Z0-9-]{0,61}[a-zA-Z0-9]$"
		labels: [string]: string
	}

	spec: {
		region:                       string
		engine:                       "aurora-postgresql" | "aurora-mysql"
		engineVersion:                string
		masterUsername:               string
		masterUserPassword:           string
		databaseName?:                string
		port?:                        int & >=1150 & <=65535
		dbSubnetGroupName?:           string
		dbClusterParameterGroupName?: string
		vpcSecurityGroupIds: [...string] | *[]
		storageEncrypted:            bool | *true
		kmsKeyId?:                   string
		backupRetentionPeriod:       int & >=1 & <=35 | *7
		preferredBackupWindow?:      string
		preferredMaintenanceWindow?: string
		deletionProtection:          bool | *false
		enabledCloudwatchLogsExports: [...string] | *[]
		tags: [string]: string
	}

	outputs?: {
		clusterIdentifier: string
		clusterResourceId: string
		arn:               string
		endpoint:          string
		readerEndpoint:    string
		port:              int
		engine:            string
		engineVersion:     string
		status:            string
	}
}
