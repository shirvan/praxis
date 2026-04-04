package sqs

#SQSQueuePolicy: {
	apiVersion: "praxis.io/v1"
	kind:       "SQSQueuePolicy"

	metadata: {
		name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9._-]{0,254}$"
		labels: [string]: string
	}

	spec: {
		region:    string
		queueName: string & =~"^[a-zA-Z0-9_-]{1,80}(\\.fifo)?$"
		policy: {
			Version: "2012-10-17" | "2008-10-17" | *"2012-10-17"
			Id?:     string
			Statement: [...{
				Sid?:      string
				Effect:    "Allow" | "Deny"
				Principal: _
				Action: string | [...string]
				Resource: string | [...string]
				Condition?: _
			}]
		}
	}

	outputs?: {
		queueUrl:  string
		queueArn:  string
		queueName: string
	}
}
