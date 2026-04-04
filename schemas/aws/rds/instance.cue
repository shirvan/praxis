package rds

#RDSInstance: {
	apiVersion: "praxis.io/v1"
	kind:       "RDSInstance"

	metadata: {
		name: string & =~"^[a-zA-Z][a-zA-Z0-9-]{0,61}[a-zA-Z0-9]$"
		labels: [string]: string
	}

	spec: {
		region:              string
		engine:              string
		engineVersion:       string
		instanceClass:       string
		allocatedStorage?:   int & >=20 & <=65536
		storageType:         "gp2" | "gp3" | "io1" | "io2" | *"gp3"
		iops?:               int & >=1000 & <=256000
		storageThroughput?:  int & >=125 & <=4000
		storageEncrypted:    bool | *true
		kmsKeyId?:           string
		masterUsername?:     string
		masterUserPassword?: string
		dbSubnetGroupName?:  string
		parameterGroupName?: string
		vpcSecurityGroupIds: [...string] | *[]
		dbClusterIdentifier?:        string
		multiAZ:                     bool | *false
		publiclyAccessible:          bool | *false
		backupRetentionPeriod:       int & >=0 & <=35 | *7
		preferredBackupWindow?:      string
		preferredMaintenanceWindow?: string
		deletionProtection:          bool | *false
		autoMinorVersionUpgrade:     bool | *true
		monitoringInterval:          0 | 1 | 5 | 10 | 15 | 30 | 60 | *0
		monitoringRoleArn?:          string
		performanceInsightsEnabled:  bool | *false
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
