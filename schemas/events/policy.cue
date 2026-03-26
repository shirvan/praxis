#PolicyPreventedDestroyData: {
	message:      string
	policy:       "lifecycle.preventDestroy"
	operation:    "delete" | "force-replace"
	resourceName: string
	resourceKind: string
	error:        string
	...
}