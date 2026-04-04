package ecr

#ECRLifecyclePolicy: {
	apiVersion: "praxis.io/v1"
	kind:       "ECRLifecyclePolicy"

	metadata: {
		name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9._-]{0,254}$"
		labels: [string]: string
	}

	spec: {
		region:         string
		repositoryName: string & =~"^[a-z0-9][a-z0-9/_.-]{1,255}$"

		lifecyclePolicyText: string & =~"^\\s*\\{"
	}

	outputs?: {
		repositoryName: string
		repositoryArn:  string
		registryId:     string
	}
}
