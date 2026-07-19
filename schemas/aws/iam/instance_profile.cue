package iam

#IAMInstanceProfile: {
	apiVersion: "praxis.io/alpha"
	kind:       "IAMInstanceProfile"

	metadata: {
		name: string & =~"^[a-zA-Z0-9+=,.@_-]{1,128}$"
		labels: [string]: string
	}

	spec: {
		path:     string | *"/"
		roleName: string
		tags: [string]: string
	}

	outputs?: {
		arn:                 string
		instanceProfileId:   string
		instanceProfileName: string
	}
}
