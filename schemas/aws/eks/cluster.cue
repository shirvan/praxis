package eks

#EKSCluster: {
	apiVersion: "praxis.io/v1"
	kind:       "EKSCluster"

	metadata: {
		// Cluster names are 1-100 chars: letters, digits, hyphens, underscores.
		name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9_-]{0,99}$"
		labels: [string]: string
	}

	spec: {
		region: string

		// Immutable after creation — changes surface as informational,
		// requires-replacement diffs during reconciliation.
		roleArn:          string
		subnetIds: [...string]
		securityGroupIds: [...string]

		// Mutable — the Kubernetes control-plane version. When omitted, AWS
		// selects the current default. Only upgrades are supported.
		version?: string

		// Mutable — control-plane endpoint access. Defaults mirror AWS.
		endpointPublicAccess:  bool | *true
		endpointPrivateAccess: bool | *false
		publicAccessCidrs: [...string]

		// Mutable — control-plane log types shipped to CloudWatch Logs.
		enabledLoggingTypes: [...("api" | "audit" | "authenticator" | "controllerManager" | "scheduler")]

		tags: [string]: string
	}

	outputs?: {
		arn:             string
		name:            string
		status:          string
		version:         string
		platformVersion: string
		endpoint:        string
	}
}
