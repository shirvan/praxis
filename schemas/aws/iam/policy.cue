package iam

#IAMPolicy: {
	apiVersion: "praxis.io/v1"
	kind:       "IAMPolicy"

	metadata: {
		name: string & =~"^[a-zA-Z0-9+=,.@_-]{1,128}$"
		labels: [string]: string
	}

	spec: {
		path: string | *"/"
		policyDocument: string
		description?: string
		tags: [string]: string
	}

	outputs?: {
		arn:        string
		policyId:   string
		policyName: string
	}
}