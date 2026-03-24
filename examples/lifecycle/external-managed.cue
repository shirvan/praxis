// external-managed.cue — S3 bucket co-managed with external tools.
//
// Demonstrates ignoreChanges to allow external systems (billing,
// compliance scanners, log aggregation) to manage specific fields
// without Praxis overriding their changes during reconciliation.
//
// Usage:
//   praxis template register examples/lifecycle/external-managed.cue --description "S3 bucket with ignored fields"
//   praxis deploy external-managed --account local -f examples/lifecycle/external-managed.vars.json --key mybucket --wait

variables: {
	name:        string & =~"^[a-z][a-z0-9-]{2,40}$"
	environment: "dev" | "staging" | "prod"
}

resources: {
	bucket: {
		apiVersion: "praxis.io/v1"
		kind:       "S3Bucket"
		metadata: name: "\(variables.name)-\(variables.environment)-data"
		spec: {
			region:     "us-east-1"
			versioning: true
			acl:        "private"
			encryption: {
				enabled:   true
				algorithm: "AES256"
			}
			tags: {
				app:       variables.name
				env:       variables.environment
				managedBy: "praxis"
			}
		}
		// Allow external systems to manage these fields without conflicts.
		lifecycle: {
			ignoreChanges: [
				"tags.CostCenter",   // managed by billing system
				"tags.LastAudit",    // managed by compliance scanner
				"tags.ManagedBy",    // may be overwritten by external tools
			]
		}
	}
}
