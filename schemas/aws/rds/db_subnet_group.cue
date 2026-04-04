package rds

#DBSubnetGroup: {
	apiVersion: "praxis.io/v1"
	kind:       "DBSubnetGroup"

	metadata: {
		name: string & =~"^[a-zA-Z][a-zA-Z0-9-]{0,253}[a-zA-Z0-9]$"
		labels: [string]: string
	}

	spec: {
		region:      string
		description: string
		subnetIds: [...string] & [_, _, ...]
		tags: [string]: string
	}

	outputs?: {
		groupName: string
		arn:       string
		vpcId:     string
		subnetIds: [...string]
		availabilityZones: [...string]
		status: string
	}
}
