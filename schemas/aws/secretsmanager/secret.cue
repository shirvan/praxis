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
		// forceDelete deletes the secret immediately with no recovery window.
		// Defaults to false, which uses a 7-day recovery window so an accidental
		// delete can be undone. Set true only for throwaway/test secrets.
		forceDelete: bool | *false
	}

	// Outputs intentionally exclude secretString so the secret value never flows
	// into deployment state or expression hydration.
	outputs?: {
		arn:       string
		name:      string
		versionId: string
	}
}
