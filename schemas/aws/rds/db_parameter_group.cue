package rds

#DBParameterGroup: {
	apiVersion: "praxis.io/v1"
	kind:       "DBParameterGroup"

	metadata: {
		name: string & =~"^[a-zA-Z][a-zA-Z0-9-]{0,253}[a-zA-Z0-9]$"
		labels: [string]: string
	}

	spec: {
		region:      string
		type:        "db" | "cluster" | *"db"
		family:      string
		description: string | *""
		parameters: [string]: string
		tags: [string]:       string
	}

	outputs?: {
		groupName: string
		arn:       string
		family:    string
		type:      string
	}
}
