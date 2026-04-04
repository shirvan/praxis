package sqs

#SQSQueue: {
	apiVersion: "praxis.io/v1"
	kind:       "SQSQueue"

	metadata: {
		name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9._-]{0,79}$"
		labels: [string]: string
	}

	spec: {
		region:                        string
		queueName:                     string & =~"^[a-zA-Z0-9_-]{1,80}(\\.fifo)?$"
		fifoQueue:                     bool | *false
		visibilityTimeout:             int & >=0 & <=43200 | *30
		messageRetentionPeriod:        int & >=60 & <=1209600 | *345600
		maximumMessageSize:            int & >=1024 & <=262144 | *262144
		delaySeconds:                  int & >=0 & <=900 | *0
		receiveMessageWaitTimeSeconds: int & >=0 & <=20 | *0
		redrivePolicy?: {
			deadLetterTargetArn: string
			maxReceiveCount:     int & >=1 & <=1000
		}
		sqsManagedSseEnabled:         bool | *true
		kmsMasterKeyId?:              string
		kmsDataKeyReusePeriodSeconds: int & >=60 & <=86400 | *300
		contentBasedDeduplication:    bool | *false
		deduplicationScope?:          "queue" | "messageGroup"
		fifoThroughputLimit?:         "perQueue" | "perMessageGroupId"
		tags: [string]: string
	}

	outputs?: {
		queueUrl:  string
		queueArn:  string
		queueName: string
	}
}
