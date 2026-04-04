package iam

#IAMUser: {
	apiVersion: "praxis.io/v1"
	kind:       "IAMUser"

	metadata: {
		name: string & =~"^[a-zA-Z0-9+=,.@_-]{1,64}$"
		labels: [string]: string
	}

	spec: {
		path:                 string | *"/"
		permissionsBoundary?: string
		inlinePolicies: [string]: string
		managedPolicyArns: [...string] | *[]
		groups: [...string] | *[]
		tags: [string]: string
	}

	outputs?: {
		arn:      string
		userId:   string
		userName: string
	}
}
