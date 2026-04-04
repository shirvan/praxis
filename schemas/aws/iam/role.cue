package iam

#IAMRole: {
	apiVersion: "praxis.io/v1"
	kind:       "IAMRole"

	metadata: {
		name: string & =~"^[a-zA-Z0-9+=,.@_-]{1,64}$"
		labels: [string]: string
	}

	spec: {
		path:                     string | *"/"
		assumeRolePolicyDocument: string
		description?:             string
		maxSessionDuration:       int & >=3600 & <=43200 | *3600
		permissionsBoundary?:     string
		inlinePolicies: [string]: string
		managedPolicyArns: [...string] | *[]
		tags: [string]: string
	}

	outputs?: {
		arn:      string
		roleId:   string
		roleName: string
	}
}
