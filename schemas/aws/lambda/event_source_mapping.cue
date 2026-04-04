package lambda

#EventSourceMapping: {
	apiVersion: "praxis.io/v1"
	kind:       "EventSourceMapping"
	metadata: name: =~"^[a-zA-Z0-9][a-zA-Z0-9._-]{0,254}$"
	spec: {
		region:                          string
		functionName:                    string
		eventSourceArn:                  string
		enabled?:                        bool | *true
		batchSize?:                      int & >=1 & <=10000
		maximumBatchingWindowInSeconds?: int & >=0 & <=300
		startingPosition?:               "TRIM_HORIZON" | "LATEST" | "AT_TIMESTAMP"
		startingPositionTimestamp?:      string
		filterCriteria?: filters: [...{
			pattern: string
		}]
		bisectBatchOnFunctionError?: bool
		maximumRetryAttempts?:       int & >=-1 & <=10000
		maximumRecordAgeInSeconds?:  int & >=-1 & <=604800
		parallelizationFactor?:      int & >=1 & <=10
		tumblingWindowInSeconds?:    int & >=0 & <=900
		destinationConfig?: onFailure: destinationArn: string
		scalingConfig?: maximumConcurrency: int & >=2 & <=1000
		functionResponseTypes?: [...("ReportBatchItemFailures")]
	}
}
