package sns

#SNSTopic: {
	apiVersion: "praxis.io/v1"
	kind:       "SNSTopic"

	metadata: {
		name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9._-]{0,254}$"
		labels: [string]: string
	}

	spec: {
		region: string

		topicName: string & =~"^[a-zA-Z0-9_-]{1,256}(\\.fifo)?$"

		displayName?: string

		fifoTopic: bool | *false

		contentBasedDeduplication: bool | *false

		// Omitted optional attributes are declarative defaults/absence. Re-applying
		// a spec without a previously configured value removes that provider value.
		policy?: string

		deliveryPolicy?: string

		kmsMasterKeyId?: string

		tags: [string]: string
	}

	outputs?: {
		topicArn:  string
		topicName: string
		owner:     string
	}
}
