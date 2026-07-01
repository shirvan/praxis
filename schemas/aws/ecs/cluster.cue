package ecs

#ECSCluster: {
	apiVersion: "praxis.io/v1"
	kind:       "ECSCluster"

	metadata: {
		// Cluster names are up to 255 chars: letters, digits, hyphens, underscores.
		name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9_-]{0,254}$"
		labels: [string]: string
	}

	spec: {
		region: string

		// Mutable — CloudWatch Container Insights for the cluster. Maps to the
		// cluster setting named "containerInsights". Defaults to disabled.
		containerInsights: *"disabled" | "enabled"

		// Mutable — the capacity providers associated with the cluster, e.g.
		// FARGATE and FARGATE_SPOT. Converged in place via
		// PutClusterCapacityProviders.
		capacityProviders: [...string]

		tags: [string]: string
	}

	outputs?: {
		arn:    string
		name:   string
		status: string
	}
}
