package sns

#SNSSubscription: {
	apiVersion: "praxis.io/v1"
	kind:       "SNSSubscription"

	metadata: {
		name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9._-]{0,254}$"
		labels: [string]: string
	}

	spec: {
		region: string

		topicArn: string & =~"^arn:aws:sns:[a-z0-9-]+:[0-9]{12}:.+$"

		protocol: "http" | "https" | "email" | "email-json" | "sms" | "sqs" | "lambda" | "firehose" | "application"

		endpoint: string

		filterPolicy?: string

		filterPolicyScope?: "MessageAttributes" | "MessageBody"

		rawMessageDelivery?: bool

		deliveryPolicy?: string

		redrivePolicy?: string

		subscriptionRoleArn?: string
	}

	outputs?: {
		subscriptionArn: string
		topicArn:        string
		protocol:        string
		endpoint:        string
		owner:           string
	}
}
