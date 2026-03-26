#CommandEventData: {
	message:    string
	action:     string
	status?:    "Pending" | "Running" | "Complete" | "Failed" | "Deleting" | "Deleted" | "Cancelled"
	account?:   string
	resourceId?: string
	region?:    string
	...
}

#CommandApplyData: #CommandEventData & {
	action: "apply"
	status: "Pending"
}

#CommandDeleteData: #CommandEventData & {
	action: "delete"
	status: "Deleting"
}

#CommandImportData: #CommandEventData & {
	action:     "import"
	resourceId: string
	region:     string
}

#CommandCancelData: #CommandEventData & {
	action: "cancel"
	status: "Running"
}