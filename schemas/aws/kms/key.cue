package kms

#KMSKey: {
	apiVersion: "praxis.io/v1"
	kind:       "KMSKey"

	metadata: {
		// The alias short name (without the "alias/" prefix). Alias names are
		// 1-256 chars: letters, digits, and the characters _ - / . The driver
		// derives the full alias "alias/<name>" internally.
		name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9/_-]{0,250}$"
		labels: [string]: string
	}

	spec: {
		region: string

		// Mutable — a human-readable description converged in place.
		description?: string

		// Immutable after creation — changes surface as informational,
		// requires-replacement diffs during reconciliation.
		keyUsage: "ENCRYPT_DECRYPT" | "SIGN_VERIFY" | "GENERATE_VERIFY_MAC" | *"ENCRYPT_DECRYPT"
		keySpec:  string | *"SYMMETRIC_DEFAULT"

		// Mutable — whether automatic annual key rotation is enabled.
		enableKeyRotation: bool | *false

		// Used only at delete time: the waiting period (in days) before AWS
		// permanently deletes the key after ScheduleKeyDeletion.
		deletionWindowInDays: int & >=7 & <=30 | *30

		tags: [string]: string
	}

	outputs?: {
		arn:       string
		keyId:     string
		aliasName: string
	}
}
