package secretsmanager

#SecretsManagerSecret: {
	apiVersion: "praxis.io/v1"
	kind:       "SecretsManagerSecret"

	metadata: {
		// The secret name. May contain ASCII letters, numbers, and /_+=.@-
		name: string & =~"^[a-zA-Z0-9/_+=.@\\-]{1,512}$"
		labels: [string]: string
	}

	spec: {
		region: string
		description?: string
		// kmsKeyId is the KMS key used to encrypt the secret value; when omitted,
		// the account default key (alias/aws/secretsmanager) is used.
		kmsKeyId?: string
		// secretString is the sensitive secret value. It is never emitted as an
		// output; reference it with a sensitivity-aware resolver instead.
		secretString: string
		tags: [string]: string
	}

	// Outputs intentionally exclude secretString so the secret value never flows
	// into deployment state or expression hydration.
	outputs?: {
		arn:       string
		name:      string
		versionId: string
	}
}
