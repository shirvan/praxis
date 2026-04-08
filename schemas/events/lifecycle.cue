#DeploymentStatus: "Pending" | "Running" | "Complete" | "Failed" | "Deleting" | "Deleted" | "Cancelled"

#DeploymentEventData: {
	message: string
	status?: #DeploymentStatus
	error?:  string
	...
}

#ResourceEventData: {
	message:      string
	status?:      #DeploymentStatus
	resourceName: string
	resourceKind: string
	error?:       string
	outputs?: [string]: _
	...
}

#DeploymentSubmittedData: #DeploymentEventData & {status: "Pending"}
#DeploymentStartedData: #DeploymentEventData & {status: "Running"}
#DeploymentCompletedData: #DeploymentEventData & {status: "Complete"}
#DeploymentFailedData: #DeploymentEventData & {status: "Failed"}
#DeploymentCancelledData: #DeploymentEventData & {status: "Cancelled"}
#DeploymentDeleteStartedData: #DeploymentEventData & {status: "Deleting"}
#DeploymentDeleteCompletedData: #DeploymentEventData & {status: "Deleted"}
#DeploymentDeleteFailedData: #DeploymentEventData & {status: "Failed"}

#ResourceReplaceStartedData: #ResourceEventData & {status: "Running"}
#ResourceAutoReplaceStartedData: #ResourceEventData & {status: "Running"}
#ResourceDispatchedData: #ResourceEventData & {status: "Running"}
#ResourceReadyData: #ResourceEventData & {status: "Running"}
#ResourceErrorData: #ResourceEventData & {status: "Running" | "Deleting", error: string}
#ResourceSkippedData: #ResourceEventData
#ResourceDeleteStartedData: #ResourceEventData & {status: "Deleting"}
#ResourceDeletedData: #ResourceEventData & {status: "Deleting"}
#ResourceDeleteErrorData: #ResourceEventData & {status: "Deleting", error: string}
