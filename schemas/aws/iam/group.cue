package iam

#IAMGroup: {
	apiVersion: "praxis.io/v1"
	kind:       "IAMGroup"

	metadata: {
		name: string & =~"^[a-zA-Z0-9+=,.@_-]{1,128}$"
		labels: [string]: string
	}

	spec: {
		path: string | *"/"
		inlinePolicies: [string]: string
		managedPolicyArns: [...string] | *[]
	}

	outputs?: {
		arn:       string
		groupId:   string
		groupName: string
	}
}
