#PolicyPreventedDestroyData: {
	message:      string
	policy:       "lifecycle.preventDestroy"
	operation:    "delete" | "force-replace" | "rollback"
	resourceName: string
	resourceKind: string
	error:        string
	...
}

#ForceDeleteOverrideData: {
	message:      string
	policy:       "lifecycle.preventDestroy"
	operation:    "delete" | "rollback"
	resourceName: string
	resourceKind: string
	error:        string
	...
}
