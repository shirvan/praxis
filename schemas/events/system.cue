#RetentionEventData: {
	message:             string
	workspace?:          string
	deploymentKey?:      string
	maxAge?:             string
	shippedEvents?:      int & >=0
	prunedEvents?:       int & >=0
	prunedChunks?:       int & >=0
	indexEntriesPruned?: int & >=0
	deploymentsScanned?: int & >=0
	deploymentsPruned?:  int & >=0
	failedDeployments?:  [...string]
	error?:              string
	...
}

#SinkEventData: {
	message:       string
	sinkName:      string
	sinkType?:     string
	eventType?:    string
	deploymentKey?: string
	error?:        string
	...
}

#SinkRegisteredData: #SinkEventData & {
	sinkName: string
	sinkType: string
}

#SinkRemovedData: #SinkEventData & {
	sinkName: string
}

#SinkDeliveryFailedData: #SinkEventData & {
	sinkName:  string
	eventType: string
	error:     string
}