// static-website.cue — S3 bucket configured for static website hosting.
//
// Usage:
//   praxis template register examples/s3/static-website.cue --description "S3 static website bucket"
//   praxis deploy static-website --account local -f examples/s3/static-website.vars.json --key docs-prod --wait

variables: {
	name:        string & =~"^[a-z][a-z0-9-]{2,40}$"
	environment: "dev" | "staging" | "prod"
}

resources: {
	website: {
		apiVersion: "praxis.io/v1"
		kind:       "S3Bucket"
		metadata: name: "\(variables.name)-\(variables.environment)-site"
		spec: {
			region:     "us-east-1"
			versioning: true
			acl:        "private"
			encryption: {
				enabled:   true
				algorithm: "AES256"
			}
			tags: {
				app:     variables.name
				env:     variables.environment
				purpose: "website"
			}
		}
	}
}
