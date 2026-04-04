package cloudwatch

#LogGroup: {
	apiVersion: "praxis.io/v1"
	kind:       "LogGroup"

	metadata: {
		name: string & =~"^[.\\-_/#A-Za-z0-9]{1,512}$"
		labels: [string]: string
	}

	spec: {
		region:           string
		logGroupClass:    "STANDARD" | "INFREQUENT_ACCESS" | *"STANDARD"
		retentionInDays?: 1 | 3 | 5 | 7 | 14 | 30 | 60 | 90 | 120 | 150 |
			180 | 365 | 400 | 545 | 731 | 1096 | 1827 | 2192 |
				2557 | 2922 | 3288 | 3653
		kmsKeyId?: string
		tags: [string]: string
	}

	outputs?: {
		arn:             string
		logGroupName:    string
		logGroupClass:   string
		retentionInDays: int
		kmsKeyId?:       string
		creationTime:    int
		storedBytes:     int
	}
}
