// dynamic-buckets.cue — Generate S3 buckets from a list variable.
//
// Demonstrates CUE comprehensions: a for loop generates one
// bucket per entry in the "buckets" list variable. An optional
// logging bucket is conditionally included.
//
// Usage:
//   praxis template register examples/s3/dynamic-buckets.cue --description "Dynamic S3 buckets from list"
//   praxis deploy dynamic-buckets --account local -f examples/s3/dynamic-buckets.vars.json --key myapp --wait

variables: {
	name:          string & =~"^[a-z][a-z0-9-]{2,40}$"
	environment:   "dev" | "staging" | "prod"
	buckets:       [...string]
	enableLogging: bool | *false
}

// Hidden helper — not a resource
_naming: {
	prefix: "\(variables.name)-\(variables.environment)"
}

// Template-local definition — reusable tag block
#StandardTags: {
	app:       variables.name
	env:       variables.environment
	managedBy: "praxis"
}

resources: {
	// Generate one bucket per entry in the list
	for _, suffix in variables.buckets {
		"bucket-\(suffix)": {
			apiVersion: "praxis.io/v1"
			kind:       "S3Bucket"
			metadata: name: "\(_naming.prefix)-\(suffix)"
			spec: {
				region:     "us-east-1"
				versioning: true
				acl:        "private"
				encryption: {
					enabled:   true
					algorithm: "AES256"
				}
				tags: #StandardTags & {
					purpose: suffix
				}
			}
		}
	}

	// Conditional resource — only created when enableLogging is true
	if variables.enableLogging {
		"log-aggregator": {
			apiVersion: "praxis.io/v1"
			kind:       "S3Bucket"
			metadata: name: "\(_naming.prefix)-logs"
			spec: {
				region:     "us-east-1"
				versioning: false
				acl:        "private"
				encryption: {
					enabled:   true
					algorithm: "AES256"
				}
				tags: #StandardTags & {
					purpose: "log-aggregation"
				}
			}
		}
	}
}
