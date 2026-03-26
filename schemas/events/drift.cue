package events

#DriftDetectedData: {
	message:      string
	resourceName: string
	resourceKind: string
	error?:       string
}

#DriftCorrectedData: {
	message:      string
	resourceName: string
	resourceKind: string
}

#DriftExternalDeleteData: {
	message:      string
	resourceName: string
	resourceKind: string
	error?:       string
}